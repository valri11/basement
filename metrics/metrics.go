package metrics

import (
	"net/http"
	"strconv"
	"time"

	"go.opentelemetry.io/otel/attribute"
	metricsApi "go.opentelemetry.io/otel/metric"
)

type AppMetrics struct {
	ReqCounter    metricsApi.Int64Counter
	ReqDuration   metricsApi.Float64Histogram
	ReqErrCounter metricsApi.Int64Counter
}

func NewAppMetrics(meter metricsApi.Meter) (*AppMetrics, error) {
	reqCounter, err := meter.Int64Counter("req_cnt", metricsApi.WithDescription("request counter"))
	if err != nil {
		return nil, err
	}
	reqDuration, err := meter.Float64Histogram(
		"req_duration",
		metricsApi.WithDescription("Requests handler end to end duration"),
		metricsApi.WithUnit("ms"),
	)
	if err != nil {
		return nil, err
	}
	errCounter, err := meter.Int64Counter("err_cnt", metricsApi.WithDescription("service error counter"))
	if err != nil {
		return nil, err
	}

	m := AppMetrics{
		ReqCounter:    reqCounter,
		ReqDuration:   reqDuration,
		ReqErrCounter: errCounter,
	}
	return &m, nil
}

type CustomResponseWriter struct {
	responseWriter http.ResponseWriter
	StatusCode     int
}

func ExtendResponseWriter(w http.ResponseWriter) *CustomResponseWriter {
	return &CustomResponseWriter{w, 0}
}

func (w *CustomResponseWriter) Write(b []byte) (int, error) {
	return w.responseWriter.Write(b)
}

func (w *CustomResponseWriter) Header() http.Header {
	return w.responseWriter.Header()
}

func (w *CustomResponseWriter) WriteHeader(statusCode int) {
	w.StatusCode = statusCode
	w.responseWriter.WriteHeader(statusCode)
}

// Flush implements http.Flusher for SSE and streaming responses.
func (w *CustomResponseWriter) Flush() {
	if f, ok := w.responseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (w *CustomResponseWriter) Done() {
	// if WriteHeader wasn't called, default to 200 OK
	if w.StatusCode == 0 {
		w.StatusCode = http.StatusOK
	}
}

func WithMetrics(metrics *AppMetrics) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := r.Context()
			requestStartTime := time.Now()

			attrs := metricsApi.WithAttributes(
				attribute.String("method", r.Method),
				attribute.String("path", r.URL.Path),
			)
			metrics.ReqCounter.Add(ctx, 1, attrs)

			ew := ExtendResponseWriter(w)
			next.ServeHTTP(ew, r)
			ew.Done()

			if ew.StatusCode >= http.StatusBadRequest {
				errAttrs := metricsApi.WithAttributes(
					attribute.String("method", r.Method),
					attribute.String("path", r.URL.Path),
					attribute.String("status", strconv.Itoa(ew.StatusCode)),
				)
				metrics.ReqErrCounter.Add(ctx, 1, errAttrs)
			}

			elapsedTime := float64(time.Since(requestStartTime)) / float64(time.Millisecond)
			metrics.ReqDuration.Record(ctx, elapsedTime, attrs)
		})
	}
}
