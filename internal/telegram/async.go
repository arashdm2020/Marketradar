package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"html"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"
	"tradebot/internal/ai"
	"tradebot/internal/catalog"
	"tradebot/internal/domain"
	"tradebot/internal/engine"
	"tradebot/internal/profile"
)

const (
	analysisTimeout = 180 * time.Second
	maxConcurrent   = 1
	maxRunes        = 3800 // leave headroom for HTML entities under Telegram's ~4096 limit
)

// AnalysisRequest is a user-initiated market analysis.
type AnalysisRequest struct {
	ID                  string
	UserID              int64
	ChatID              int64
	MessageID           int64 // original confirmation message; zero for non-callback requests
	Symbol              string
	Timeframe           catalog.Timeframe
	Style               string
	MinProfit           float64
	MaxProfit           float64
	MaxRisk             float64
	ConfigVersion       int
	ModelCandles        int
	EngineCandles       int
	HorizonCandles      int
	MaxDurationSeconds  int
	ResponseTokenBudget int
}

// analysisEngine is the interface AsyncWorker needs to run analyses.
// engine.Engine satisfies this interface.
type analysisEngine interface {
	Analyze(context.Context, engine.Request) (domain.Decision, error)
}

// messageSender is the interface AsyncWorker needs to send Telegram messages.
// *Bot satisfies this interface.
type messageSender interface {
	sendText(context.Context, int64, string, [][]map[string]any) (int64, error)
}

// AsyncWorker processes analysis requests one at a time with per-user deduplication.
// Every enqueued request terminates with exactly one user-visible result.
type AsyncWorker struct {
	bot        messageSender
	store      *profile.Store
	engine     analysisEngine
	ch         chan AnalysisRequest
	inflight   map[int64]string // userID -> requestID
	inflightMu sync.Mutex
	wg         sync.WaitGroup
	stop       chan struct{}
	started    bool
	startMu    sync.Mutex
}

// NewAsyncWorker creates a worker but does not start it. Call Start() before use.
func NewAsyncWorker(b messageSender, s *profile.Store, e analysisEngine) *AsyncWorker {
	return &AsyncWorker{
		bot:      b,
		store:    s,
		engine:   e,
		ch:       make(chan AnalysisRequest, 10),
		inflight: make(map[int64]string),
		stop:     make(chan struct{}),
	}
}

// SetBot sets the message sender reference (needed because Bot and Worker reference each other).
func (w *AsyncWorker) SetBot(b messageSender) {
	w.bot = b
}

// Start launches the worker goroutine.
func (w *AsyncWorker) Start(ctx context.Context) {
	w.startMu.Lock()
	defer w.startMu.Unlock()
	if w.started {
		return
	}
	w.started = true
	w.wg.Add(1)
	go w.run(ctx)
}

// Stop signals the worker to stop and waits for in-flight processing to finish.
func (w *AsyncWorker) Stop() {
	close(w.stop)
	w.wg.Wait()
}

// Enqueue queues an analysis request. Returns false if a request for the same user is already in flight.
func (w *AsyncWorker) Enqueue(ctx context.Context, req AnalysisRequest) (bool, error) {
	w.inflightMu.Lock()
	if _, exists := w.inflight[req.UserID]; exists {
		w.inflightMu.Unlock()
		slog.Info("analysis_deduped", "request_id", req.ID)
		return false, nil
	}
	w.inflight[req.UserID] = req.ID
	w.inflightMu.Unlock()
	if e := w.store.Reserve(req.ID, req.UserID, time.Now()); e != nil {
		w.inflightMu.Lock()
		delete(w.inflight, req.UserID)
		w.inflightMu.Unlock()
		return false, e
	}

	if e := w.store.CreateRequestState(req.ID, strconv.FormatInt(req.UserID, 10), strconv.FormatInt(req.ChatID, 10), req.Symbol, string(req.Timeframe), req.Style); e != nil {
		_ = w.store.SetUse(req.ID, "released")
		w.inflightMu.Lock()
		delete(w.inflight, req.UserID)
		w.inflightMu.Unlock()
		return false, e
	}
	_, _ = w.store.DB.Exec(`UPDATE request_states SET config_version=?,horizon_candles=?,engine_candles=0,model_candles=0 WHERE id=?`, req.ConfigVersion, req.HorizonCandles, req.ID)

	select {
	case w.ch <- req:
		return true, nil
	case <-ctx.Done():
		_ = w.store.SetUse(req.ID, "released")
		w.inflightMu.Lock()
		delete(w.inflight, req.UserID)
		w.inflightMu.Unlock()
		return false, ctx.Err()
	}
}

// IsInflight returns true if a request for the given user is currently being processed or queued.
func (w *AsyncWorker) IsInflight(userID int64) bool {
	w.inflightMu.Lock()
	defer w.inflightMu.Unlock()
	_, ok := w.inflight[userID]
	return ok
}

