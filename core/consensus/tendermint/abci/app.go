package abci

import (
	"encoding/json"
	"fmt"
	"sync"

	"github.com/gallactic/gallactic/core/blockchain"
	"github.com/gallactic/gallactic/core/consensus/tendermint/codes"
	"github.com/gallactic/gallactic/core/execution"
	"github.com/gallactic/gallactic/crypto"
	"github.com/gallactic/gallactic/txs"
	"github.com/gallactic/gallactic/version"
	log "github.com/inconshreveable/log15"
	"github.com/pkg/errors"
	abciTypes "github.com/tendermint/tendermint/abci/types"
	"github.com/tendermint/tendermint/libs/common"
)

const responseInfoName = "Gallactic"

type App struct {
	// State
	bc            *blockchain.Blockchain
	checker       execution.BatchExecutor
	committer     execution.BatchCommitter
	mempoolLocker sync.Locker
	// We need to cache these from BeginBlock for when we need actually need it in Commit
	block *abciTypes.RequestBeginBlock
}

var _ abciTypes.Application = &App{}

func NewApp(bc *blockchain.Blockchain, checker execution.BatchExecutor, committer execution.BatchCommitter) *App {
	return &App{
		bc:        bc,
		checker:   checker,
		committer: committer,
	}
}

// Provide the Mempool lock. When provided we will attempt to acquire this lock in a goroutine during the Commit. We
// will keep the checker cache locked until we are able to acquire the mempool lock which signals the end of the commit
// and possible recheck on Tendermint's side.
func (app *App) SetMempoolLocker(mempoolLocker sync.Locker) {
	app.mempoolLocker = mempoolLocker
}

func (app *App) Info(info abciTypes.RequestInfo) abciTypes.ResponseInfo {
	return abciTypes.ResponseInfo{
		Data:             responseInfoName,
		Version:          version.Version,
		LastBlockHeight:  int64(app.bc.LastBlockHeight()),
		LastBlockAppHash: app.bc.LastAppHash(),
	}
}

func (app *App) SetOption(option abciTypes.RequestSetOption) (respSetOption abciTypes.ResponseSetOption) {
	respSetOption.Log = "SetOption not supported"
	respSetOption.Code = codes.UnsupportedRequestCode
	return
}

func (app *App) Query(reqQuery abciTypes.RequestQuery) (respQuery abciTypes.ResponseQuery) {
	respQuery.Log = "Query not supported"
	respQuery.Code = codes.UnsupportedRequestCode
	return
}

func (app *App) CheckTx(txBytes []byte) abciTypes.ResponseCheckTx {
	txEnv := new(txs.Envelope)
	if err := txEnv.Decode(txBytes); err != nil {
		log.Error("CheckTx decoding error",
			"error", err)

		return abciTypes.ResponseCheckTx{
			Code: codes.EncodingErrorCode,
			Log:  fmt.Sprintf("Encoding error: %s", err),
		}
	}
	txRec := txEnv.GenerateReceipt()
	err := app.checker.Execute(txEnv, txRec)
	if err != nil {
		log.Error("CheckTx execution error",
			"error", err,
			"tx_hash", txRec.Hash)

		return abciTypes.ResponseCheckTx{
			Code: codes.EncodingErrorCode,
			Log:  fmt.Sprintf("CheckTx could not execute transaction: %s, error: %v", txEnv, err),
		}
	}

	receiptBytes, err := json.Marshal(txRec)
	if err != nil {
		return abciTypes.ResponseCheckTx{
			Code: codes.TxExecutionErrorCode,
			Log:  fmt.Sprintf("CheckTx could not serialize receipt: %s", err),
		}
	}
	log.Debug("CheckTx success",
		"tx_hash", txRec.Hash)

	return abciTypes.ResponseCheckTx{
		Code: codes.TxExecutionSuccessCode,
		Log:  "CheckTx success - receipt in data",
		Data: receiptBytes,
	}
}

func (app *App) InitChain(chain abciTypes.RequestInitChain) (respInitChain abciTypes.ResponseInitChain) {
	// Could verify agreement on initial validator set here
	return
}

func (app *App) BeginBlock(block abciTypes.RequestBeginBlock) (respBeginBlock abciTypes.ResponseBeginBlock) {
	app.block = &block

	set := app.bc.ValidatorSet()
	state := app.bc.State()
	byzantines := block.ByzantineValidators

	for _, b := range byzantines {
		addr, err := crypto.ValidatorAddress(b.Validator.Address)
		if err != nil {
			/// TODO:::
		} else {
			/// remove Byzantine validator from state and set
			set.ForceLeave(addr)
			state.ByzantineValidator(addr)
		}
	}

	app.bc.EvaluateSortition(uint64(block.Header.Height), block.Hash)

	return
}

