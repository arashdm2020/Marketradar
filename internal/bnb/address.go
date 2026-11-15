package bnb

import (
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/btcsuite/btcd/btcutil/hdkeychain"
	"golang.org/x/crypto/sha3"
	"strings"
)

// Deriver accepts only the public external-chain node m/44'/60'/0'/0.
// The operator must verify the ancestors against the offline reference wallet.
type Deriver struct{ key *hdkeychain.ExtendedKey }

func NewDeriver(xpub string) (*Deriver, error) {
	k, e := hdkeychain.NewKeyFromString(strings.TrimSpace(xpub))
	if e != nil {
		return nil, fmt.Errorf("invalid xpub: %w", e)
	}
	if k.IsPrivate() || k.Depth() != 4 || k.ChildIndex() != 0 {
		return nil, errors.New("expected public node at m/44'/60'/0'/0")
	}
	return &Deriver{key: k}, nil
}
func (d *Deriver) Address(index uint32) (string, error) {
	if index >= hdkeychain.HardenedKeyStart {
		return "", errors.New("index out of public range")
	}
	child, e := d.key.Derive(index)
	if e != nil {
		return "", e
	}
	pub, e := child.ECPubKey()
	if e != nil {
		return "", e
	}
	uncompressed := pub.SerializeUncompressed()
	h := sha3.NewLegacyKeccak256()
	_, _ = h.Write(uncompressed[1:])
	sum := h.Sum(nil)
	raw := hex.EncodeToString(sum[len(sum)-20:])
	return checksum(raw), nil
}
func checksum(lower string) string {
	h := sha3.NewLegacyKeccak256()
	_, _ = h.Write([]byte(lower))
	sum := h.Sum(nil)
	out := []byte(lower)
	for i := range out {
		if out[i] >= 'a' && out[i] <= 'f' {
			n := sum[i/2]
			if i%2 == 0 {
				n >>= 4
			} else {
				n &= 15
			}
			if n >= 8 {
				out[i] -= 32
			}
		}
	}
	return "0x" + string(out)
}
