package bnb

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type Transaction struct {
	Hash, To, Value, Input, BlockHash string
	BlockNumber                       uint64
}
type Block struct {
	Number       uint64
	Hash         string
	Time         time.Time
	Transactions []Transaction
}
type Receipt struct {
	Status      uint64
	BlockHash   string
	BlockNumber uint64
}
type Chain interface {
	ChainID(context.Context) (uint64, error)
	Head(context.Context) (uint64, error)
	Balance(context.Context, string) (*big.Int, error)
	PendingNonce(context.Context, string) (uint64, error)
	GasPrice(context.Context) (*big.Int, error)
	Block(context.Context, uint64) (Block, error)
	Receipt(context.Context, string) (Receipt, error)
	TxStatus(context.Context, string) (known, final, success bool, e error)
	SendRaw(context.Context, string) error
	BlockByHash(context.Context, string) (Block, error)
}
type RPC struct {
	URL  string
	HTTP *http.Client
}

func (r RPC) call(ctx context.Context, method string, params any, out any) error {
	if r.URL == "" {
		return errors.New("RPC not configured")
	}
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	req, e := http.NewRequestWithContext(ctx, "POST", r.URL, bytes.NewReader(body))
	if e != nil {
		return errors.New("RPC URL invalid")
	}
	req.Header.Set("Content-Type", "application/json")
	h := r.HTTP
	if h == nil {
		h = &http.Client{Timeout: 12 * time.Second}
	}
	resp, e := h.Do(req)
	if e != nil {
		return errors.New("RPC transport failure")
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("RPC HTTP %d", resp.StatusCode)
	}
	var wire struct {
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if e = json.NewDecoder(io.LimitReader(resp.Body, 32<<20)).Decode(&wire); e != nil {
		return e
	}
	if wire.Error != nil {
		return fmt.Errorf("RPC error %d", wire.Error.Code)
	}
	if len(wire.Result) == 0 || string(wire.Result) == "null" {
		return errors.New("RPC result unavailable")
	}
	return json.Unmarshal(wire.Result, out)
}
func hexUint(s string) (uint64, error) {
	if !strings.HasPrefix(s, "0x") {
		return 0, errors.New("invalid hex quantity")
	}
	return strconv.ParseUint(s[2:], 16, 64)
}
func (r RPC) ChainID(ctx context.Context) (uint64, error) {
	var x string
	e := r.call(ctx, "eth_chainId", []any{}, &x)
	if e != nil {
		return 0, e
	}
	return hexUint(x)
}
func (r RPC) Head(ctx context.Context) (uint64, error) {
	var x string
	e := r.call(ctx, "eth_blockNumber", []any{}, &x)
	if e != nil {
		return 0, e
	}
	return hexUint(x)
}
func (r RPC) Balance(ctx context.Context, address string) (*big.Int, error) {
	if len(address) != 42 {
		return nil, errors.New("invalid address")
	}
	var value string
	if e := r.call(ctx, "eth_getBalance", []any{address, "latest"}, &value); e != nil {
		return nil, e
	}
	n, ok := new(big.Int).SetString(strings.TrimPrefix(value, "0x"), 16)
	if !ok {
		return nil, errors.New("invalid balance")
	}
	return n, nil
}
func (r RPC) Block(ctx context.Context, n uint64) (Block, error) {
	var x struct {
		Number, Hash, Timestamp string
		Transactions            []struct{ Hash, To, Value, Input, BlockHash, BlockNumber string }
	}
	e := r.call(ctx, "eth_getBlockByNumber", []any{fmt.Sprintf("0x%x", n), true}, &x)
	if e != nil {
		return Block{}, e
	}
	number, e := hexUint(x.Number)
	if e != nil || number != n {
		return Block{}, errors.New("RPC block number mismatch")
	}
	stamp, e := hexUint(x.Timestamp)
	if e != nil {
		return Block{}, e
	}
	b := Block{Number: n, Hash: x.Hash, Time: time.Unix(int64(stamp), 0).UTC()}
	if len(b.Hash) != 66 {
		return Block{}, errors.New("invalid block hash")
	}
	for _, t := range x.Transactions {
		bn, e := hexUint(t.BlockNumber)
		if e != nil {
			return Block{}, e
		}
		b.Transactions = append(b.Transactions, Transaction{Hash: t.Hash, To: t.To, Value: t.Value, Input: t.Input, BlockHash: t.BlockHash, BlockNumber: bn})
	}
	return b, nil
}
func (r RPC) Receipt(ctx context.Context, hash string) (Receipt, error) {
	var x struct{ Status, BlockHash, BlockNumber string }
	e := r.call(ctx, "eth_getTransactionReceipt", []any{hash}, &x)
	if e != nil {
		return Receipt{}, e
	}
	status, e := hexUint(x.Status)
	if e != nil {
		return Receipt{}, e
	}
	bn, e := hexUint(x.BlockNumber)
	return Receipt{Status: status, BlockHash: x.BlockHash, BlockNumber: bn}, e
}
func (r RPC) PendingNonce(ctx context.Context, address string) (uint64, error) {
	var x string
	e := r.call(ctx, "eth_getTransactionCount", []any{address, "pending"}, &x)
	if e != nil {
		return 0, e
	}
	return hexUint(x)
}
func (r RPC) GasPrice(ctx context.Context) (*big.Int, error) {
	var x string
	e := r.call(ctx, "eth_gasPrice", []any{}, &x)
	if e != nil {
		return nil, e
	}
	n, ok := new(big.Int).SetString(strings.TrimPrefix(x, "0x"), 16)
	if !ok {
		return nil, errors.New("invalid gas price")
	}
	return n, nil
}
func (r RPC) TxStatus(ctx context.Context, hash string) (known, final, success bool, e error) {
	_, err := r.BlockByHash(ctx, hash)
	if err != nil {
		return false, false, false, nil
	}
	receipt, err := r.Receipt(ctx, hash)
	if err != nil {
		return true, false, false, nil
	}
	return true, true, receipt.Status == 1, nil
}
func (r RPC) SendRaw(ctx context.Context, rawTx string) error {
	var x string
	e := r.call(ctx, "eth_sendRawTransaction", []any{rawTx}, &x)
	if e != nil {
		return e
	}
	if len(x) != 66 || !strings.HasPrefix(x, "0x") {
		return errors.New("invalid transaction hash returned")
	}
	return nil
}
func (r RPC) BlockByHash(ctx context.Context, hash string) (Block, error) {
	var x struct {
		Number       string
		Hash         string
		Timestamp    string
		Transactions []struct{ Hash, To, Value, Input, BlockHash, BlockNumber string }
	}
	e := r.call(ctx, "eth_getBlockByHash", []any{hash, true}, &x)
	if e != nil {
		return Block{}, e
	}
	number, e := hexUint(x.Number)
	if e != nil {
		return Block{}, e
	}
	stamp, e := hexUint(x.Timestamp)
	if e != nil {
		return Block{}, e
	}
	b := Block{Number: number, Hash: x.Hash, Time: time.Unix(int64(stamp), 0).UTC()}
	if len(b.Hash) != 66 {
		return Block{}, errors.New("invalid block hash")
	}
	for _, t := range x.Transactions {
		bn, e := hexUint(t.BlockNumber)
		if e != nil {
			return Block{}, e
		}
		b.Transactions = append(b.Transactions, Transaction{Hash: t.Hash, To: t.To, Value: t.Value, Input: t.Input, BlockHash: t.BlockHash, BlockNumber: bn})
	}
	return b, nil
}
