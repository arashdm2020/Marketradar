package telegram

import (
	"context"
	"database/sql"
	"net/http"
	"strings"
	"testing"
	"time"
	"tradebot/internal/profile"
)

func TestPaymentNotificationDeliveryTracked(t *testing.T) {
	s, err := profile.Open(t.TempDir() + "/notify.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()
	_, _ = s.Ensure(8080)
	tx, err := s.DB.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err = profile.EnqueuePaymentNotificationTx(tx, 8080, "invoice-1", "0x123", "payment_received", "100", "standard", "", time.Now().Add(-time.Second)); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	transport := &captureTransport{}
	b := Bot{Store: s, Token: "test", HTTP: &http.Client{Transport: transport}}
	b.processPaymentNotifications(context.Background())
	var status string
	if err = s.DB.QueryRow(`SELECT status FROM payment_notification_outbox WHERE invoice_id='invoice-1'`).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "delivered" {
		t.Fatalf("status=%s", status)
	}
	if len(transport.bodies) != 1 || !strings.Contains(transport.bodies[0], "invoice-1") {
		t.Fatal("receipt not sent")
	}
	var n int
	err = s.DB.QueryRow(`SELECT count(*) FROM payment_notification_outbox WHERE status='delivered'`).Scan(&n)
	if err != nil && err != sql.ErrNoRows {
		t.Fatal(err)
	}
	b.processPaymentNotifications(context.Background())
	if len(transport.bodies) != 1 {
		t.Fatal("delivered receipt repeated")
	}
}
