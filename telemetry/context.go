package telemetry

import (
	"context"
	"log/slog"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/trace"
)

type ctxTracerKey struct{}

func TracerFromContext(ctx context.Context) (trace.Tracer, bool) {
	t, ok := ctx.Value(ctxTracerKey{}).(trace.Tracer)
	return t, ok
}

// TracerOrDefault extracts the tracer from context, falling back to the global tracer.
func TracerOrDefault(ctx context.Context) trace.Tracer {
	t, ok := ctx.Value(ctxTracerKey{}).(trace.Tracer)
	if !ok {
		slog.Warn("otel tracer not set in context, using global tracer")
		return otel.Tracer("fallback")
	}
	return t
}

func NewContextWithTracer(parent context.Context, t trace.Tracer) context.Context {
	return context.WithValue(parent, ctxTracerKey{}, t)
}
