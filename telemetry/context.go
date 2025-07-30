package telemetry

import (
	"context"

	"go.opentelemetry.io/otel/trace"
)

type ctxTracerKey struct{}

func TracerFromContext(ctx context.Context) (trace.Tracer, bool) {
	t, ok := ctx.Value(ctxTracerKey{}).(trace.Tracer)
	return t, ok
}

func MustTracerFromContext(ctx context.Context) trace.Tracer {
	t, ok := ctx.Value(ctxTracerKey{}).(trace.Tracer)
	if !ok {
		panic("otel tracer not set in context")
	}
	return t
}

func NewContextWithTracer(parent context.Context, t trace.Tracer) context.Context {
	return context.WithValue(parent, ctxTracerKey{}, t)
}
