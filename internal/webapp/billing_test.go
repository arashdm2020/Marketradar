package webapp

import (
	"context"
	"crypto/sha512"
	"errors"
	"fmt"
	"github.com/btcsuite/btcd/btcutil/hdkeychain"
	"github.com/btcsuite/btcd/chaincfg"
	"golang.org/x/crypto/pbkdf2"
	"math/big"
	"strings"
	"sync"
	"testing"
	"time"
	"tradebot/internal/bnb"
	"tradebot/internal/domain"
	"tradebot/internal/engine"
	"tradebot/internal/profile"
)

type fakeChain struct {
	head     uint64
	blocks   map[uint64]bnb.Block
	receipts map[string]bnb.Receipt
	chainID  uint64
	down     bool
}

func (c *fakeChain) ChainID(context.Context) (uint64, error) {
	if c.down {
		return 0, errors.New("RPC offline")
	}
	return c.chainID, nil
}
func (c *fakeChain) Head(context.Context) (uint64, error) {
	if c.down {
		return 0, errors.New("RPC offline")
	}
	return c.head, nil
}
func (c *fakeChain) Block(_ context.Context, n uint64) (bnb.Block, error) {
	if c.down {
		return bnb.Block{}, errors.New("RPC offline")
	}
	v, ok := c.blocks[n]
	if !ok {
		return bnb.Block{}, fmt.Errorf("missing block %d", n)
	}
	return v, nil
}
func (c *fakeChain) Receipt(_ context.Context, h string) (bnb.Receipt, error) {
	if c.down {
		return bnb.Receipt{}, errors.New("RPC offline")
	}
	v, ok := c.receipts[h]
	if !ok {
		return bnb.Receipt{}, fmt.Errorf("missing receipt")
	}
	return v, nil
}
func (c *fakeChain) Balance(context.Context, string) (*big.Int, error) {
	if c.down {
		return nil, errors.New("RPC offline")
	}
	return big.NewInt(0), nil
}
func (c *fakeChain) PendingNonce(context.Context, string) (uint64, error) {
	if c.down {
		return 0, errors.New("RPC offline")
	}
	return 0, nil
}
func (c *fakeChain) GasPrice(context.Context) (*big.Int, error) {
	if c.down {
		return nil, errors.New("RPC offline")
	}
	return big.NewInt(0), nil
}
func (c *fakeChain) TxStatus(context.Context, string) (bool, bool, bool, error) {
	if c.down {
		return false, false, false, errors.New("RPC offline")
	}
	return true, true, true, nil
}
func (c *fakeChain) SendRaw(context.Context, string) error {
	if c.down {
		return errors.New("RPC offline")
	}
	return nil
}
func (c *fakeChain) BlockByHash(context.Context, string) (bnb.Block, error) {
	if c.down {
		return bnb.Block{}, errors.New("RPC offline")
	}
	return bnb.Block{}, nil
}
func testDeriver(t *testing.T) *bnb.Deriver {
	t.Helper()
	seed := pbkdf2.Key([]byte("test test test test test test test test test test test junk"), []byte("mnemonic"), 2048, 64, sha512.New)
	key, e := hdkeychain.NewMaster(seed, &chaincfg.MainNetParams)
	if e != nil {
		t.Fatal(e)
	}
	for _, n := range []uint32{44 + hdkeychain.HardenedKeyStart, 60 + hdkeychain.HardenedKeyStart, hdkeychain.HardenedKeyStart, 0} {
		key, e = key.Derive(n)
		if e != nil {
			t.Fatal(e)
		}
	}
	pub, e := key.Neuter()
	if e != nil {
		t.Fatal(e)
	}
	d, e := bnb.NewDeriver(pub.String())
	if e != nil {
		t.Fatal(e)
	}
	return d
}
func testStores(t *testing.T) (*profile.Store, *Store) {
	t.Helper()
	p, e := profile.Open(t.TempDir() + "/billing.db")
	if e != nil {
		t.Fatal(e)
	}
	w, e := NewStore(p.DB)
	if e != nil {
		t.Fatal(e)
	}
	return p, w
}
func addPayment(c *fakeChain, n uint64, at time.Time, to, amount string, txID int) {
	hash := fmt.Sprintf("0x%064x", txID)
	blockHash := fmt.Sprintf("0x%064x", n)
	c.blocks[n] = bnb.Block{Number: n, Hash: blockHash, Time: at, Transactions: []bnb.Transaction{{Hash: hash, To: to, Value: amount, Input: "0x", BlockNumber: n, BlockHash: blockHash}}}
	c.receipts[hash] = bnb.Receipt{Status: 1, BlockNumber: n, BlockHash: blockHash}
}

