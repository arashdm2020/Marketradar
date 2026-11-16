# Tradebot

## پنل Next.js و تنظیمات پیشرفته

لانچر ویندوز سرویس Go و پنل Next.js را با هم می‌سازد و اجرا می‌کند. پنل جدید مدیریت روی `http://127.0.0.1:3000/` است؛ ورود ادمین از `http://127.0.0.1:8088/` انجام می‌شود و کوکی احرازشدهٔ Go برای API پنل استفاده می‌شود. فقط شناسهٔ عددی ادمین اجازهٔ تغییر دارد. `AI_CONFIG_MASTER_KEY` باید ۳۲ بایت تصادفی به‌شکل ۶۴ نویسهٔ hex باشد؛ کلیدهای ارائه‌دهنده در SQLite با AES-GCM رمز می‌شوند و از API فقط وضعیت «کلید ثبت شده» برمی‌گردد. برای هر ارائه‌دهنده، نسخهٔ پیش‌نویس، تست محدود (حداکثر ۳ درخواست در ساعت)، انتشار و بازگشت به نسخهٔ پیشین وجود دارد. مقایسهٔ تنظیم کندل روی همان UUID با حداکثر یک اجرای آزمایشی در ساعت انجام می‌شود و وارد گزارش کاربر نمی‌شود. حد مجاز موتور ۲۵۰ تا ۱۰۰۰، مدل ۵۰ تا ۵۰۰ و افق ۱ تا ۹۶ کندل است؛ تعداد مدل نباید از موتور بیشتر باشد. پیش‌فرض ۵۰۰/۵۰۰/۱۲ است. اگر اسنپ‌شات ذخیره‌شده تعداد کندل موتور نسخهٔ جدید را نداشته باشد، مقایسهٔ همان اسنپ‌شات رد می‌شود.

جزئیات پالایش‌شدهٔ تحلیل تنها تا ۳۰ روز پس از ایجاد UUID از API مدیریت قابل خواندن است؛ نمونهٔ پاسخ تست AI نیز پس از ۳۰ روز حذف می‌شود. هزینهٔ تخمینی تا تنظیم جدول قیمت ارائه‌دهندگان «در دسترس نیست» است. تلاش‌های واقعی HTTP مدل در هر دو کانال وب و تلگرام مستقل از تعداد تحلیل‌ها ثبت می‌شوند؛ گزارش‌های قدیمی که پیش از این تغییر اجرا شده‌اند، رکورد تلاش API ندارند.

صف خزانه برای واریزهای تأییدشده رکورد یکتای هش پرداخت می‌سازد و، هنگام فعال‌سازی صریح هر دو تنظیم `TREASURY_MAINNET_ENABLED=true` و `TREASURY_SWEEP_ENABLED=true`، موجودی کیف‌پول‌های تلگرامِ قابل‌امضا را هر ۳۰ ثانیه بررسی می‌کند. موجودی بیشتر از هزینهٔ انتقال به مقصد ثابت خزانه صف‌بندی می‌شود و کار حل‌نشده مانع انتقال تکراری است. تغییر آدرس مادر به کد تازهٔ `/admin` نیاز دارد، نسخه و ممیزی ثبت می‌کند و کارهای صف را برای بازبینی نگه می‌دارد. منطق امضا/nonce، کسر کارمزد، وضعیت پخش نامشخص و بازیابی RPC تست می‌شود.

## اتصال حساب وب و تلگرام

`WEB_PUBLIC_BASE_URL` باید آدرس عمومی **HTTPS** پنل وب باشد (بدون `/app/` در انتها)، برای نمونه `https://radar.example.com`. برنامه همچنان روی loopback گوش می‌دهد؛ برای دسترسی بیرونی یک reverse proxy با TLS و محدودسازی مناسب لازم است. تا وقتی این متغیر تنظیم نشده، ربات پیوند اتصال یا خرید ناامن نمی‌سازد.

