package cmd

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	"github.com/valri11/basement/config"
	"github.com/valri11/go-servicepack/problem"
	spsemconv "github.com/valri11/go-servicepack/semconv"

	"github.com/valri11/basement/internal/semconv"
)

const (
	incomingTraceID = "4bf92f3577b34da6a3ce929d0e0e4736"
	incomingSpanID  = "00f067aa0ba902b7"
)

var (
	setupOnce sync.Once
	spans     *tracetest.SpanRecorder
	reader    *sdkmetric.ManualReader
)

func newTestServer(t *testing.T) (http.Handler, *srvHandler) {
	t.Helper()
	setupOnce.Do(func() {
		spans = tracetest.NewSpanRecorder()
		otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spans)))
		reader = sdkmetric.NewManualReader()
		otel.SetMeterProvider(sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)))
		otel.SetTextMapPropagator(propagation.TraceContext{})
	})
	spans.Reset()

	h, err := newWebSrvHandler(config.Configuration{})
	if err != nil {
		t.Fatal(err)
	}
	return h.routes(), h
}

func get(srv http.Handler, path string, header map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	for k, v := range header {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec
}

func serverSpan(t *testing.T) sdktrace.ReadOnlySpan {
	t.Helper()
	for _, s := range spans.Ended() {
		if s.SpanKind() == trace.SpanKindServer {
			return s
		}
	}
	t.Fatalf("no SERVER span among %d spans", len(spans.Ended()))
	return nil
}

func spanAttr(s sdktrace.ReadOnlySpan, key attribute.Key) attribute.Value {
	for _, kv := range s.Attributes() {
		if kv.Key == key {
			return kv.Value
		}
	}
	return attribute.Value{}
}

func TestIncomingTraceContextIsJoined(t *testing.T) {
	srv, _ := newTestServer(t)

	get(srv, "/demo/trace", map[string]string{
		"traceparent": "00-" + incomingTraceID + "-" + incomingSpanID + "-01",
	})

	ended := spans.Ended()
	if len(ended) != 2 {
		t.Fatalf("spans = %d, want SERVER + demo.work", len(ended))
	}
	server := serverSpan(t)
	if server.Name() != "GET /demo/trace" {
		t.Errorf("server span name = %q", server.Name())
	}
	if server.Parent().SpanID().String() != incomingSpanID {
		t.Errorf("server span parent = %s, want incoming %s", server.Parent().SpanID(), incomingSpanID)
	}
	for _, s := range ended {
		if s.SpanContext().TraceID().String() != incomingTraceID {
			t.Errorf("span %q in trace %s, want %s", s.Name(), s.SpanContext().TraceID(), incomingTraceID)
		}
		if s.Name() == "demo.work" && s.Parent().SpanID() != server.SpanContext().SpanID() {
			t.Errorf("demo.work is not a child of the SERVER span")
		}
	}
}

func TestPanicEndsServerSpanAsError(t *testing.T) {
	srv, _ := newTestServer(t)

	rec := get(srv, "/demo/panic", nil)

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
	s := serverSpan(t)
	if s.Status().Code != codes.Error {
		t.Errorf("span status = %v, want Error", s.Status().Code)
	}
	if got := spanAttr(s, "http.response.status_code").AsInt64(); got != http.StatusInternalServerError {
		t.Errorf("http.response.status_code = %d, want 500", got)
	}
	if got := spanAttr(s, "problem.type").AsString(); got != problem.TypePanic {
		t.Errorf("problem.type = %q", got)
	}
}

func TestValidationErrorLeavesSpanUnset(t *testing.T) {
	srv, _ := newTestServer(t)

	get(srv, "/demo/validation", nil)

	s := serverSpan(t)
	if s.Status().Code != codes.Unset {
		t.Errorf("span status = %v, want Unset for 4xx", s.Status().Code)
	}
	if got := spanAttr(s, "problem.type").AsString(); got != problem.TypeValidationError {
		t.Errorf("problem.type = %q", got)
	}
}

func TestUnmatchedPathsShareOneRoute(t *testing.T) {
	srv, _ := newTestServer(t)

	for _, p := range []string{"/x1", "/x2/y", "/x3"} {
		if rec := get(srv, p, nil); rec.Code != http.StatusNotFound {
			t.Errorf("%s: status = %d, want 404", p, rec.Code)
		}
	}

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(t.Context(), &rm); err != nil {
		t.Fatal(err)
	}
	routes := map[string]bool{}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != spsemconv.ServicepackHTTPServerProblemsName {
				continue
			}
			for _, dp := range m.Data.(metricdata.Sum[int64]).DataPoints {
				if v, _ := dp.Attributes.Value("http.response.status_code"); v.AsInt64() != http.StatusNotFound {
					continue
				}
				v, _ := dp.Attributes.Value("http.route")
				routes[v.AsString()] = true
			}
		}
	}
	if len(routes) != 1 || !routes["/"] {
		t.Errorf("404 routes = %v, want only \"/\"", routes)
	}
}

