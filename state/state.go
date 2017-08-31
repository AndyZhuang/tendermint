package state

import (
	"bytes"
	"io/ioutil"
	"sync"
	"time"

	abci "github.com/tendermint/abci/types"

	wire "github.com/tendermint/go-wire"

	cmn "github.com/tendermint/tmlibs/common"
	dbm "github.com/tendermint/tmlibs/db"
	"github.com/tendermint/tmlibs/log"

	"github.com/tendermint/tendermint/state/txindex"
	"github.com/tendermint/tendermint/state/txindex/null"
	"github.com/tendermint/tendermint/types"
)

var (
	stateKey         = []byte("stateKey")
	abciResponsesKey = []byte("abciResponsesKey")
)

//------------------------------------------------------------------------

// State encapsulates the state of the Tendermint core blockchain.
// NOTE: The state struct is not goroutine-safe.
type State struct {
	// mtx for writing to db
	mtx sync.Mutex
	db  dbm.DB

	// should not change
	GenesisDoc *types.GenesisDoc
	ChainID    string

	// Updated at end of SetBlockAndValidators
	// Genesis state has this set to 0.  So, Block(H=0) does not exist
	LastBlockHeight int
	LastBlockID     types.BlockID
	LastBlockTime   time.Time
	Validators      *types.ValidatorSet
	// block.LastCommit validated against LastValidators
	LastValidators *types.ValidatorSet
	// AppHash is updated after Commit
	AppHash []byte

	TxIndexer txindex.TxIndexer `json:"-"` // Transaction indexer

	logger log.Logger
}

// LoadState pulls the data from the db argument and puts it into
// the returned state object.
func LoadState(db dbm.DB) *State {
	return loadState(db, stateKey)
}

func loadState(db dbm.DB, key []byte) *State {
	s := &State{db: db, TxIndexer: &null.TxIndex{}}
	buf := db.Get(key)

	if len(buf) == 0 {
		return nil
	}

	r, n, err := bytes.NewReader(buf), new(int), new(error)
	wire.ReadBinaryPtr(&s, r, 0, n, err)
	if *err != nil {
		// NOTE: DATA HAS BEEN CORRUPTED OR THE SPEC HAS CHANGED
		cmn.Exit(cmn.Fmt(`LoadState: Data has been corrupted or
its spec has changed: %v\n`, *err))
	}

	// TODO: ensure that buf is completely read.
	return s
}

// SetLogger sets the logger on this state.
func (s *State) SetLogger(l log.Logger) {
	s.logger = l
}

// Copy returns an independent copy of the state except for the
// TxIndexer field.
// TODO: Create a value copy of TxIndexer to provide a fully independent
// copy of the state.
func (s *State) Copy() *State {
	return &State{
		db:              s.db,
		GenesisDoc:      s.GenesisDoc,
		ChainID:         s.ChainID,
		LastBlockHeight: s.LastBlockHeight,
		LastBlockID:     s.LastBlockID,
		LastBlockTime:   s.LastBlockTime,
		Validators:      s.Validators.Copy(),
		LastValidators:  s.LastValidators.Copy(),
		AppHash:         s.AppHash,
		TxIndexer:       s.TxIndexer, // pointer here, not value
		logger:          s.logger,
	}
}

// Save writes the state to disk.
func (s *State) Save() {
	s.mtx.Lock()
	defer s.mtx.Unlock()
	s.db.SetSync(stateKey, s.Bytes())
}

// SaveABCIResponses sets them in the state and writes them to disk
// in case we crash after app.Commit and before s.Save()
func (s *State) SaveABCIResponses(abciResponses *ABCIResponses) {
	// save the validators to the db
	s.db.SetSync(abciResponsesKey, abciResponses.Bytes())
}

// LoadABCIResponses reads the responses from disk.
func (s *State) LoadABCIResponses() *ABCIResponses {
	abciResponses := new(ABCIResponses)

	buf := s.db.Get(abciResponsesKey)
	if len(buf) != 0 {
		r, n, err := bytes.NewReader(buf), new(int), new(error)
		wire.ReadBinaryPtr(abciResponses, r, 0, n, err)
		if *err != nil {
			// NOTE: DATA HAS BEEN CORRUPTED OR THE SPEC HAS
			// CHANGED
			cmn.Exit(cmn.Fmt(`LoadABCIResponses: Data has been
corrupted or its spec has changed: %v\n`, *err))
		}
		// TODO: ensure that buf is completely read.
	}
	return abciResponses
}

// Equals compares two states for equality by comparing the byte
// representation.
func (s *State) Equals(s2 *State) bool {
	return bytes.Equal(s.Bytes(), s2.Bytes())
}

// Bytes transforms a state into a byte slice.
func (s *State) Bytes() []byte {
	buf, n, err := new(bytes.Buffer), new(int), new(error)
	wire.WriteBinary(s, buf, n, err)
	if *err != nil {
		cmn.PanicCrisis(*err)
	}
	return buf.Bytes()
}