پس از مصرف سهمیهٔ رایگان، کاربر در ربات «اتصال حساب وب» را می‌زند. پیوند ۱۰ دقیقه اعتبار دارد و یک‌بارمصرف است. کاربر پس از ورود یا ثبت‌نام در وب باید اتصال همان حساب را صریحاً تأیید کند. ربات با «بررسی اشتراک» اعتبار حساب متصل را از دیتابیس مشترک می‌خواند. ترتیب مجوز هر تحلیل: معافیت ادمین، اشتراک فعال وب، سپس سهمیهٔ روز تقویمی تهران. انقضای اشتراک دسترسی پولی ربات را هم پایان می‌دهد.

صدور فاکتور عمومی بسته است. حتی با RPC و xpub، تنها ایمیل آزمایشی `WEB_BSC_TEST_EMAIL` با `WEB_BSC_TEST_INVOICES_ENABLED=true` می‌تواند فاکتور بگیرد؛ قیمت پلن هم باید در پنل مدیریت تعیین شده باشد. اگر این پیش‌نیازها نباشند، وب وضعیت غیرفعال را نشان می‌دهد. دکمه‌های تلگرام از ایموجی استاندارد Unicode استفاده می‌کنند و به شناسهٔ Custom Emoji وابسته نیستند. Telegram Stars همچنان غیرفعال است.

Windows launcher: double-click `Start-MarketRadar.cmd`. It checks Telegram identity and webhook compatibility, builds the Go executable, and runs the bot, shared analysis engine, alert scheduler, and loopback web panel in one process. Keep the console open; Ctrl+C stops it. A named mutex prevents duplicate launches through this script. `Stop-MarketRadar.cmd` stops the executable belonging to this project, including a background test instance. Shutdown via the stop script is forced; do not use it during an analysis unless you intend to cancel it. When Telegram is unavailable, `go run . web` starts the independent browser panel without the bot.

NVIDIA inference defaults: `NVIDIA_REASONING_EFFORT=medium`, `NVIDIA_REASONING_BUDGET=1024`, `NVIDIA_MAX_TOKENS=4096`, `NVIDIA_TEMPERATURE=1.0`; streaming is disabled. The default input sends all 500 compact OHLC rows and the deterministic summary. Version 2 sends the same 500-candle quantitative summary and the last 50 OHLC rows. Safe stderr telemetry records tokens, finish reason, content length, latency, and input mode. Failed inference or rejected top-level analysis returns `ANALYSIS_UNAVAILABLE`, not a market WAIT. No automatic malformed-response retry is performed.

Opt-in live verification: set `RUN_LIVE=1` then run `go test -run TestLiveAnalysis -v .`. It makes one paid provider analysis, stores a synthetic local test profile in `data/live_test.db`, and writes `data/live_analysis.json` plus `data/live_report.html`. It does not send Telegram messages or test onboarding. Normal tests require no credentials.

## Telegram extension

Set `TELEGRAM_BOT_TOKEN` in `.env`, then run `go run . bot`. The bot uses Telegram long polling and stores user profiles and analysis history in `data/tradebot.db` (SQLite WAL). `/start` begins one-time onboarding; `/analyze` selects a symbol, native timeframe, and style. `/history`, `/settings`, `/markets`, `/cancel`, and `/delete_data` are available. The bot is restricted to private chats. The CLI also accepts `go run . analyze --symbol XAU/USD --timeframe 4h --style swing`.

Existing Telegram profiles can be enriched from the Bot API without starting another polling instance: `go run . profile-backfill --limit 20` inspects up to 20 existing users and prints counts only. After a verified SQLite backup, `go run . profile-backfill --limit 20 --apply` fills missing first name, last name and username fields. The command makes at most 100 sequential `getChat` requests at 250 ms intervals, does not create users or wallets, and never writes a phone number. Successful checks are marked to avoid repeating the same lookup. Failures remain eligible for a later run. Normal bot messages and callbacks also refresh available sender fields.

The seven configured instruments are EUR/USD, GBP/USD, USD/JPY, USD/CHF, AUD/USD, XAU/USD, and WTI/USD. Availability depends on the Twelve Data subscription; WTI/USD is not assumed to be interchangeable with futures or CFDs. The selected native interval is sent directly to Twelve Data and used for the full 500-candle analysis. Daily and weekly provider timestamp semantics, concurrent request limits, and advanced profile editing still need hardening before production deployment.

