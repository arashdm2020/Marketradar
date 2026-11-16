package engine

import (
	"context"
	"strings"
	"testing"

	"tradebot/internal/catalog"
)

func TestEngineRejectsIncompatibleTimeframeHorizonBeforeMarketData(t *testing.T) {
	_, err := (Engine{}).Analyze(context.Background(), Request{Symbol: "XAU/USD", Timeframe: catalog.Timeframe("30min"), Style: "scalp"})
	if err == nil || !strings.Contains(err.Error(), "incompatible") {
		t.Fatalf("got %v", err)
	}
}