func (app *App) DeliverTx(txBytes []byte) abciTypes.ResponseDeliverTx {
	txEnv := new(txs.Envelope)
	if err := txEnv.Decode(txBytes); err != nil {
		log.Error("DeliverTx decoding error",
			"error", err)

		app.mempoolLocker.Unlock()
		return abciTypes.ResponseDeliverTx{
			Code: codes.EncodingErrorCode,
			Log:  fmt.Sprintf("Encoding error: %s", err),
		}
	}

	txRec := txEnv.GenerateReceipt()
	txRec.Height = app.block.Header.Height
	if err := app.committer.Execute(txEnv, txRec); err != nil {
		log.Error("DeliverTx execution error",
			"error", err,
			"tx_hash", txRec.Hash)

		return abciTypes.ResponseDeliverTx{
			Code: codes.TxExecutionErrorCode,
			Log:  fmt.Sprintf("DeliverTx could not execute transaction: %s, error: %s", txEnv, err),
		}
	}

	var tags []common.KVPair
	var logTag common.KVPair
	bs, err := txRec.Logs.MarshalBinary()

	if err != nil {
		return abciTypes.ResponseDeliverTx{
			Code: codes.TxExecutionErrorCode,
			Log:  fmt.Sprintf("DeliverTx could not serialize logs: %s", err),
		}
	}

	logTag.Key = []byte("evm.log")
	logTag.Value = bs
	tags = append(tags, logTag)

	return abciTypes.ResponseDeliverTx{
		Code:      codes.TxExecutionSuccessCode,
		Log:       "DeliverTx success - receipt in data",
		GasUsed:   int64(txRec.GasUsed),
		GasWanted: int64(txRec.GasWanted),
		Tags:      tags,
	}
}

func (app *App) EndBlock(reqEndBlock abciTypes.RequestEndBlock) abciTypes.ResponseEndBlock {
	/// Update validator set
	set := app.bc.ValidatorSet()
	set.AdjustPower(reqEndBlock.GetHeight())
	vals := set.Validators()
	leavers := set.Leavers()

	updates := make([]abciTypes.ValidatorUpdate, len(vals)+len(leavers))
	i := 0
	for _, v := range vals {
		updates[i].Power = v.Power()
		updates[i].PubKey = v.PublicKey().ABCIPubKey()
		i++
	}

	for _, v := range leavers {
		updates[i].Power = 0
		updates[i].PubKey = v.PublicKey().ABCIPubKey()
		i++
	}

	return abciTypes.ResponseEndBlock{
		ValidatorUpdates: updates,
	}
}

func (app *App) Commit() abciTypes.ResponseCommit {
	log.Debug("Committing block",
		"height", app.block.Header.Height,
		"hash", app.block.Hash,
		"txs", app.block.Header.NumTxs,
		"block_time", app.block.Header.Time, // [CSK] this sends a fairly non-sensical number; should be human readable
		"last_block_time", app.bc.LastBlockTime(),
		"last_block_hash", app.bc.LastBlockHash())

	// Lock the checker while we reset it and possibly while recheckTxs replays transactions
	app.checker.Lock()
	defer func() {
		// Tendermint may replay transactions to the check cache during a recheck, which happens after we have returned
		// from Commit(). The mempool is locked by Tendermint for the duration of the commit phase; during Commit() and
		// the subsequent mempool.Update() so we schedule an acquisition of the mempool lock in a goroutine in order to
		// 'observe' the mempool unlock event that happens later on. By keeping the checker read locked during that
		// period we can ensure that anything querying the checker (such as service.MempoolAccounts()) will block until
		// the full Tendermint commit phase has completed.
		if app.mempoolLocker != nil {
			go func() {
				// we won't get this until after the commit and we will acquire strictly after this commit phase has
				// ended (i.e. when Tendermint's BlockExecutor.Commit() returns
				app.mempoolLocker.Lock()
				// Prevent any mempool getting relocked while we unlock - we could just unlock immediately but if a new
				// commit starts gives goroutines blocked on checker a chance to progress before the next commit phase
				defer app.mempoolLocker.Unlock()
				app.checker.Unlock()
			}()
		} else {
			// If we have not be provided with access to the mempool lock
			app.checker.Unlock()
		}
	}()

	// First commit the app start, this app hash will not get checkpointed until the next block when we are sure
	// that nothing in the downstream commit process could have failed. At worst we go back one block.
	err := app.committer.Commit()
	if err != nil {
		panic(errors.Wrap(err, "Could not commit transactions in block to execution state"))
	}

	/// Pay fees to the proposer
	if app.block.Header.ProposerAddress != nil {
		addr, err := crypto.ValidatorAddress(app.block.Header.ProposerAddress)
		if err != nil {
			panic(errors.Wrap(err, "invalid address for the proposer"))
		}
		st := app.bc.State()
		fee := app.committer.Fees()
		if err := st.IncentivizeValidator(addr, fee); err != nil {
			panic(errors.Wrap(err, "could not update proposer information"))
		}
	}

	// Commit to our blockchain state which will checkpoint the previous app hash by saving it to the database
	// (we know the previous app hash is safely committed because we are about to commit the next)
	appHash, err := app.bc.CommitBlock(app.block.Header.Time, app.block.Hash)
	if err != nil {
		panic(errors.Wrap(err, "could not commit block to blockchain state"))
	}

	err = app.checker.Reset()
	if err != nil {
		panic(errors.Wrap(err, "could not reset check cache during commit"))
	}

	// Perform a sanity check our block height
	if app.bc.LastBlockHeight() != uint64(app.block.Header.Height) {
		panic(fmt.Errorf("gallactic has recorded a block height of %v, "+
			"but Tendermint reports a block height of %v, and the two should agree",
			app.bc.LastBlockHeight(), app.block.Header.Height))
	}
	return abciTypes.ResponseCommit{
		Data: appHash,
	}
}
