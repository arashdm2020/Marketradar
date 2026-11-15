package treasury

import (
	"context"
	"errors"
	"math/big"
	"strings"
	"testing"
	"time"
	"tradebot/internal/bnb"
	"tradebot/internal/profile"
	"tradebot/internal/webapp"
)

type fakeChain struct {
	down                  bool
	known, final, success bool
	sends                 int
	chain                 uint64
}

func (c *fakeChain) ChainID(context.Context) (uint64, error) {
	if c.down {
		return 0, errors.New("RPC down")
	}
	return c.chain, nil
}
func (c *fakeChain) Balance(context.Context, string) (*big.Int, error) {
	if c.down {
		return nil, errors.New("RPC down")
	}
	return big.NewInt(10000000000000000), nil
}
func (c *fakeChain) PendingNonce(context.Context, string) (uint64, error) {
	if c.down {
		return 0, errors.New("RPC down")
	}
	return 7, nil
}
func (c *fakeChain) GasPrice(context.Context) (*big.Int, error) {
	if c.down {
		return nil, errors.New("RPC down")
	}
	return big.NewInt(1000000000), nil
}
func (c *fakeChain) TxStatus(context.Context, string) (bool, bool, bool, error) {
	if c.down {
		return false, false, false, errors.New("RPC down")
	}
	return c.known, c.final, c.success, nil
}
func (c *fakeChain) SendRaw(context.Context, string) error {
	c.sends++
	c.known = true
	return errors.New("timeout after broadcast")
}

type fakeSigner struct {
	calls int
	last  bnb.SignRequest
}

func (s *fakeSigner) Sign(_ context.Context, r bnb.SignRequest) (bnb.Signed, error) {
	s.calls++
	s.last = r
	return bnb.Signed{Raw: "0x1234", Hash: "0x" + strings.Repeat("a", 64)}, nil
}
func TestDestinationReviewQueueRecoveryAndNoDoubleSweep(t *testing.T) {
	p, e := profile.Open(t.TempDir() + "/treasury.db")
	if e != nil {
		t.Fatal(e)
	}
	defer p.DB.Close()
	if _, e = webapp.NewStore(p.DB); e != nil {
		t.Fatal(e)
	}
	s := &Store{DB: p.DB, Profiles: p}
	code, e := p.IssueAdminCode(profile.AdminID)
	if e != nil {
		t.Fatal(e)
	}
	dest, e := s.SetDestination(profile.AdminID, code, "0x"+strings.Repeat("1", 40))
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.SetDestination(profile.AdminID, code, "0x"+strings.Repeat("2", 40)); e == nil {
		t.Fatal("reused admin code accepted")
	}
	hash := "0x" + strings.Repeat("b", 64)
	source := "0x" + strings.Repeat("c", 40)
	now := time.Now().UTC().Format(time.RFC3339)
	if _, e = p.DB.Exec(`INSERT INTO web_payment_events(tx_hash,address,amount_wei,block_number,block_time,classification,seen_at) VALUES(?,?,?,1,?,'paid',?)`, hash, source, "10000000000000000", now, now); e != nil {
		t.Fatal(e)
	}
	if _, e = p.DB.Exec(`INSERT INTO treasury_jobs(payment_hash,source,amount_wei,destination_version,status,updated_at) VALUES(?,?,?,?,'queued',?)`, hash, source, "10000000000000000", dest.Version, now); e != nil {
		t.Fatal(e)
	}
	if _, e = p.DB.Exec(`INSERT OR IGNORE INTO treasury_jobs(payment_hash,source,amount_wei,destination_version,status,updated_at) VALUES(?,?,?,?,'queued',?)`, hash, source, "10000000000000000", dest.Version, now); e != nil {
		t.Fatal(e)
	}
	var count int
	_ = p.DB.QueryRow(`SELECT count(*) FROM treasury_jobs WHERE payment_hash=?`, hash).Scan(&count)
	if count != 1 {
		t.Fatal("duplicate payment queued")
	}
	code, e = p.IssueAdminCode(profile.AdminID)
	if e != nil {
		t.Fatal(e)
	}
	newDest, e := s.SetDestination(profile.AdminID, code, "0x"+strings.Repeat("2", 40))
	if e != nil {
		t.Fatal(e)
	}
	job, _ := s.Job(hash)
	if job.Status != "needs_review" {
		t.Fatal("old destination job not held")
	}
	if e = s.Review(profile.AdminID, hash); e != nil {
		t.Fatal(e)
	}
	chain := &fakeChain{chain: 97}
	signer := &fakeSigner{}
	worker := &Worker{Store: s, Chain: chain, Signer: signer, ExpectedChainID: 97}
	ctx := context.Background()
	if e = worker.Tick(ctx); e != nil {
		t.Fatal(e)
	}
	job, _ = s.Job(hash)
	if job.Status != "signed" || signer.calls != 1 || signer.last.Destination != newDest.Address || signer.last.Nonce != 7 {
		t.Fatal("signed job invalid")
	}
	if e = worker.Tick(ctx); e == nil {
		t.Fatal("expected send timeout")
	}
	job, _ = s.Job(hash)
	if job.Status != "broadcast_unknown" {
		t.Fatal("timeout state lost")
	}
	chain.down = true
	if e = worker.Tick(ctx); e == nil {
		t.Fatal("RPC outage hidden")
	}
	chain.down = false
	chain.known = true
	if e = worker.Tick(ctx); e != nil {
		t.Fatal(e)
	}
	if signer.calls != 1 || chain.sends != 1 {
		t.Fatal("outage caused duplicate transfer")
	}
	chain.final = true
	chain.success = true
	if e = worker.Tick(ctx); e != nil {
		t.Fatal(e)
	}
	job, _ = s.Job(hash)
	if job.Status != "transferred" {
		t.Fatal("not finalized")
	}
	amounts, e := s.Amounts()
	if e != nil || amounts["transferred"] != "9979000000000000" {
		t.Fatalf("treasury totals: %v %v", amounts, e)
	}
	if signer.last.AmountWei != "9979000000000000" {
		t.Fatalf("fee deduction %s", signer.last.AmountWei)
	}
}

func TestBalanceDiscoveryQueuesEachWalletOnce(t *testing.T) {
	p, e := profile.Open(t.TempDir() + "/discovery.db")
	if e != nil {
		t.Fatal(e)
	}
	defer p.DB.Close()
	if _, e = webapp.NewStore(p.DB); e != nil {
		t.Fatal(e)
	}
	if _, e = p.Ensure(profile.AdminID); e != nil {
		t.Fatal(e)
	}
	if _, e = p.EnsureCustodialWallet(profile.AdminID); e != nil {
		t.Fatal(e)
	}
	store := &Store{DB: p.DB, Profiles: p}
	if e = store.SeedDestination(DefaultDestination); e != nil {
		t.Fatal(e)
	}
	chain := &fakeChain{chain: 56}
	worker := &Worker{Store: store, Chain: chain, Signer: &fakeSigner{}, ExpectedChainID: 56}
	queued, e := worker.DiscoverBalances(context.Background())
	if e != nil || queued != 1 {
		t.Fatalf("first discovery queued=%d err=%v", queued, e)
	}
	queued, e = worker.DiscoverBalances(context.Background())
	if e != nil || queued != 0 {
		t.Fatalf("duplicate discovery queued=%d err=%v", queued, e)
	}
	var count int
	if e = p.DB.QueryRow(`SELECT count(*) FROM treasury_jobs WHERE status='queued'`).Scan(&count); e != nil || count != 1 {
		t.Fatalf("queued jobs=%d err=%v", count, e)
	}
}
