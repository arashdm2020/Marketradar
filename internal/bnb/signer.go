package bnb

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"strings"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/rlp"

	"tradebot/internal/custody"
)

// CustodialSigner retrieves encrypted private keys from the custody store
// and signs EIP-155 BSC transactions using go-ethereum.
type CustodialSigner struct {
	store *custody.Store
}

func NewCustodialSigner(store *custody.Store) *CustodialSigner {
	return &CustodialSigner{store: store}
}

func (s *CustodialSigner) Sign(ctx context.Context, req SignRequest) (Signed, error) {
	privKeyHex, err := s.store.RetrieveKey(req.Source)
	if err != nil {
		return Signed{}, err
	}
	ecdsaKey, err := crypto.HexToECDSA(privKeyHex)
	if err != nil {
		return Signed{}, errors.New("invalid private key")
	}

	source := crypto.PubkeyToAddress(ecdsaKey.PublicKey)
	if !common.IsHexAddress(req.Source) || !strings.EqualFold(source.Hex(), req.Source) {
		return Signed{}, errors.New("private key does not match source wallet")
	}
	if req.ChainID != 56 {
		return Signed{}, errors.New("custodial signer requires BSC mainnet chain ID 56")
	}
	if !strings.EqualFold(req.Destination, "0x0F65096f6C02aA6DC0d0b8c99dDeBbc3CD90B3F5") {
		return Signed{}, errors.New("treasury destination not allowed")
	}
	amount, gasPrice := parseBigInt(req.AmountWei), parseBigInt(req.GasPriceWei)
	if amount == nil || gasPrice == nil || amount.Sign() <= 0 || gasPrice.Sign() <= 0 {
		return Signed{}, errors.New("invalid transaction amount or gas price")
	}

	chainID := big.NewInt(int64(req.ChainID))
	signer := types.LatestSignerForChainID(chainID)

	tx := types.NewTransaction(
		req.Nonce,
		common.HexToAddress(req.Destination),
		amount,
		req.GasLimit,
		gasPrice,
		nil,
	)

	signedTx, err := types.SignTx(tx, signer, ecdsaKey)
	if err != nil {
		return Signed{}, fmt.Errorf("sign tx: %w", err)
	}

	// Verify recovered sender matches source address
	from, err := types.Sender(signer, signedTx)
	if err != nil {
		return Signed{}, fmt.Errorf("recover sender: %w", err)
	}
	if from.Hex() != source.Hex() {
		return Signed{}, errors.New("signer address mismatch")
	}

	raw, err := rlp.EncodeToBytes(signedTx)
	if err != nil {
		return Signed{}, fmt.Errorf("rlp encode: %w", err)
	}

	return Signed{
		Raw:  "0x" + hex.EncodeToString(raw),
		Hash: "0x" + signedTx.Hash().Hex(),
	}, nil
}

func parseBigInt(dec string) *big.Int {
	n, ok := new(big.Int).SetString(dec, 10)
	if !ok {
		return nil
	}
	return n
}
