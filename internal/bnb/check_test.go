package bnb

import (
	"context"
	"crypto/sha512"
	"math/big"
	"testing"

	"github.com/btcsuite/btcd/btcutil/hdkeychain"
	"github.com/btcsuite/btcd/chaincfg"
	"golang.org/x/crypto/pbkdf2"
)

type readOnlyChain struct{ calls int }

func (c *readOnlyChain) ChainID(context.Context) (uint64, error) { c.calls++; return 56, nil }
func (c *readOnlyChain) Head(context.Context) (uint64, error)    { c.calls++; return 123, nil }
func (c *readOnlyChain) Balance(context.Context, string) (*big.Int, error) {
	return big.NewInt(0), nil
}
func (c *readOnlyChain) PendingNonce(context.Context, string) (uint64, error) { return 0, nil }
func (c *readOnlyChain) GasPrice(context.Context) (*big.Int, error)          { return big.NewInt(0), nil }
func (c *readOnlyChain) Block(context.Context, uint64) (Block, error) {
	panic("check must not scan blocks")
}
func (c *readOnlyChain) Receipt(context.Context, string) (Receipt, error) {
	panic("check must not read receipts")
}
func (c *readOnlyChain) TxStatus(context.Context, string) (bool, bool, bool, error) {
	return false, false, false, nil
}
func (c *readOnlyChain) SendRaw(context.Context, string) error { return nil }
func (c *readOnlyChain) BlockByHash(context.Context, string) (Block, error) {
	return Block{}, nil
}

func TestCheckShowsTwoAddressesWithoutMutation(t *testing.T) {
	seed := pbkdf2.Key([]byte("test test test test test test test test test test test junk"), []byte("mnemonic"), 2048, 64, sha512.New)
	key, e := hdkeychain.NewMaster(seed, &chaincfg.MainNetParams)
	if e != nil {
		t.Fatal(e)
	}
	for _, i := range []uint32{44 + hdkeychain.HardenedKeyStart, 60 + hdkeychain.HardenedKeyStart, hdkeychain.HardenedKeyStart, 0} {
		key, e = key.Derive(i)
		if e != nil {
			t.Fatal(e)
		}
	}
	pub, e := key.Neuter()
	if e != nil {
		t.Fatal(e)
	}
	d, e := NewDeriver(pub.String())
	if e != nil {
		t.Fatal(e)
	}
	chain := &readOnlyChain{}
	r, e := Check(context.Background(), d, chain, 56, "0xf39Fd6e51aad88F6F4ce6aB8827279cffFb92266")
	if e != nil {
		t.Fatal(e)
	}
	if !r.ReadOnly || !r.NetworkMatches || !r.ReferenceMatches || r.HeadBlock != 123 || r.Address0 == r.Address1 || chain.calls != 2 {
		t.Fatalf("check result: %+v calls=%d", r, chain.calls)
	}
}
