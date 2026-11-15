package config

import "testing"

func TestExplicitCLIDefaultsOverrideLegacyNames(t *testing.T) {
	t.Setenv("CLI_DEFAULT_SYMBOL", "AUD/USD")
	t.Setenv("CLI_DEFAULT_TIMEFRAME", "30min")
	t.Setenv("SYMBOL", "XAU/USD")
	t.Setenv("TIMEFRAME", "5min")

	cfg := Load()
	if cfg.Symbol != "AUD/USD" || cfg.Timeframe != "30min" {
		t.Fatalf("CLI defaults = %s/%s, want AUD/USD/30min", cfg.Symbol, cfg.Timeframe)
	}
}
