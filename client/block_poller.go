package client

import "github.com/pokt-network/poktroll/pkg/client"

// simpleBlock implements client.Block interface.
// This is a lightweight implementation used by both BlockPoller (deprecated)
// and BlockSubscriber for representing blockchain blocks.
type simpleBlock struct {
	height int64
	hash   []byte
}

func (b *simpleBlock) Height() int64 { return b.height }
func (b *simpleBlock) Hash() []byte  { return b.hash }

// Ensure simpleBlock implements client.Block
var _ client.Block = (*simpleBlock)(nil)
