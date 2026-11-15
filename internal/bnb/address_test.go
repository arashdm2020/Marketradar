package bnb

import (
	"crypto/sha512"
	"github.com/btcsuite/btcd/btcutil/hdkeychain"
	"github.com/btcsuite/btcd/chaincfg"
	"golang.org/x/crypto/pbkdf2"
	"testing"
)

func TestReferenceWalletAddress(t *testing.T) {
	mnemonic := "test test test test test test test test test test test junk"
	seed := pbkdf2.Key([]byte(mnemonic), []byte("mnemonic"), 2048, 64, sha512.New)
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
	if _, e = NewDeriver(key.String()); e == nil {
		t.Fatal("private extended key accepted")
	}
	d, e := NewDeriver(pub.String())
	if e != nil {
		t.Fatal(e)
	}
	addr, e := d.Address(0)
	if e != nil {
		t.Fatal(e)
	}
	if addr != "0xf39Fd6e51aad88F6F4ce6aB8827279cffFb92266" {
		t.Fatalf("reference wallet mismatch: %s", addr)
	}
}
