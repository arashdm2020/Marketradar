package bnb

import (
	"context"
	"errors"
	"strings"
)

// CheckConfig makes only read-only RPC calls and derives public addresses.
type CheckResult struct {
	ExpectedChainID     uint64 `json:"expected_chain_id"`
	RPCChainID          uint64 `json:"rpc_chain_id"`
	NetworkMatches      bool   `json:"network_matches"`
	HeadBlock           uint64 `json:"head_block"`
	XPubLevel           string `json:"xpub_level"`
	RelativePath        string `json:"relative_path"`
	Address0            string `json:"address_0"`
	Address1            string `json:"address_1"`
	ReferenceConfigured bool   `json:"reference_configured"`
	ReferenceMatches    bool   `json:"reference_matches"`
	ReadOnly            bool   `json:"read_only"`
}

func Check(ctx context.Context, d *Deriver, chain Chain, expected uint64, reference string) (CheckResult, error) {
	out := CheckResult{ExpectedChainID: expected, XPubLevel: "m/44'/60'/0'/0", RelativePath: "i (0 or 1)", ReferenceConfigured: reference != "", ReadOnly: true}
	if d == nil || chain == nil {
		return out, errors.New("xpub or RPC not configured")
	}
	var e error
	out.Address0, e = d.Address(0)
	if e != nil {
		return out, e
	}
	out.Address1, e = d.Address(1)
	if e != nil {
		return out, e
	}
	out.ReferenceMatches = reference != "" && strings.EqualFold(out.Address0, reference)
	out.RPCChainID, e = chain.ChainID(ctx)
	if e != nil {
		return out, e
	}
	out.NetworkMatches = out.RPCChainID == expected
	out.HeadBlock, e = chain.Head(ctx)
	return out, e
}
