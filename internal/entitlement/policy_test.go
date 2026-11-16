package entitlement

import (
	"testing"

	"tradebot/internal/catalog"
)

func TestLegacyPublicMapsToStandard(t *testing.T) {
	if got := Normalize("Public"); got != Standard {
		t.Fatalf("legacy Public mapped to %s", got)
	}
}

func TestPremiumEligibilityIsNotPrematurelyEnabled(t *testing.T) {
	p := Defaults[Premium]
	if !p.Features.MultiTimeframeEligible || !p.Features.ExtendedReportEligible {
		t.Fatal("Premium eligibility missing")
	}
	if p.Features.MultiTimeframeEnabled || p.Features.ExtendedReportEnabled {
		t.Fatal("unimplemented Premium feature was enabled")
	}
	if !p.Allows("XAU/USD", catalog.Timeframe("4h"), catalog.HorizonPosition) {
		t.Fatal("valid market combination rejected")
	}
	if p.Allows("XAU/USD", catalog.Timeframe("5min"), catalog.HorizonSwing) {
		t.Fatal("invalid combination allowed")
	}
}