func TestProbesAreNotTraced(t *testing.T) {
	srv, h := newTestServer(t)

	if rec := get(srv, "/readyz", nil); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("readyz before start = %d, want 503", rec.Code)
	}
	h.ready.Store(true)
	for _, p := range []string{"/livez", "/readyz"} {
		if rec := get(srv, p, nil); rec.Code != http.StatusOK {
			t.Errorf("%s = %d, want 200", p, rec.Code)
		}
	}

	if n := len(spans.Ended()); n != 0 {
		t.Errorf("probes produced %d spans", n)
	}
}

func collectMetrics(t *testing.T) map[string]metricdata.Aggregation {
	t.Helper()
	var rm metricdata.ResourceMetrics
	if err := reader.Collect(t.Context(), &rm); err != nil {
		t.Fatal(err)
	}
	out := map[string]metricdata.Aggregation{}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			out[m.Name] = m.Data
		}
	}
	return out
}

func itemsByResult(t *testing.T) map[string]int64 {
	t.Helper()
	out := map[string]int64{}
	data, ok := collectMetrics(t)[semconv.BasementDemoWorkItemsName]
	if !ok {
		return out
	}
	for _, dp := range data.(metricdata.Sum[int64]).DataPoints {
		v, _ := dp.Attributes.Value(semconv.BasementDemoWorkResultKey)
		out[v.AsString()] = dp.Value
	}
	return out
}

func TestDemoWorkRecordsSpanAndMetrics(t *testing.T) {
	srv, _ := newTestServer(t)
	before := itemsByResult(t)

	if rec := get(srv, "/demo/trace?items=3", nil); rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if rec := get(srv, "/demo/trace?items=2&fail=true", nil); rec.Code != http.StatusInternalServerError {
		t.Fatalf("fail status = %d, want 500", rec.Code)
	}

	after := itemsByResult(t)
	if got := after["ok"] - before["ok"]; got != 3 {
		t.Errorf("ok items = %d, want 3", got)
	}
	if got := after["error"] - before["error"]; got != 2 {
		t.Errorf("error items = %d, want 2", got)
	}
	if _, ok := collectMetrics(t)[semconv.BasementDemoWorkDurationName]; !ok {
		t.Errorf("%s not recorded", semconv.BasementDemoWorkDurationName)
	}

	var work []sdktrace.ReadOnlySpan
	for _, s := range spans.Ended() {
		if s.Name() == "demo.work" {
			work = append(work, s)
		}
	}
	if len(work) != 2 {
		t.Fatalf("demo.work spans = %d, want 2", len(work))
	}
	if got := spanAttr(work[0], semconv.BasementDemoWorkItemCountKey).AsInt64(); got != 3 {
		t.Errorf("item_count = %d, want 3", got)
	}
	if work[1].Status().Code != codes.Error {
		t.Errorf("failed work span status = %v, want Error", work[1].Status().Code)
	}
}

func TestDemoWorkRejectsBadItems(t *testing.T) {
	srv, _ := newTestServer(t)

	for _, q := range []string{"abc", "0", "101"} {
		if rec := get(srv, "/demo/trace?items="+q, nil); rec.Code != http.StatusBadRequest {
			t.Errorf("items=%s: status = %d, want 400", q, rec.Code)
		}
	}
}