Telegram navigation uses native inline keyboards and edits callback messages. Onboarding offers percentage buttons and accepts custom numeric text. A model-proposed conditional setup is shown only when its prices pass the same risk validator and its close trigger matches a calculated support or resistance. Users can enable a close-confirmed alert; one poll per symbol and timeframe checks closed candles and expires alerts after 12 nominal timeframe durations. Alert scheduling is a first implementation: holidays, session closures, provider quotas, and durable notification retry still need work. No Mini App is used.

Local, indicative XAU/USD M5 analysis. It never submits orders. `analyze` requires 500 validated closed candles, calculates indicators in Go, requests a structured NVIDIA opinion, then independently validates the proposal. All prices are indicative because OHLC contains no bid, ask, or spread.

## Setup

Copy `.env.example` to `.env` and set `TWELVEDATA_API_KEY` and `NVIDIA_API_KEY`. `.env` and `data/` are Git ignored. Environment variables override `.env`. `CLI_DEFAULT_SYMBOL` and `CLI_DEFAULT_TIMEFRAME` are defaults for CLI/backtesting only; Telegram and web analyses use the user's selected market and timeframe. The default NVIDIA model is `nvidia/nemotron-3-ultra-550b-a55b`.

```powershell
go run . doctor
go run . inspect
go run . analyze
go run . backtest --samples 20 --horizon 12 --cost-r 0.1
go test ./...
go vet ./...
go build ./...
```

JSON goes to stdout and errors to stderr. Successful analysis and backtest reports are also saved under `data/`. `inspect` prints 500 candles and may be large. `backtest` requests 1,002 candles and makes up to `--samples` NVIDIA calls; provider plan limits and token cost apply. A dataset must contain at least 500 context candles plus the forward horizon. History is not yet cached; each backtest downloads data. The 500-candle window is rebuilt at every historical point, with no future bars in the snapshot. Swings require two confirming bars and record the confirmation timestamp.

## Failure behavior

Market data is rejected for invalid OHLC, duplicate timestamps, insufficient closed candles, unexpected weekday gaps, and staleness. Weekend closure assumes Friday 22:00 through Sunday 22:00 UTC; daylight-saving changes and holidays may require an exchange calendar. Incomplete candles are removed using the configured publication buffer. The AI response is decoded strictly; malformed or truncated responses produce `ANALYSIS_UNAVAILABLE`. Risk validation rejects missing, nonfinite or geometrically invalid prices, low risk/reward, distant entries, implausible ATR stop distance, stale data, and identity mismatch. An accepted BUY/SELL remains a **conditional paper signal**, never an executable order.

Backtest entry and TP/SL touches are candle simulations. If both TP and SL occur in one candle, SL is assumed first. `--cost-r` applies a user supplied round-trip cost in R units; no spread is inferred. Untriggered and unresolved outcomes are distinct. Output metrics are limited to counts, activation, win rate, and average R; results are insufficient for claims of predictive value without a deterministic baseline and larger historical samples.

## Current limitations

Twelve Data may limit history by subscription. Session handling is approximate. The CLI does not yet provide a persistent AI response cache, rate-limited backtest scheduler, full performance metric set, paper trading journal, or deterministic baseline comparison. These are the next development steps before interpreting any performance result.

## Access, subscriptions, and admin panel

The bot reserves one free analysis per user per Tehran calendar day before running the model. A delivered BUY, SELL, or reasoned WAIT consumes it. A technical failure releases it. An uncertain Telegram delivery blocks another free request until it can be reconciled. Administrator exemptions bypass the daily limit; web subscriptions and legacy Telegram payment records do not. A duplicate callback for the same in-flight user is ignored.

