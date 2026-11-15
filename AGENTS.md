# Tradebot AGENTS.md

Single Go module (`module tradebot`, go 1.23). Entry point: root `main.go`. Commands: `analyze`, `inspect`, `backtest`, `doctor`, `bot`, `web`, `bnb-check`.

## Setup

1. Copy `.env.example` to `.env`. `.env` and `data/` are gitignored; never commit credentials or DB files.
2. Set `TWELVEDATA_API_KEY` and `NVIDIA_API_KEY` in `.env`. Env vars override `.env` (`config.Load()` reads `.env` only when the var is unset).
3. Toolchain: Go 1.23 **plus CGO/gcc** — `mattn/go-sqlite3` is cgo, so `go build`, `go vet`, `go test` fail without a C compiler (mingw/msys2 on Windows).
4. Defaults: symbol `XAU/USD`, timeframe `5min`, model `nvidia/nemotron-3-ultra-550b-a55b`, NVIDIA base `https://integrate.api.nvidia.com/v1`.

## Commands (run from repo root)

- `go run . doctor` — verify provider keys present + Twelve Data reachable; prints JSON to stdout.
- `go run . inspect` — prints 500 candles (may be large).
- `go run . analyze --symbol XAU/USD --timeframe 4h --style swing` — `--symbol`, `--timeframe`, `--style` (default `day`). Requires 500 validated closed candles.
- `go run . backtest --samples 20 --horizon 12 --cost-r 0.1` — `--samples` 1..100, `--horizon` 6 or 12, `--cost-r` round-trip cost in R. Requests 1,002 candles and up to `--samples` NVIDIA calls; history is not cached.
- `go run . bot` — Telegram long polling; needs `TELEGRAM_BOT_TOKEN`; stores profiles/history in `data/tradebot.db` (SQLite WAL, `SQLITE_PATH` override); private chats only.
- `go run . web` — independent loopback web panel at `ADMIN_LISTEN_ADDR` (default `127.0.0.1:8088`, loopback-only enforced); account registration, gated BNB pilot invoices, paid web analysis. Do not run `bot` and `web` on the same listen address.
- `go run . bnb-check` — read-only `eth_chainId`/`eth_blockNumber` + derived BNB addresses for indexes 0 and 1. Requires `WEB_BSC_XPUB` and `WEB_BSC_RPC_URL`; sends no transaction.
- `go test ./...` — normal tests need no credentials.
- `RUN_LIVE=1 go test -run TestLiveAnalysis -v .` — opt-in **paid** provider analysis; writes `data/live_analysis.json` / `data/live_report.html`, uses `data/live_test.db`. Skip unless spending tokens.
- `RUN_COMPARE=1 go test -count=1 -run TestLiveInputComparison -v .` — two paid provider calls; writes `data/live_input_comparison.json`.
- `go vet ./...` → `go build ./...` → `go test ./...` — verification order.

## Packages (internal/)

`ai`, `aimgmt`, `adminweb`, `backtest`, `bnb`, `catalog`, `condition`, `config`, `domain`, `engine`, `indicators`, `marketdata`, `profile`, `risk`, `scenario`, `snapshot`, `structure`, `telegram`, `treasury`, `webapp`. Root package `main` owns CLI wiring in `main.go`; `live_test.go` is the opt-in live test.

`admin-next/` is a separate Next.js panel (not the Go web panel); its `node_modules/` and `.next/` are gitignored. `bin/marketradar.exe` is a prebuilt Windows launcher (binary committed, `*.exe` gitignored).

## Data & outputs

- JSON to stdout; errors to stderr. Successful analysis/backtest reports saved under `data/`.
- Weekend closure assumed Friday 22:00–Sunday 22:00 UTC; DST/holidays may need an exchange calendar.
- Incomplete candles removed via `CANDLE_CLOSE_BUFFER_SECONDS` (default 30s). AI response decoded strictly; malformed/truncated → `ANALYSIS_UNAVAILABLE`, never a WAIT. No automatic malformed-response retry.

## Telemetry / limits

- NVIDIA defaults: reasoning effort `medium`, budget `1024`, max tokens `4096`, temperature `1.0`, streaming disabled. All 500 compact OHLC rows + deterministic summary per request.
- Telegram alerts: one poll per symbol/timeframe, expires after 12 nominal timeframe durations. First implementation — holidays, session closures, quotas, durable retry not hardened.

## Windows launchers

- `Start-MarketRadar.cmd` / `.ps1` — builds the Go exe, runs bot + analysis engine + alert scheduler + loopback panel in one process. Named mutex prevents duplicate launches. Keep console open; Ctrl+C stops it.
- `Stop-MarketRadar.cmd` / `.ps1` — forced shutdown of this project's executable (including background test instance). Do not use during an analysis unless cancelling.