func TestTelegramInvoiceSettlementIsIdempotent(t *testing.T) {
	p, s := testStores(t)
	defer p.DB.Close()
	_, e := p.Ensure(9001)
	if e != nil {
		t.Fatal(e)
	}
	address, e := p.EnsureCustodialWallet(9001)
	if e != nil {
		t.Fatal(e)
	}
	now := time.Now().UTC().Truncate(time.Second)
	invoice, e := p.CreateTelegramInvoice(9001, address, "1000000000000000", 56, now.Add(15*time.Minute))
	if e != nil {
		t.Fatal(e)
	}
	_, _ = p.DB.Exec(`INSERT INTO treasury_destinations(address,active,admin_id,at_utc) VALUES(?,1,?,?)`, "0x0f65096f6c02aa6dc0d0b8c99ddebbc3cd90b3f5", profile.AdminID, now.Format(time.RFC3339))
	chain := &fakeChain{head: 112, chainID: 56, blocks: map[uint64]bnb.Block{}, receipts: map[string]bnb.Receipt{}}
	addPayment(chain, 101, now.Add(time.Minute), address, "0x38d7ea4c68000", 77)
	_, _ = p.DB.Exec(`INSERT OR REPLACE INTO web_chain_scan(id,cursor) VALUES(1,100)`)
	b := Billing{Store: s, Chain: chain, ExpectedChainID: 56, Confirmations: 12, Deriver: testDeriver(t), ReferenceVerified: true}
	if _, e = b.ScanOnce(context.Background()); e != nil {
		t.Fatal(e)
	}
	paid, e := p.TelegramInvoice(9001, invoice.ID)
	if e != nil || paid.Status != "paid" {
		t.Fatalf("invoice=%+v err=%v", paid, e)
	}
	var first string
	if e = p.DB.QueryRow(`SELECT subscription_until FROM entitlements WHERE user_id=9001`).Scan(&first); e != nil {
		t.Fatal(e)
	}
	_, _ = p.DB.Exec(`UPDATE web_chain_scan SET cursor=100 WHERE id=1`)
	if _, e = b.ScanOnce(context.Background()); e != nil {
		t.Fatal(e)
	}
	var second string
	_ = p.DB.QueryRow(`SELECT subscription_until FROM entitlements WHERE user_id=9001`).Scan(&second)
	if first != second {
		t.Fatal("duplicate transaction extended subscription twice")
	}
}

type fakeAnalyzer struct{}

func (fakeAnalyzer) Analyze(_ context.Context, r engine.Request) (domain.Decision, error) {
	return domain.Decision{Final: "WAIT", Snapshot: domain.Snapshot{Symbol: r.Symbol, Timeframe: r.Timeframe.Label(), Candles: make([]domain.Candle, 500)}, Proposed: domain.Signal{Signal: "WAIT", Reasoning: []string{"شرط ورود تأیید نشده است"}}}, nil
}