The internal Go panel/API is available at `http://127.0.0.1:8088` while `bot` runs. Set `ADMIN_LISTEN_ADDR=disabled` to disable it; only loopback addresses are accepted. `ADMIN_PUBLIC_URL` controls the browser-facing Next.js URL included in `/admin` replies (`http://127.0.0.1:3000/login` locally and `https://admin-radar.foxwd.tech/login` in production). The authenticated administrator (Telegram numeric ID `8697362188`) can send `/admin` in a private bot chat to receive a one-time five-minute login code. The panel lists users and their subscription/free quota/exemption state, permits exemption changes, searches analyses by tracking UUID, and switches version 1 (500 engine/500 model) or version 2 (500 engine/50 model). Exemption and configuration changes are audited in SQLite. Telegram Login Widget signatures are also supported when a public domain is configured through BotFather. Never expose the loopback panel directly to the internet.

Every analysis gets a UUID shown as «شماره پیگیری تحلیل» and each model attempt has a separate UUID in `analysis_attempts`. Analysis request status and delivery status are recorded separately. The final report has a «🔄 تحلیل جدید» button and the two-line Persian disclaimer. The button opens symbol selection and the resulting request passes the same quota check.

Telegram Stars checkout is disabled. The bot does not issue invoices or activate access from web purchases. The «buy» callback only explains that sales happen in the separate web panel. Card-to-card is displayed there as disabled and has no payment flow.

## Independent web subscriptions with native BNB

Run `go run . web` for the web panel without starting Telegram. Open `http://127.0.0.1:8088/app/` to register, log in, create a BNB invoice, and request or view a paid analysis. `go run . bot` also serves the web panel in the same process. Both use `SQLITE_PATH`; do not run both on the same listen address. The admin panel is at `/`, with price editing, web users, and payment review. Web accounts are separate from Telegram identities and paid web access never changes bot quota.

Telegram wallet provisioning does not use an xpub: it atomically creates or reuses the user's persisted custodial wallet. Existing addresses are never replaced. The xpub settings below belong only to the separate legacy web-invoice HD-wallet flow and the read-only `bnb-check` verifier. The public invoice endpoint is closed by default. For a single controlled pilot of that legacy web flow, configure all of the following:

```dotenv
WEB_BSC_RPC_URL=https://your-trusted-bsc-rpc.example
WEB_BSC_CHAIN_ID=56
WEB_BSC_CONFIRMATIONS=12
WEB_BSC_XPUB=your-public-extended-key-at-m/44'/60'/0'/0
WEB_BSC_REFERENCE_ADDRESS=the-offline-wallet-address-at-m/44'/60'/0'/0/0
WEB_BSC_TEST_INVOICES_ENABLED=true
WEB_BSC_TEST_EMAIL=pilot-account-email@example.com
```

