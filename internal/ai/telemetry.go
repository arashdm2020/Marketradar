package ai

import (
	"context"
	"time"
)

type analysisContextKey struct{}
type analysisContext struct{ ID, Channel string }

func WithAnalysisID(ctx context.Context, id, channel string) context.Context {
	return context.WithValue(ctx, analysisContextKey{}, analysisContext{ID: id, Channel: channel})
}
func AnalysisContext(ctx context.Context) (string, string) {
	v, _ := ctx.Value(analysisContextKey{}).(analysisContext)
	return v.ID, v.Channel
}

type Attempt struct {
	AnalysisID, Channel, Provider, Model, Status, FinishReason string
	FailureClass, BreakerState                                 string
	StartedAt                                                  time.Time
	LatencyMS, TotalLatencyMS                                  int64
	HTTPStatus, PromptTokens, CompletionTokens, AttemptCount   int
}
