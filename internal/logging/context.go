package logging

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
)

type ctxKey struct{}

// WithLogger returns a child context carrying the given logger.
func WithLogger(ctx context.Context, logger *slog.Logger) context.Context {
	return context.WithValue(ctx, ctxKey{}, logger)
}

// FromContext returns the logger stored in ctx, or slog.Default() if none.
func FromContext(ctx context.Context) *slog.Logger {
	if l, ok := ctx.Value(ctxKey{}).(*slog.Logger); ok && l != nil {
		return l
	}
	return slog.Default()
}

// TraceContext carries request-scoped trace identifiers through context.
type TraceContext struct {
	SessionID string // = convID
	TurnID    string // generated per agent loop iteration
}

type traceKey struct{}

// WithTrace returns a child context carrying the given TraceContext.
func WithTrace(ctx context.Context, tc *TraceContext) context.Context {
	return context.WithValue(ctx, traceKey{}, tc)
}

// TraceFrom returns the TraceContext stored in ctx, or nil if none.
func TraceFrom(ctx context.Context) *TraceContext {
	if tc, ok := ctx.Value(traceKey{}).(*TraceContext); ok {
		return tc
	}
	return nil
}

// NewTurnID generates a short random turn identifier (8 hex chars).
func NewTurnID() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
