package condition

import (
	"testing"
	"tradebot/internal/domain"
)

func TestBreakoutNeedsRetest(t *testing.T) {
	stage, hit := Step("BREAK_RETEST_BUY", 100, 100, 0, domain.Candle{Low: 101, High: 103, Close: 102})
	if hit || stage != 1 {
		t.Fatal("breakout alone activated alert")
	}
	stage, hit = Step("BREAK_RETEST_BUY", 100, 100, stage, domain.Candle{Low: 101, High: 103, Close: 102})
	if hit {
		t.Fatal("no retest")
	}
	_, hit = Step("BREAK_RETEST_BUY", 100, 100, stage, domain.Candle{Low: 99.9, High: 102, Close: 101})
	if !hit {
		t.Fatal("confirmed retest missed")
	}
}
