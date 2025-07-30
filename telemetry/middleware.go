package telemetry

import (
	"log/slog"
	"net/http"
	"time"

	"go.opentelemetry.io/otel/trace"
)

func WithOtelTracerContext(tracer trace.Tracer) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := NewContextWithTracer(r.Context(), tracer)
			r = r.WithContext(ctx)

			next.ServeHTTP(w, r)
		})
	}
}

func WithRequestLog() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := r.Context()
			tracer := MustTracerFromContext(ctx)
			ctx, span := tracer.Start(ctx, "request")
			defer span.End()
			r = r.WithContext(ctx)

			startedAt := time.Now()
			next.ServeHTTP(w, r)

			slog.DebugContext(ctx,
				"request",
				"method", r.Method,
				"path", r.URL.Path,
				"proto", r.Proto,
				"remoteAddr", r.RemoteAddr,
				"latency_us", float64(time.Since(startedAt))/float64(time.Microsecond),
			)
		})
	}
}