func (w *AsyncWorker) run(ctx context.Context) {
	defer w.wg.Done()
	for {
		select {
		case <-ctx.Done():
			return
		case <-w.stop:
			return
		case req := <-w.ch:
			w.process(ctx, req)
		}
	}
}

func (w *AsyncWorker) process(ctx context.Context, req AnalysisRequest) {
	start := time.Now()
	defer func() {
		w.inflightMu.Lock()
		delete(w.inflight, req.UserID)
		w.inflightMu.Unlock()
	}()

	log := slog.With("request_id", req.ID, "symbol", req.Symbol, "timeframe", req.Timeframe, "style", req.Style)

	// Stage 1: send "processing" message
	progressCtx := ctx
	if req.MessageID > 0 {
		progressCtx = context.WithValue(ctx, editKey{}, req.MessageID)
	}
	msgID, e := w.bot.sendText(progressCtx, req.ChatID, customEmoji(EmojiMarketAnalysis)+" <b>تحلیل در حال انجام است</b>\n\nداده‌های بازار دریافت و موقعیت‌های معاملاتی ارزیابی می‌شوند…", nil)
	if e != nil {
		log.Warn("processing_message_failed", "error", e.Error())
		_ = w.store.SetUse(req.ID, "released")
		_ = w.store.UpdateRequestState(req.ID, "failed", "", e.Error(), nil)
		return
	}
	log.Info("stage_processing_sent", "message_id", msgID, "duration_ms", time.Since(start).Milliseconds())
	_ = w.store.UpdateRequestState(req.ID, "running", "", "", &msgID)

	// Stage 2: run analysis with bounded context
	attemptID := profile.NewID()
	_, _ = w.store.DB.Exec(`INSERT INTO analysis_attempts(attempt_id,analysis_id,started_at,status) VALUES(?,?,?,'running')`, attemptID, req.ID, time.Now().UTC().Format(time.RFC3339))
	log = log.With("attempt_id", attemptID)
	timeout := analysisTimeout
	if req.MaxDurationSeconds >= 30 && req.MaxDurationSeconds <= 600 {
		timeout = time.Duration(req.MaxDurationSeconds) * time.Second
	}
	analysisCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	analysisCtx = ai.WithAnalysisID(analysisCtx, req.ID, "telegram")

	d, e := w.engine.Analyze(analysisCtx, engine.Request{
		Symbol:              req.Symbol,
		Timeframe:           req.Timeframe,
		Style:               req.Style,
		MinProfit:           req.MinProfit,
		MaxProfit:           req.MaxProfit,
		MaxRisk:             req.MaxRisk,
		Language:            "fa",
		ModelCandles:        req.ModelCandles,
		EngineCandles:       req.EngineCandles,
		HorizonCandles:      req.HorizonCandles,
		ResponseTokenBudget: req.ResponseTokenBudget,
	})
	analysisDuration := time.Since(start)
	engineCount := len(d.Snapshot.Candles)
	modelCount := req.ModelCandles
	if modelCount > engineCount {
		modelCount = engineCount
	}
	if engineCount == 0 {
		modelCount = 0
	}
	_, _ = w.store.DB.Exec(`UPDATE request_states SET engine_candles=?,model_candles=? WHERE id=?`, engineCount, modelCount, req.ID)
	attemptStatus := "completed"
	if e != nil || d.Final == "ANALYSIS_UNAVAILABLE" {
		attemptStatus = "failed"
	}
	_, _ = w.store.DB.Exec(`UPDATE analysis_attempts SET finished_at=?,status=? WHERE attempt_id=?`, time.Now().UTC().Format(time.RFC3339), attemptStatus, attemptID)

	log.Info("analysis_complete", "duration_ms", analysisDuration.Milliseconds(), "final", d.Final, "conditional_count", len(d.ValidatedConditional), "error", e != nil)
	if e == nil && d.Final == "ANALYSIS_UNAVAILABLE" {
		reason := "تحلیل تکمیل نشد"
		if len(d.Reasons) > 0 {
			reason = strings.Join(d.Reasons, "؛ ")
		}
		if d.FailureClass != "" {
			e = &ai.ProviderError{Class: ai.FailureClass(d.FailureClass), Cause: errors.New(reason)}
		} else {
			e = errors.New(reason)
		}
	}
	deliveryCtx, deliveryCancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer deliveryCancel()

	// Stage 3: guaranteed final delivery
	if e != nil {
		_ = w.store.SetUse(req.ID, "released")
		_ = w.store.UpdateRequestState(req.ID, "failed", "", e.Error(), nil)
		if de := w.deliverFinal(deliveryCtx, req, msgID, domain.Decision{}, e); de != nil {
			log.Error("final_delivery_failed", "error", de)
		}
		return
	}

	raw, _ := json.Marshal(d)
	_, _ = w.store.DB.Exec(`UPDATE request_states SET model=?,prompt_version=? WHERE id=?`, d.Model, d.PromptVersion, req.ID)
	_ = w.store.Record(req.UserID, req.Symbol, string(req.Timeframe), req.Style, string(raw))
	_ = w.store.UpdateRequestState(req.ID, "completed", string(raw), "", &msgID)

	if de := w.deliverFinal(deliveryCtx, req, msgID, d, nil); de != nil {
		if IsDefinitiveRejection(de) {
			_ = w.store.SetUse(req.ID, "released")
		} else {
			_ = w.store.SetUse(req.ID, "uncertain")
		}
		log.Error("final_delivery_failed", "error", de)
		_ = w.store.UpdateRequestState(req.ID, "delivery_failed", string(raw), de.Error(), &msgID)
		return
	}
	_ = w.store.SetUse(req.ID, "consumed")
	_ = w.store.UpdateRequestState(req.ID, "delivered", string(raw), "", &msgID)
	log.Info("final_delivery_accepted", "duration_ms", time.Since(start).Milliseconds(), "status", d.Final)
}