func TestInvoiceSettlementAndWebAnalysis(t *testing.T) {
	p, s := testStores(t)
	defer p.DB.Close()
	user, e := s.Register("user@example.com", "very-long-password-123")
	if e != nil {
		t.Fatal(e)
	}
	if e = s.SetPrice(profile.AdminID, "0.01"); e != nil {
		t.Fatal(e)
	}
	chain := &fakeChain{head: 100, blocks: map[uint64]bnb.Block{}, receipts: map[string]bnb.Receipt{}, chainID: 97}
	billing := Billing{Store: s, Deriver: testDeriver(t), Chain: chain, ExpectedChainID: 97, Confirmations: 12, ReferenceVerified: true}
	billing.TestInvoicesEnabled = true
	billing.TestEmail = "user@example.com"
	now := time.Now().UTC().Truncate(time.Second)
	inv, e := billing.CreateInvoice(context.Background(), user, now)
	if e != nil {
		t.Fatal(e)
	}
	if inv.ChainID != 97 || inv.PriceWei != "10000000000000000" || inv.ExpiresAt.Sub(inv.CreatedAt) != 15*time.Minute {
		t.Fatalf("bad invoice: %+v", inv)
	}
	if _, e = (&AnalysisService{Store: s, Config: p, Engine: fakeAnalyzer{}}).Submit(user, "XAU/USD", "5min", "day"); e == nil {
		t.Fatal("analysis before payment accepted")
	}
	addPayment(chain, 101, now.Add(2*time.Minute), inv.Address, "0x2386f26fc10000", 1)
	chain.head = 112
	if _, e = billing.ScanOnce(context.Background()); e != nil {
		t.Fatal(e)
	}
	paid, e := s.Invoice(user, inv.ID)
	if e != nil || paid.Status != "paid" {
		t.Fatalf("not paid: %+v %v", paid, e)
	}
	until, e := s.Subscription(user)
	if e != nil || !until.After(time.Now()) {
		t.Fatal("subscription not activated")
	}
	_, _ = s.DB.Exec(`UPDATE web_chain_scan SET cursor=100 WHERE id=1`)
	if _, e = billing.ScanOnce(context.Background()); e != nil {
		t.Fatal(e)
	}
	again, _ := s.Subscription(user)
	if !again.Equal(until) {
		t.Fatal("duplicate transaction extended subscription")
	}
	var duplicates int
	if e = s.DB.QueryRow(`SELECT duplicate_count FROM web_payment_events WHERE tx_hash=?`, fmt.Sprintf("0x%064x", 1)).Scan(&duplicates); e != nil || duplicates != 1 {
		t.Fatalf("duplicate review count=%d error=%v", duplicates, e)
	}
	service := AnalysisService{Store: s, Config: p, Engine: fakeAnalyzer{}, Context: context.Background()}
	id, e := service.Submit(user, "XAU/USD", "5min", "day")
	if e != nil {
		t.Fatal(e)
	}
	for i := 0; i < 30; i++ {
		r, e := s.Analysis(user, id)
		if e == nil && r.Status == "completed" {
			if r.EngineCandles != 500 || r.ModelCandles != 500 {
				t.Fatalf("counts: %+v", r)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("web analysis did not complete")
}

func TestConcurrentInvoiceOneAddressAndManualReview(t *testing.T) {
	p, s := testStores(t)
	defer p.DB.Close()
	user, _ := s.Register("other@example.com", "another-long-password")
	_ = s.SetPrice(profile.AdminID, "0.02")
	chain := &fakeChain{head: 200, blocks: map[uint64]bnb.Block{}, receipts: map[string]bnb.Receipt{}, chainID: 56}
	billing := Billing{Store: s, Deriver: testDeriver(t), Chain: chain, ExpectedChainID: 56, Confirmations: 2, ReferenceVerified: true}
	billing.TestInvoicesEnabled = true
	billing.TestEmail = "other@example.com"
	now := time.Now().UTC().Truncate(time.Second)
	var wg sync.WaitGroup
	ids := make(chan string, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v, e := billing.CreateInvoice(context.Background(), user, now)
			if e != nil {
				ids <- "error:" + e.Error()
			} else {
				ids <- v.ID
			}
		}()
	}
	wg.Wait()
	close(ids)
	first := ""
	for id := range ids {
		if strings.HasPrefix(id, "error:") {
			t.Fatal(id)
		}
		if first == "" {
			first = id
		} else if id != first {
			t.Fatal("duplicate invoices")
		}
	}
	var wallets int
	_ = s.DB.QueryRow(`SELECT count(*) FROM web_wallets WHERE user_id=?`, user).Scan(&wallets)
	if wallets != 1 {
		t.Fatalf("wallets=%d", wallets)
	}
	inv, _ := s.Invoice(user, first)
	addPayment(chain, 201, now.Add(time.Minute), inv.Address, "0x1", 2)
	chain.head = 202
	if _, e := billing.ScanOnce(context.Background()); e != nil {
		t.Fatal(e)
	}
	addPayment(chain, 202, now.Add(20*time.Minute), inv.Address, "0x4563918244f40000", 3)
	chain.head = 203
	if _, e := billing.ScanOnce(context.Background()); e != nil {
		t.Fatal(e)
	}
	review, e := s.ReviewPayments()
	if e != nil {
		t.Fatal(e)
	}
	if len(review) != 2 {
		t.Fatalf("review count=%d", len(review))
	}
	classes := map[string]bool{}
	for _, v := range review {
		classes[v.Classification] = true
	}
	if !classes["amount_mismatch"] || !classes["late_or_outside_window"] {
		t.Fatalf("review classes: %+v", classes)
	}
}
func TestConfirmedAfterExpiryUsesBlockTime(t *testing.T) {
	p, s := testStores(t)
	defer p.DB.Close()
	user, _ := s.Register("lateconfirm@example.com", "long-test-password")
	_ = s.SetPrice(profile.AdminID, "0.01")
	chain := &fakeChain{head: 100, chainID: 97, blocks: map[uint64]bnb.Block{}, receipts: map[string]bnb.Receipt{}}
	billing := Billing{Store: s, Deriver: testDeriver(t), Chain: chain, ExpectedChainID: 97, Confirmations: 12, ReferenceVerified: true}
	billing.TestInvoicesEnabled = true
	billing.TestEmail = "lateconfirm@example.com"
	now := time.Now().UTC().Truncate(time.Second)
	first, e := billing.CreateInvoice(context.Background(), user, now)
	if e != nil {
		t.Fatal(e)
	}
	addPayment(chain, 101, now.Add(14*time.Minute), first.Address, "0x2386f26fc10000", 20)
	chain.head = 101
	second, e := billing.CreateInvoice(context.Background(), user, now.Add(16*time.Minute))
	if e != nil {
		t.Fatal(e)
	}
	if second.Address != first.Address || second.ID == first.ID {
		t.Fatal("address changed or invoice reused")
	}
	chain.head = 112
	if _, e = billing.ScanOnce(context.Background()); e != nil {
		t.Fatal(e)
	}
	paid, _ := s.Invoice(user, first.ID)
	if paid.Status != "paid" {
		t.Fatalf("in-window block paid after expiry not accepted: %s", paid.Status)
	}
	pending, _ := s.Invoice(user, second.ID)
	if pending.Status != "pending" {
		t.Fatal("new invoice modified")
	}
}
func TestWrongNetworkPreventsInvoice(t *testing.T) {
	p, s := testStores(t)
	defer p.DB.Close()
	user, _ := s.Register("wrongnet@example.com", "long-test-password")
	_ = s.SetPrice(profile.AdminID, "0.01")
	chain := &fakeChain{head: 100, chainID: 56, blocks: map[uint64]bnb.Block{}, receipts: map[string]bnb.Receipt{}}
	billing := Billing{Store: s, Deriver: testDeriver(t), Chain: chain, ExpectedChainID: 97, Confirmations: 12, ReferenceVerified: true}
	billing.TestInvoicesEnabled = true
	billing.TestEmail = "wrongnet@example.com"
	if _, e := billing.CreateInvoice(context.Background(), user, time.Now()); e == nil {
		t.Fatal("wrong network invoice issued")
	}
}
func TestWalletRootCannotChange(t *testing.T) {
	p, s := testStores(t)
	defer p.DB.Close()
	if e := s.PinWalletRoot("xpub-reference-A"); e != nil {
		t.Fatal(e)
	}
	if e := s.PinWalletRoot("xpub-reference-A"); e != nil {
		t.Fatal(e)
	}
	if e := s.PinWalletRoot("xpub-reference-B"); e == nil {
		t.Fatal("wallet root changed")
	}
}
func TestInvoiceFailsClosedWithoutConfiguredChain(t *testing.T) {
	p, s := testStores(t)
	defer p.DB.Close()
	user, _ := s.Register("disabled@example.com", "long-test-password")
	_ = s.SetPrice(profile.AdminID, "0.01")
	billing := Billing{Store: s, Deriver: testDeriver(t), ExpectedChainID: 56, Confirmations: 12, ReferenceVerified: true}
	billing.TestInvoicesEnabled = true
	billing.TestEmail = "disabled@example.com"
	if _, e := billing.CreateInvoice(context.Background(), user, time.Now()); e == nil {
		t.Fatal("invoice issued without RPC")
	}
}
func TestPublicInvoicingClosed(t *testing.T) {
	p, s := testStores(t)
	defer p.DB.Close()
	allowed, _ := s.Register("pilot@example.com", "long-test-password")
	other, _ := s.Register("otherpilot@example.com", "long-test-password")
	_ = s.SetPrice(profile.AdminID, "0.01")
	chain := &fakeChain{head: 100, chainID: 56, blocks: map[uint64]bnb.Block{}, receipts: map[string]bnb.Receipt{}}
	billing := Billing{Store: s, Deriver: testDeriver(t), Chain: chain, ExpectedChainID: 56, Confirmations: 12, ReferenceVerified: true}
	if _, e := billing.CreateInvoice(context.Background(), allowed, time.Now()); e == nil {
		t.Fatal("default public invoicing open")
	}
	billing.TestInvoicesEnabled = true
	billing.TestEmail = "pilot@example.com"
	if _, e := billing.CreateInvoice(context.Background(), other, time.Now()); e == nil {
		t.Fatal("unlisted account received invoice")
	}
	if _, e := billing.CreateInvoice(context.Background(), allowed, time.Now()); e != nil {
		t.Fatal(e)
	}
}
func TestPaymentRecoveredAfterRPCOutage(t *testing.T) {
	p, s := testStores(t)
	defer p.DB.Close()
	user, _ := s.Register("recovery@example.com", "long-test-password")
	_ = s.SetPrice(profile.AdminID, "0.01")
	chain := &fakeChain{head: 100, chainID: 56, blocks: map[uint64]bnb.Block{}, receipts: map[string]bnb.Receipt{}}
	billing := Billing{Store: s, Deriver: testDeriver(t), Chain: chain, ExpectedChainID: 56, Confirmations: 2, ReferenceVerified: true, TestInvoicesEnabled: true, TestEmail: "recovery@example.com"}
	now := time.Now().UTC().Truncate(time.Second)
	invoice, e := billing.CreateInvoice(context.Background(), user, now)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = billing.ScanOnce(context.Background()); e != nil {
		t.Fatal(e)
	}
	var cursorBefore int64
	if e = s.DB.QueryRow(`SELECT cursor FROM web_chain_scan WHERE id=1`).Scan(&cursorBefore); e != nil {
		t.Fatal(e)
	}
	addPayment(chain, 101, now.Add(time.Minute), invoice.Address, "0x2386f26fc10000", 88)
	chain.head = 102
	chain.down = true
	if _, e = billing.ScanOnce(context.Background()); e == nil {
		t.Fatal("outage not detected")
	}
	var cursorDown int64
	_ = s.DB.QueryRow(`SELECT cursor FROM web_chain_scan WHERE id=1`).Scan(&cursorDown)
	if cursorDown != cursorBefore {
		t.Fatal("cursor advanced during outage")
	}
	chain.down = false
	if _, e = billing.ScanOnce(context.Background()); e != nil {
		t.Fatal(e)
	}
	paid, _ := s.Invoice(user, invoice.ID)
	if paid.Status != "paid" {
		t.Fatalf("payment not recovered: %s", paid.Status)
	}
	until, _ := s.Subscription(user)
	if !until.After(time.Now()) {
		t.Fatal("subscription not active after recovery")
	}
}
