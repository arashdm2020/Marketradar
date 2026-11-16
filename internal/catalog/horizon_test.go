package catalog

import "testing"

func TestTimeframeHorizonMatrix(t *testing.T) {
	cases := []struct {
		frame Timeframe
		h     Horizon
		ok    bool
	}{
		{"1min", HorizonScalp, true}, {"1min", HorizonIntraday, false},
		{"5min", HorizonScalp, true}, {"5min", HorizonIntraday, true}, {"5min", HorizonSwing, false},
		{"15min", HorizonScalp, true}, {"15min", HorizonIntraday, true},
		{"30min", HorizonScalp, false}, {"30min", HorizonIntraday, true},
		{"1h", HorizonIntraday, true}, {"1h", HorizonSwing, true}, {"1h", HorizonPosition, false},
		{"4h", HorizonIntraday, true}, {"4h", HorizonSwing, true}, {"4h", HorizonPosition, true},
		{"1day", HorizonSwing, true}, {"1day", HorizonPosition, true}, {"1day", HorizonIntraday, false},
		{"1week", HorizonPosition, true}, {"1week", HorizonSwing, false},
	}
	for _, tc := range cases {
		if got := Compatible(tc.frame, tc.h); got != tc.ok {
			t.Errorf("Compatible(%s,%s)=%v want %v", tc.frame, tc.h, got, tc.ok)
		}
	}
}
