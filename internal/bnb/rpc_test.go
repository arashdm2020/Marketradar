package bnb

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRPCWire(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var q struct {
			Method string `json:"method"`
			Params []any  `json:"params"`
		}
		if e := json.NewDecoder(r.Body).Decode(&q); e != nil {
			t.Error(e)
			return
		}
		var result any
		switch q.Method {
		case "eth_chainId":
			result = "0x61"
		case "eth_blockNumber":
			result = "0x65"
		case "eth_getBlockByNumber":
			result = map[string]any{"number": "0x65", "hash": "0x" + strings.Repeat("a", 64), "timestamp": "0x69200000", "transactions": []map[string]string{{"hash": "0x" + strings.Repeat("b", 64), "to": "0xf39Fd6e51aad88F6F4ce6aB8827279cffFb92266", "value": "0x1", "input": "0x", "blockHash": "0x" + strings.Repeat("a", 64), "blockNumber": "0x65"}}}
		case "eth_getTransactionReceipt":
			result = map[string]string{"status": "0x1", "blockHash": "0x" + strings.Repeat("a", 64), "blockNumber": "0x65"}
		default:
			t.Errorf("unexpected method %s", q.Method)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "result": result})
	}))
	defer srv.Close()
	rpc := RPC{URL: srv.URL}
	ctx := context.Background()
	id, e := rpc.ChainID(ctx)
	if e != nil || id != 97 {
		t.Fatalf("chain id %d %v", id, e)
	}
	head, e := rpc.Head(ctx)
	if e != nil || head != 101 {
		t.Fatalf("head %d %v", head, e)
	}
	b, e := rpc.Block(ctx, 101)
	if e != nil || len(b.Transactions) != 1 {
		t.Fatalf("block %+v %v", b, e)
	}
	receipt, e := rpc.Receipt(ctx, b.Transactions[0].Hash)
	if e != nil || receipt.Status != 1 {
		t.Fatalf("receipt %+v %v", receipt, e)
	}
}
