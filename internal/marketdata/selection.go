package marketdata

import (
	"context"
	"fmt"
	"time"

	"tradebot/internal/domain"
)

// SelectClosed validates the whole returned page, excludes every unfinished
// bar, and returns exactly count latest observations without padding.
func SelectClosed(in []domain.Candle, at time.Time, interval, buffer time.Duration, count int) ([]domain.Candle, error) {
	if count < 1 || interval <= 0 {
		return nil, fmt.Errorf("invalid candle selection")
	}
	ordered, err := Normalize(in)
	if err != nil {
		return nil, err
	}
	for _, candle := range ordered {
		if interval < 24*time.Hour {
			if !candle.Time.Equal(candle.Time.Truncate(interval)) {
				return nil, fmt.Errorf("candle timestamp not aligned to timeframe")
			}
		} else if candle.Time.Hour() != 0 || candle.Time.Minute() != 0 || candle.Time.Second() != 0 {
			return nil, fmt.Errorf("daily or weekly candle date not aligned")
		}
	}
	closed := Closed(ordered, at.UTC(), interval, buffer)
	if len(closed) < count {
		return nil, fmt.Errorf("insufficient valid closed candles: got %d, need %d", len(closed), count)
	}
	return closed[len(closed)-count:], nil
}

// FetchClosed makes a second bounded request only when a full first page
// contained too many unconfirmed bars. Twelve Data's outputsize cap is 5000.
func (c Client) FetchClosed(ctx context.Context, symbol, frame string, count int, at time.Time, buffer time.Duration) ([]domain.Candle, error) {
	interval, err := intervalDuration(frame)
	if err != nil {
		return nil, err
	}
	for size := count + 2; size <= 5000; {
		if size > 5000 {
			return nil, fmt.Errorf("requested candle page exceeds provider limit")
		}
		page, err := c.Fetch(ctx, symbol, frame, size)
		if err != nil {
			return nil, err
		}
		selected, selectionErr := SelectClosed(page, at, interval, buffer, count)
		if selectionErr == nil {
			return selected, nil
		}
		if len(page) < size || size == 5000 {
			return nil, selectionErr
		}
		// Validation failures other than missing closed bars must fail closed.
		if len(Closed(page, at, interval, buffer)) >= count {
			return nil, selectionErr
		}
		if size == count+2 {
			size = count + 16
		} else {
			size = min(5000, size*2)
		}
	}
	return nil, fmt.Errorf("insufficient valid closed candles")
}

func intervalDuration(frame string) (time.Duration, error) {
	switch frame {
	case "1min":
		return time.Minute, nil
	case "3min":
		return 3 * time.Minute, nil
	case "5min":
		return 5 * time.Minute, nil
	case "15min":
		return 15 * time.Minute, nil
	case "30min":
		return 30 * time.Minute, nil
	case "1h":
		return time.Hour, nil
	case "4h":
		return 4 * time.Hour, nil
	case "1day":
		return 24 * time.Hour, nil
	case "1week":
		return 7 * 24 * time.Hour, nil
	default:
		return 0, fmt.Errorf("unsupported timeframe %q", frame)
	}
}

// Aggregate3Min builds an M3 bar only from three consecutive, closedable M1
// bars within the same UTC-aligned bucket. Missing minutes remain missing.
func Aggregate3Min(in []domain.Candle) ([]domain.Candle, error) {
	ordered, err := Normalize(in)
	if err != nil {
		return nil, err
	}
	out := make([]domain.Candle, 0, len(ordered)/3)
	for i := 0; i+2 < len(ordered); {
		bucket := ordered[i].Time.Truncate(3 * time.Minute)
		if ordered[i].Time.Equal(bucket) && ordered[i+1].Time.Equal(bucket.Add(time.Minute)) && ordered[i+2].Time.Equal(bucket.Add(2*time.Minute)) {
			a, b, c := ordered[i], ordered[i+1], ordered[i+2]
			out = append(out, domain.Candle{Time: bucket, Open: a.Open, High: max(a.High, b.High, c.High), Low: min(a.Low, b.Low, c.Low), Close: c.Close})
			i += 3
		} else {
			i++
		}
	}
	return out, nil
}
