package entitlement

import (
	"strings"

	"tradebot/internal/catalog"
)

type PlanID string

const (
	Visitor  PlanID = "visitor"
	Standard PlanID = "standard"
	Premium  PlanID = "premium"
)

type Features struct {
	MultiTimeframeEligible bool `json:"multi_timeframe_eligible"`
	MultiTimeframeEnabled  bool `json:"multi_timeframe_enabled"`
	ExtendedReportEligible bool `json:"extended_report_eligible"`
	ExtendedReportEnabled  bool `json:"extended_report_enabled"`
}

type Policy struct {
	ID             PlanID   `json:"id"`
	Label          string   `json:"label"`
	DailyAllowance int      `json:"daily_allowance"`
	Priority       int      `json:"priority"`
	AllMarkets     bool     `json:"all_markets"`
	Features       Features `json:"features"`
}

// Defaults centralizes provisional product limits. Business logic consumes
// these policies and never compares plan names to grant individual features.
var Defaults = map[PlanID]Policy{
	Visitor:  {ID: Visitor, Label: "Visitor", DailyAllowance: 1, Priority: 0, AllMarkets: true},
	Standard: {ID: Standard, Label: "Standard", DailyAllowance: 25, Priority: 1, AllMarkets: true},
	Premium: {
		ID: Premium, Label: "Premium", DailyAllowance: 30, Priority: 2, AllMarkets: true,
		Features: Features{MultiTimeframeEligible: true, ExtendedReportEligible: true},
	},
}

func Normalize(raw string) PlanID {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "premium":
		return Premium
	case "standard", "public":
		return Standard
	default:
		return Visitor
	}
}

func For(raw string) Policy { return Defaults[Normalize(raw)] }

func (p Policy) Allows(symbol string, frame catalog.Timeframe, horizon catalog.Horizon) bool {
	if !p.AllMarkets {
		return false
	}
	inst, ok := catalog.Find(symbol)
	return ok && inst.Enabled && catalog.Compatible(frame, horizon)
}

type Access struct {
	Policy             Policy `json:"policy"`
	SubscriptionActive bool   `json:"subscription_active"`
	SubscriptionUntil  string `json:"subscription_until"`
	Exempt             bool   `json:"exempt"`
	Allowance          int    `json:"daily_allowance"` // -1 means unlimited
	Used               int    `json:"daily_used"`
	Remaining          int    `json:"daily_remaining"` // -1 means unlimited
	LegacyUnlimited    bool   `json:"legacy_unlimited"`
}
