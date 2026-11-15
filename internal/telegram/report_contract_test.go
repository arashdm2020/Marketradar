package telegram

import (
	"context"
	"strings"
	"testing"
	"tradebot/internal/catalog"
	"tradebot/internal/domain"
	"tradebot/internal/profile"
)

type captureSender struct {
	messages []string
	buttons  [][][]map[string]any
}

func (c *captureSender) sendText(_ context.Context, _ int64, msg string, buttons [][]map[string]any) (int64, error) {
	c.messages = append(c.messages, msg)
	c.buttons = append(c.buttons, buttons)
	return 1, nil
}
func TestReportTrackingButtonAndDisclaimer(t *testing.T) {
	s, e := profile.Open(t.TempDir() + "/t.db")
	if e != nil {
		t.Fatal(e)
	}
	defer s.DB.Close()
	sender := &captureSender{}
	w := NewAsyncWorker(sender, s, &mockEngine{})
	req := AnalysisRequest{ID: "123e4567-e89b-42d3-a456-426614174000", ChatID: 1, UserID: 1, Symbol: "XAU/USD", Timeframe: catalog.Timeframe("5min"), Style: "scalp"}
	d := domain.Decision{Final: "WAIT"}
	if e = w.deliverFinal(context.Background(), req, 0, d, nil); e != nil {
		t.Fatal(e)
	}
	msg := sender.messages[0]
	if !strings.Contains(msg, "شماره پیگیری تحلیل") || !strings.Contains(msg, req.ID) {
		t.Fatal("missing tracking number")
	}
	if !strings.HasSuffix(msg, reportDisclaimer) {
		t.Fatal("disclaimer not exact final two lines")
	}
	if sender.buttons[0][0][0]["callback_data"] != "analyze" || sender.buttons[0][0][0]["text"] != "🔄 تحلیل جدید" {
		t.Fatal("new analysis button missing")
	}
}
func TestLongReportButtonOnlyOnLastChunk(t *testing.T) {
	s, e := profile.Open(t.TempDir() + "/t.db")
	if e != nil {
		t.Fatal(e)
	}
	defer s.DB.Close()
	sender := &captureSender{}
	w := NewAsyncWorker(sender, s, &mockEngine{})
	d := domain.Decision{Final: "WAIT", Proposed: domain.Signal{Reasoning: []string{strings.Repeat("داده‌های بازار؛ ", 500)}}}
	req := AnalysisRequest{ID: "123e4567-e89b-42d3-a456-426614174000", ChatID: 1, Symbol: "XAU/USD", Timeframe: catalog.Timeframe("5min")}
	if e = w.deliverFinal(context.Background(), req, 0, d, nil); e != nil {
		t.Fatal(e)
	}
	if len(sender.messages) < 2 {
		t.Fatal("long report not split")
	}
	for i, m := range sender.messages {
		if len([]rune(m)) > 3800 {
			t.Fatalf("chunk %d too long", i)
		}
		if i < len(sender.messages)-1 && len(sender.buttons[i]) > 0 {
			t.Fatal("button before final chunk")
		}
	}
	if len(sender.buttons[len(sender.buttons)-1]) == 0 {
		t.Fatal("final button missing")
	}
}
