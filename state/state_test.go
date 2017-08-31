package state

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"

	abci "github.com/tendermint/abci/types"

	crypto "github.com/tendermint/go-crypto"

	dbm "github.com/tendermint/tmlibs/db"
	"github.com/tendermint/tmlibs/log"

	cfg "github.com/tendermint/tendermint/config"
)

// setupTestCase does setup common to all test cases
func setupTestCase(t *testing.T) (func(t *testing.T), dbm.DB, *State) {
	config := cfg.ResetTestRoot("state_")
	stateDB := dbm.NewDB("state", config.DBBackend, config.DBDir())
	state := GetState(stateDB, config.GenesisFile())
	state.SetLogger(log.TestingLogger())

	tearDown := func(t *testing.T) {}

	return tearDown, stateDB, state
}

func TestStateCopy(t *testing.T) {
	tearDown, _, state := setupTestCase(t)
	defer tearDown(t)

	stateCopy := state.Copy()

	if !state.Equals(stateCopy) {
		t.Fatal(`Expected state and its copy to be identical.
Got %v\n expected %v\n`, stateCopy, state)
	}

	stateCopy.LastBlockHeight++

	if state.Equals(stateCopy) {
		t.Fatal(`Expected states to be different.
Got the same %v`, state)
	}
}

func TestStateSaveLoad(t *testing.T) {
	tearDown, stateDB, state := setupTestCase(t)
	defer tearDown(t)

	state.LastBlockHeight++
	state.Save()

	loadedState := LoadState(stateDB)
	if !state.Equals(loadedState) {
		t.Fatal(`Expected state and its copy to be identical.
Got %v\n expected %v\n`, loadedState, state)
	}
}

func TestABCIResponsesSaveLoad(t *testing.T) {
	tearDown, _, state := setupTestCase(t)
	defer tearDown(t)
	assert := assert.New(t)

	state.LastBlockHeight++

	// build mock responses
	block := makeBlock(2, state)
	abciResponses := NewABCIResponses(block)
	abciResponses.DeliverTx[0] =
		&abci.ResponseDeliverTx{Data: []byte("foo")}
	abciResponses.DeliverTx[1] =
		&abci.ResponseDeliverTx{Data: []byte("bar"), Log: "ok"}
	abciResponses.EndBlock =
		abci.ResponseEndBlock{Diffs: []*abci.Validator{{
			PubKey: crypto.GenPrivKeyEd25519().PubKey().
				Bytes(),
			Power: 10},
		}}
	abciResponses.txs = nil

	state.SaveABCIResponses(abciResponses)
	abciResponses2 := state.LoadABCIResponses()
	assert.Equal(abciResponses, abciResponses2,
		fmt.Sprintf(`ABCIResponses don't match:
Got %v, Expected %v`, abciResponses2, abciResponses))
}