// SetBlockAndValidators mutates state variables to match block and
// validators after running EndBlock.
func (s *State) SetBlockAndValidators(header *types.Header,
	blockPartsHeader types.PartSetHeader,
	abciResponses *ABCIResponses) {

	// copy the valset so we can apply changes from EndBlock
	// and update s.LastValidators and s.Validators
	prevValSet := s.Validators.Copy()
	nextValSet := prevValSet.Copy()

	// update the validator set with the latest abciResponses
	err := updateValidators(nextValSet, abciResponses.EndBlock.Diffs)
	if err != nil {
		s.logger.Error("Error changing validator set", "err", err)
		// TODO: err or carry on?
	}

	// Update validator accums and set state variables
	nextValSet.IncrementAccum(1)

	s.setBlockAndValidators(header.Height,
		types.BlockID{header.Hash(), blockPartsHeader},
		header.Time,
		prevValSet, nextValSet)
}

func (s *State) setBlockAndValidators(
	height int, blockID types.BlockID, blockTime time.Time,
	prevValSet, nextValSet *types.ValidatorSet) {

	s.LastBlockHeight = height
	s.LastBlockID = blockID
	s.LastBlockTime = blockTime
	s.Validators = nextValSet
	s.LastValidators = prevValSet
}

// GetValidators returns LastValidators and Validators.
func (s *State) GetValidators() (*types.ValidatorSet,
	*types.ValidatorSet) {

	return s.LastValidators, s.Validators
}

// GetState loads the most recent state from "state" db or creates a new
// one from genesis. If a new state is created it is also saved to disk.
func GetState(stateDB dbm.DB, genesisFile string) *State {
	state := LoadState(stateDB)
	if state == nil {
		state = MakeGenesisStateFromFile(stateDB, genesisFile)
		state.Save()
	}

	return state
}

//------------------------------------------------------------------------

// ABCIResponses holds intermediate state during block processing
type ABCIResponses struct {
	Height int

	DeliverTx []*abci.ResponseDeliverTx
	EndBlock  abci.ResponseEndBlock

	txs types.Txs // reference for indexing results by hash
}

// NewABCIResponses creates an initialised but empty ABCIResponses
// pointer.
func NewABCIResponses(block *types.Block) *ABCIResponses {
	return &ABCIResponses{
		Height:    block.Height,
		DeliverTx: make([]*abci.ResponseDeliverTx, block.NumTxs),
		txs:       block.Data.Txs,
	}
}

// Bytes serialises the ABCIResponse.
func (a *ABCIResponses) Bytes() []byte {
	buf, n, err := new(bytes.Buffer), new(int), new(error)
	wire.WriteBinary(*a, buf, n, err)
	if *err != nil {
		cmn.PanicCrisis(*err)
	}
	return buf.Bytes()
}

//------------------------------------------------------------------------
// Genesis

// MakeGenesisStateFromFile reads and unmarshals state from the given
// file.
//
// Used during replay and in tests.
func MakeGenesisStateFromFile(db dbm.DB, genDocFile string) *State {
	genDocJSON, err := ioutil.ReadFile(genDocFile)
	if err != nil {
		cmn.Exit(cmn.Fmt("Couldn't read GenesisDoc file: %v", err))
	}
	genDoc, err := types.GenesisDocFromJSON(genDocJSON)
	if err != nil {
		cmn.Exit(cmn.Fmt("Error reading GenesisDoc: %v", err))
	}
	return MakeGenesisState(db, genDoc)
}

// MakeGenesisState creates state from types.GenesisDoc.
//
// Used in tests.
func MakeGenesisState(db dbm.DB, genDoc *types.GenesisDoc) *State {
	if len(genDoc.Validators) == 0 {
		cmn.Exit(cmn.Fmt("The genesis file has no validators"))
	}

	if genDoc.GenesisTime.IsZero() {
		genDoc.GenesisTime = time.Now()
	}

	// Make validators slice
	validators := make([]*types.Validator, len(genDoc.Validators))
	for i, val := range genDoc.Validators {
		pubKey := val.PubKey
		address := pubKey.Address()

		// Make validator
		validators[i] = &types.Validator{
			Address:     address,
			PubKey:      pubKey,
			VotingPower: val.Amount,
		}
	}

	return &State{
		db:              db,
		GenesisDoc:      genDoc,
		ChainID:         genDoc.ChainID,
		LastBlockHeight: 0,
		LastBlockID:     types.BlockID{},
		LastBlockTime:   genDoc.GenesisTime,
		Validators:      types.NewValidatorSet(validators),
		LastValidators:  types.NewValidatorSet(nil),
		AppHash:         genDoc.AppHash,
		// we do not need indexer during replay and in tests
		TxIndexer: &null.TxIndex{},
	}
}