func (w *AsyncWorker) deliverFinal(ctx context.Context, req AnalysisRequest, processingMsgID int64, d domain.Decision, err error) error {
	var result string
	if err != nil {
		result = customEmoji(EmojiFailure) + " <b>تحلیل تکمیل نشد</b>\n\nبه دلیل خطای فنی یا دادهٔ بازار، تصمیم معاملاتی تولید نشد. لطفاً بعداً دوباره تلاش کنید."
		class := ai.Classify(err)
		if class == ai.FailureOverloaded || class == ai.FailureRateLimit || class == ai.FailureUpstream || class == ai.FailureCircuit {
			result = customEmoji(EmojiExpiration) + " <b>سرویس تحلیل هوش مصنوعی موقتاً شلوغ است</b>\n\nداده‌های بازار دریافت شد، اما سرویس تحلیل در حال حاضر ظرفیت پاسخ‌گویی ندارد. هیچ تصمیم معاملاتی تولید نشده است؛ لطفاً چند دقیقهٔ دیگر دوباره تلاش کنید."
		} else if errors.Is(err, context.DeadlineExceeded) || class == ai.FailureDeadline {
			result = customEmoji(EmojiExpiration) + " <b>مهلت تحلیل به پایان رسید</b>\n\nاین پیام تصمیم معاملاتی نیست؛ لطفاً بعداً دوباره تلاش کنید."
		}
	} else {
		result = Render(d, req.Symbol, string(req.Timeframe), req.Style, profile.User{Timezone: "Asia/Tehran", MaxRisk: req.MaxRisk})
	}
	result = "<b>شماره پیگیری تحلیل:</b> <code>" + req.ID + "</code>\n" + result
	buttonLabel := "🔄 تحلیل جدید"
	if err != nil {
		buttonLabel = "🔄 تلاش دوباره"
	}
	buttons := [][]map[string]any{{button(buttonLabel, "analyze")}}

	chunks := splitReport(result)
	for i, chunk := range chunks {
		partCtx := ctx
		if i == 0 && processingMsgID > 0 {
			partCtx = context.WithValue(ctx, editKey{}, processingMsgID)
		}
		var row [][]map[string]any
		if i == len(chunks)-1 {
			row = buttons
		}
		_, e := w.bot.sendText(partCtx, req.ChatID, chunk, row)
		if e != nil {
			slog.Warn("html_delivery_failed_fallback", "request_id", req.ID, "chunk", i, "error", e.Error())
			_, e2 := w.bot.sendText(partCtx, req.ChatID, html.EscapeString(stripHTML(chunk)), row)
			if e2 != nil {
				return e2
			}
		}
	}
	return nil
}

func splitReport(s string) []string {
	const limit = 3500
	if visibleRunes(s) <= limit {
		return []string{s}
	}
	var out []string
	var b strings.Builder
	for _, line := range strings.SplitAfter(s, "\n") {
		if visibleRunes(line) > limit {
			if b.Len() > 0 {
				out = append(out, b.String())
				b.Reset()
			}
			plain := []rune(stripHTML(line))
			for len(plain) > 0 {
				n := len(plain)
				if n > 3000 {
					n = 3000
				}
				out = append(out, html.EscapeString(string(plain[:n])))
				plain = plain[n:]
			}
			continue
		}
		if visibleRunes(b.String())+visibleRunes(line) > limit {
			out = append(out, b.String())
			b.Reset()
		}
		b.WriteString(line)
	}
	if b.Len() > 0 {
		out = append(out, b.String())
	}
	return out
}

func stripHTML(s string) string {
	var b strings.Builder
	inTag := false
	for _, r := range s {
		if r == '<' {
			inTag = true
			continue
		}
		if r == '>' {
			inTag = false
			continue
		}
		if !inTag {
			b.WriteRune(r)
		}
	}
	return strings.TrimSpace(b.String())
}