**Exact key level:** export `WEB_BSC_XPUB` from the external receiving-chain node `m/44'/60'/0'/0` of the offline organizational wallet. The hardened levels `44'`, `60'`, and `0'` have already been derived offline. Do **not** export the account xpub at `m/44'/60'/0'`: the application derives only one relative non-hardened child `/<i>`, not `/0/<i>`. Child `i=0` is `m/44'/60'/0'/0/0`; child `i=1` is `m/44'/60'/0'/0/1`. `WEB_BSC_REFERENCE_ADDRESS` must be the offline wallet's address at **index 0**, `m/44'/60'/0'/0/0`. Verify **both** index 0 and index 1 against the offline wallet before any deposit. BIP-44 defines the external chain as `change=0` and the next level as the address index. The public xpub can derive non-hardened public children; no private key is needed in MarketRadar.

Run this read-only check after setting the xpub and RPC URL (the reference address is optional for the first comparison):

```powershell
go run . bnb-check
```

It prints JSON with the RPC chain ID, expected chain ID, current block height, derived addresses for indexes 0 and 1, and whether index 0 matches the configured reference address. It calls only `eth_chainId` and `eth_blockNumber`; it does not create an invoice, allocate an address, sign, or broadcast a transaction. A network or reference mismatch makes the command fail. It does not print the xpub or RPC URL. After comparing both addresses offline, set `WEB_BSC_REFERENCE_ADDRESS` and run it again.

At service startup, derived address 0 must match the reference address. The first verified xpub fingerprint is pinned in SQLite; a changed wallet root stops startup rather than reusing old customer addresses. Keep the seed phrase and private keys outside this project. SQLite atomically allocates each user one unique child index and reuses that user's address for later invoices. The address only settles invoices; the application has no customer balance or withdrawal API.

Set the one-month plan amount in BNB from the authenticated admin panel. Until a positive price, trusted RPC, xpub, and reference address are configured, invoice creation fails closed. Even after configuration, only the exact registered `WEB_BSC_TEST_EMAIL` can receive an invoice while `WEB_BSC_TEST_INVOICES_ENABLED=true`; all other accounts remain blocked. With the flag false, no new invoices are issued, while the monitor can still settle previously issued ones. An invoice lasts 15 minutes. The scanner checks the configured chain ID, top-level destination, exact native BNB value in wei, empty transaction input, successful receipt, matching block hash, and configured confirmation count. It uses the transaction's block timestamp for the invoice window; a later confirmation of an in-window payment still counts. A transaction hash has a unique database record, and settlement plus one-month extension occur in one SQLite transaction. Wrong amounts, out-of-window payments, failed transactions, and extra payments are listed for admin review. Only direct native BNB transfers are supported; token transfers and contract-internal transfers are excluded. A separate signer and treasury sweep process remain future work.

Tests use a reference-wallet address vector, a simulated chain, and a mocked JSON-RPC HTTP server. They cover concurrent invoice creation, a payment recorded in an eligible block but confirmed after invoice expiry, RPC outage and recovery without advancing the scan cursor, mismatched and late payments, duplicate transaction replay, the single-account invoice gate, and web analysis access after settlement. No mainnet RPC, signer, live deposit, or production web deployment has been verified. Before exposing the web panel publicly, add HTTPS at a trusted reverse proxy, account verification, login rate limiting, and operational monitoring.

### Controlled mainnet pilot (manual wallet transfer)

1. Export the **public** xpub at `m/44'/60'/0'/0` from the offline organizational wallet. Compare indexes 0 and 1 from `go run . bnb-check` with the offline wallet. Set the index-0 reference address and rerun the check; require `network_matches=true` and `reference_matches=true` for chain ID 56.
2. Register a dedicated pilot web account at `/app/`. Set its exact email in `WEB_BSC_TEST_EMAIL` and explicitly set `WEB_BSC_TEST_INVOICES_ENABLED=true`. Set the one-month pilot price from the authenticated admin panel. Keep the web listener on loopback; use the same machine's browser for this pilot.
3. Start `go run . web` (or the combined Windows launcher when Telegram is healthy). In the pilot account, create **one** invoice. Check the displayed BNB Smart Chain network, exact native BNB amount, receiving address, invoice UUID, and UTC expiration. Confirm that a different web account cannot create an invoice.
4. **You** make the direct native BNB transfer from the wallet you control, to that exact invoice address and for the exact amount, while the invoice is valid. Save the transaction hash. The app never initiates a transfer. The transaction must enter a block before the 15-minute deadline; its confirmations may arrive later.
5. Wait for the configured confirmations and refresh the invoice page. Expect `paid`, an active one-month web subscription, and one `paid` payment event. Request a web analysis and check that the report displays its tracking UUID. Reprocessing the same transaction hash must leave the subscription end unchanged. If the RPC is briefly unavailable, the scanner keeps its cursor and resumes after reconnection; avoid making an additional transfer to test recovery.
6. Set `WEB_BSC_TEST_INVOICES_ENABLED=false` and restart the service after the pilot. Existing invoices remain monitorable. Review any mismatched or late transfer in the admin panel; do not manually infer a credit from wallet balance alone.

To compare model input modes on the same real 500-candle snapshot, set `RUN_COMPARE=1` and run `go test -count=1 -run TestLiveInputComparison -v .`. It makes two provider calls and saves `data/live_input_comparison.json`. The test fails if either inference call fails. The default version remains 500/500 until an administrator explicitly switches it. Normal tests do not call providers.
