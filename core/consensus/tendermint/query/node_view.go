package query

import (
	"fmt"

	"github.com/gallactic/gallactic/core/consensus/tendermint"
	"github.com/gallactic/gallactic/crypto"
	"github.com/gallactic/gallactic/txs"
	"github.com/tendermint/tendermint/consensus"
	consensusTypes "github.com/tendermint/tendermint/consensus/types"
	tmEd25519 "github.com/tendermint/tendermint/crypto/ed25519"
	"github.com/tendermint/tendermint/p2p"
	"github.com/tendermint/tendermint/state"
	"github.com/tendermint/tendermint/types"
)

type NodeView struct {
	tmNode *tendermint.Node
}

func NewNodeView(tmNode *tendermint.Node) *NodeView {
	return &NodeView{
		tmNode: tmNode,
	}
}

func (nv *NodeView) NodeInfo() p2p.NodeInfo {
	return nv.tmNode.NodeInfo()
}

func (nv *NodeView) Peers() p2p.IPeerSet {
	return nv.tmNode.Switch().Peers()
}

func (nv *NodeView) BlockStore() state.BlockStoreRPC {
	return nv.tmNode.BlockStore()
}

// Pass -1 to get all available transactions
func (nv *NodeView) MempoolTransactions(maxTxs int) ([]*txs.Envelope, error) {
	var transactions []*txs.Envelope
	for _, txBytes := range nv.tmNode.MempoolReactor().Mempool.ReapMaxTxs(maxTxs) {
		txEnv := new(txs.Envelope)
		if err := txEnv.Decode(txBytes); err != nil {
			return nil, err
		}
		transactions = append(transactions, txEnv)
	}
	return transactions, nil
}

func (nv *NodeView) RoundState() *consensusTypes.RoundState {
	return nv.tmNode.ConsensusState().GetRoundState()
}

func (nv *NodeView) RoundStateJSON() ([]byte, error) {
	return nv.tmNode.ConsensusState().GetRoundStateJSON()
}

func (nv *NodeView) PeerRoundStates() ([]*consensusTypes.PeerRoundState, error) {
	peers := nv.tmNode.Switch().Peers().List()
	peerRoundStates := make([]*consensusTypes.PeerRoundState, len(peers))
	for i, peer := range peers {
		peerState, ok := peer.Get(types.PeerStateKey).(*consensus.PeerState)
		if !ok {
			return nil, fmt.Errorf("could not get PeerState for peer: %s", peer)
		}
		peerRoundStates[i] = peerState.GetRoundState()
	}
	return peerRoundStates, nil
}

func (nv *NodeView) PrivValidatorPublicKey() (crypto.PublicKey, error) {
	pub := nv.tmNode.PrivValidator().GetPubKey().(tmEd25519.PubKeyEd25519)

	return crypto.PublicKeyFromRawBytes(pub[:])
}

// func (nv *NodeView) DefaultNodeInfo() p2p.DefaultNodeInfo {
// 	return nv.tmNode.NodeInfo()
// }
