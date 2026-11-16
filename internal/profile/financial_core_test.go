package profile

import (
	"sync"
	"testing"
	"time"
)

func TestTelegramInvoiceLifecycleConcurrentAndRestartSafe(t *testing.T) {
	path := t.TempDir() + "/financial.db"
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = s.Ensure(7001)
	address, err := s.EnsureCustodialWallet(7001)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	ids := make(chan string, 12)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v, e := s.CreateTelegramInvoice(7001, address, "100", 56, time.Now().Add(time.Hour), 123)
			if e != nil {
				t.Error(e)
				return
			}
			ids <- v.ID
		}()
	}
	wg.Wait()
	close(ids)
	first := ""
	for id := range ids {
		if first == "" {
			first = id
		} else if first != id {
			t.Fatalf("duplicate invoice %s != %s", first, id)
		}
	}
	var count int
	_ = s.DB.QueryRow(`SELECT count(*) FROM telegram_invoices WHERE telegram_id=7001 AND status='pending'`).Scan(&count)
	if count != 1 {
		t.Fatalf("pending=%d", count)
	}
	s.DB.Close()
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()
	inv, err := s.TelegramInvoice(7001, first)
	if err != nil || inv.StartBlock != 123 {
		t.Fatalf("restart invoice=%+v err=%v", inv, err)
	}
	_, _ = s.DB.Exec(`UPDATE telegram_invoices SET expires_at=? WHERE id=?`, time.Now().Add(-time.Minute).UTC().Format(time.RFC3339), first)
	next, err := s.CreateTelegramInvoice(7001, inv.Address, "100", 56, time.Now().Add(time.Hour), 200)
	if err != nil {
		t.Fatal(err)
	}
	if next.ID == first {
		t.Fatal("expired invoice reused")
	}
}

func TestPaymentNotificationOutboxRetryAndDelivery(t *testing.T) {
	s, err := Open(t.TempDir() + "/outbox.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()
	_, _ = s.Ensure(7002)
	tx, _ := s.DB.Begin()
	now := time.Now().UTC().Truncate(time.Second)
	if err = EnqueuePaymentNotificationTx(tx, 7002, "inv", "0xabc", "payment_received", "10", "standard", "", now); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	items, err := s.ClaimPaymentNotifications(now, 10)
	if err != nil || len(items) != 1 {
		t.Fatalf("claim=%d err=%v", len(items), err)
	}
	if err = s.CompletePaymentNotification(items[0].ID, false, "temporary", now); err != nil {
		t.Fatal(err)
	}
	items, err = s.ClaimPaymentNotifications(now.Add(time.Hour), 10)
	if err != nil || len(items) != 1 {
		t.Fatalf("retry=%d err=%v", len(items), err)
	}
	if err = s.CompletePaymentNotification(items[0].ID, true, "", now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	var status string
	_ = s.DB.QueryRow(`SELECT status FROM payment_notification_outbox WHERE id=?`, items[0].ID).Scan(&status)
	if status != "delivered" {
		t.Fatalf("status=%s", status)
	}
}
