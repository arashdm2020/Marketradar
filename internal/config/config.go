package config

import (
	"bufio"
	"os"
	"strconv"
	"strings"
)

type Config struct {
	TwelveKey, NVIDIAKey, Model, BaseURL, Symbol, Timeframe string
	Count, Buffer, Horizon                                  int
	MinRR                                                   float64
}

func Load() Config {
	if f, e := os.Open(".env"); e == nil {
		defer f.Close()
		s := bufio.NewScanner(f)
		for s.Scan() {
			line := strings.TrimSpace(s.Text())
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			p := strings.SplitN(line, "=", 2)
			if len(p) == 2 && os.Getenv(strings.TrimSpace(p[0])) == "" {
				os.Setenv(strings.TrimSpace(p[0]), strings.Trim(strings.TrimSpace(p[1]), "\"'"))
			}
		}
	}
	integer := func(k string, d int) int {
		v, e := strconv.Atoi(os.Getenv(k))
		if e != nil || v <= 0 {
			return d
		}
		return v
	}
	decimal := func(k string, d float64) float64 {
		v, e := strconv.ParseFloat(os.Getenv(k), 64)
		if e != nil || v <= 0 {
			return d
		}
		return v
	}
	value := func(k, d string) string {
		if v := os.Getenv(k); v != "" {
			return v
		}
		return d
	}
	defaultSymbol := value("CLI_DEFAULT_SYMBOL", value("SYMBOL", "XAU/USD"))
	defaultTimeframe := value("CLI_DEFAULT_TIMEFRAME", value("TIMEFRAME", "5min"))
	return Config{os.Getenv("TWELVEDATA_API_KEY"), os.Getenv("NVIDIA_API_KEY"), value("NVIDIA_MODEL", "nvidia/nemotron-3-ultra-550b-a55b"), value("NVIDIA_BASE_URL", "https://integrate.api.nvidia.com/v1"), defaultSymbol, defaultTimeframe, integer("CANDLE_COUNT", 500), integer("CANDLE_CLOSE_BUFFER_SECONDS", 30), integer("SIGNAL_HORIZON_CANDLES", 12), decimal("MIN_RISK_REWARD", 1.5)}
}
