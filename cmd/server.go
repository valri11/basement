/*
Copyright © 2025 Val Gridnev <valer.gr@gmail.com>
*/
package cmd

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/justinas/alice"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"

	"github.com/valri11/basement/config"
	"github.com/valri11/basement/internal/semconv"
	"github.com/valri11/go-servicepack/metrics"
	"github.com/valri11/go-servicepack/middleware/cors"
	"github.com/valri11/go-servicepack/problem"
	"github.com/valri11/go-servicepack/telemetry"
)

const (
	serviceName = "basement"
	scopeName   = "github.com/valri11/basement/cmd"

	shutdownTimeout          = 10 * time.Second
	telemetryShutdownTimeout = 5 * time.Second
)

// Set with -ldflags "-X github.com/valri11/basement/cmd.version=...".
var version = ""

var serverCmd = &cobra.Command{
	Use:          "server",
	Short:        "Start the HTTP server with OpenTelemetry instrumentation",
	Long:         `Start the basement HTTP server with configurable OpenTelemetry traces, metrics, and logs export.`,
	RunE:         doServerCmd,
	SilenceUsage: true,
}

type srvHandler struct {
	cfg     config.Configuration
	tracer  trace.Tracer
	metrics *metrics.AppMetrics
	demo    demoMetrics
	ready   atomic.Bool
}

type demoMetrics struct {
	duration metric.Float64Histogram
	items    metric.Int64Counter
}

func newDemoMetrics(meter metric.Meter) (demoMetrics, error) {
	duration, err := meter.Float64Histogram(semconv.BasementDemoWorkDurationName,
		metric.WithDescription(semconv.BasementDemoWorkDurationDescription),
		metric.WithUnit(semconv.BasementDemoWorkDurationUnit),
		metric.WithExplicitBucketBoundaries(0.001, 0.0025, 0.005, 0.01, 0.025, 0.05, 0.1),
	)
	if err != nil {
		return demoMetrics{}, err
	}
	items, err := meter.Int64Counter(semconv.BasementDemoWorkItemsName,
		metric.WithDescription(semconv.BasementDemoWorkItemsDescription),
		metric.WithUnit(semconv.BasementDemoWorkItemsUnit),
	)
	if err != nil {
		return demoMetrics{}, err
	}
	return demoMetrics{duration: duration, items: items}, nil
}

func init() {
	rootCmd.AddCommand(serverCmd)

	serverCmd.Flags().Int("port", 8080, "service port to listen")
	serverCmd.Flags().BoolP("disable-tls", "", false, "development mode (http on localhost)")
	serverCmd.Flags().String("tls-cert", "", "TLS certificate file")
	serverCmd.Flags().String("tls-cert-key", "", "TLS certificate key file")
	serverCmd.Flags().BoolP("disable-telemetry", "", false, "disable telemetry publishing")
	serverCmd.Flags().String("telemetry-collector", "", "OTLP gRPC collector URL (default from OTEL_EXPORTER_OTLP_ENDPOINT)")
	serverCmd.Flags().String("log-level", "info", "log level: debug, info, warn, error")
	serverCmd.Flags().String("environment", "", "deployment.environment.name resource attribute")

	viper.BindEnv("server.disabletelemetry", "OTEL_SDK_DISABLED")

	viper.BindPFlag("server.port", serverCmd.Flags().Lookup("port"))
	viper.BindPFlag("server.disabletls", serverCmd.Flags().Lookup("disable-tls"))
	viper.BindPFlag("server.tlscertfile", serverCmd.Flags().Lookup("tls-cert"))
	viper.BindPFlag("server.tlscertkeyfile", serverCmd.Flags().Lookup("tls-cert-key"))
	viper.BindPFlag("server.disabletelemetry", serverCmd.Flags().Lookup("disable-telemetry"))
	viper.BindPFlag("server.telemetrycollector", serverCmd.Flags().Lookup("telemetry-collector"))
	viper.BindPFlag("server.loglevel", serverCmd.Flags().Lookup("log-level"))
	viper.BindPFlag("server.environment", serverCmd.Flags().Lookup("environment"))

	viper.AutomaticEnv()
}

func newWebSrvHandler(cfg config.Configuration) (*srvHandler, error) {
	meter := otel.Meter(scopeName)
	appMetrics, err := metrics.NewAppMetrics(meter)
	if err != nil {
		return nil, fmt.Errorf("failed to create metrics: %w", err)
	}
	demo, err := newDemoMetrics(meter)
	if err != nil {
		return nil, fmt.Errorf("failed to create demo metrics: %w", err)
	}

	srv := srvHandler{
		cfg:     cfg,
		tracer:  otel.Tracer(scopeName),
		metrics: appMetrics,
		demo:    demo,
	}

	return &srv, nil
}

func (h *srvHandler) routes() http.Handler {
	chain := alice.New(
		telemetry.HTTPMiddleware(),
		telemetry.WithRequestLog(),
		metrics.WithMetrics(h.metrics),
		problem.Recoverer,
	).Then

	mux := http.NewServeMux()

	// Probes are untraced: kubelet polls them every few seconds.
	mux.HandleFunc("GET /livez", h.livezHandler)
	mux.HandleFunc("GET /readyz", h.readyzHandler)

	mux.Handle("/demo/trace", chain(http.HandlerFunc(h.demoTraceHandler)))
	// Demo endpoints exercising the RFC 9457 error paths.
	mux.Handle("/demo/validation", chain(http.HandlerFunc(h.demoValidationHandler)))
	mux.Handle("/demo/panic", chain(http.HandlerFunc(h.demoPanicHandler)))

	// Unmatched routes get problem+json instead of http.NotFound's text/plain.
	mux.Handle("/", chain(problem.NotFoundHandler()))

	return cors.CORS(mux)
}

func parseLogLevel(s string) (slog.Level, error) {
	var level slog.Level
	if s == "" {
		return slog.LevelInfo, nil
	}
	if err := level.UnmarshalText([]byte(s)); err != nil {
		return level, fmt.Errorf("invalid log level %q: %w", s, err)
	}
	return level, nil
}

func doServerCmd(cmd *cobra.Command, args []string) error {
	var cfg config.Configuration
	if err := viper.Unmarshal(&cfg); err != nil {
		return fmt.Errorf("failed to unmarshal config: %w", err)
	}
	logLevel, err := parseLogLevel(cfg.Server.LogLevel)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	shutdownTelemetry, err := telemetry.InitProviders(ctx,
		cfg.Server.DisableTelemetry,
		serviceName,
		cfg.Server.TelemetryCollector,
		telemetry.WithLogLevel(logLevel),
		telemetry.WithServiceVersion(version),
		telemetry.WithEnvironment(cfg.Server.Environment),
	)
	if err != nil {
		return fmt.Errorf("failed to init telemetry providers: %w", err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), telemetryShutdownTimeout)
		defer cancel()
		if err := shutdownTelemetry(ctx); err != nil {
			slog.Error("failed to shutdown telemetry providers", "error", err)
		}
	}()
	slog.Debug("config", "cfg", cfg)

	h, err := newWebSrvHandler(cfg)
	if err != nil {
		return err
	}

	srv := &http.Server{
		Handler:      h.routes(),
		IdleTimeout:  time.Minute,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 30 * time.Second,
	}
	if !cfg.Server.DisableTLS {
		cert, err := tls.LoadX509KeyPair(cfg.Server.TLSCertFile, cfg.Server.TLSCertKeyFile)
		if err != nil {
			return fmt.Errorf("failed to load TLS certificate: %w", err)
		}
		srv.TLSConfig = &tls.Config{Certificates: []tls.Certificate{cert}}
	}

	ln, err := net.Listen("tcp", fmt.Sprintf("0.0.0.0:%d", cfg.Server.Port))
	if err != nil {
		return fmt.Errorf("failed to listen: %w", err)
	}

	srvErr := make(chan error, 1)
	go func() {
		if cfg.Server.DisableTLS {
			srvErr <- srv.Serve(ln)
		} else {
			srvErr <- srv.ServeTLS(ln, "", "")
		}
	}()
	h.ready.Store(true)
	slog.Info("server started", "addr", ln.Addr().String())

	select {
	case err := <-srvErr:
		return fmt.Errorf("server failed: %w", err)
	case <-ctx.Done():
		stop()
	}

	h.ready.Store(false)
	slog.Info("shutting down")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("server shutdown failed: %w", err)
	}
	return nil
}

func writeJSON(ctx context.Context, w http.ResponseWriter, v any) {
	out, err := json.Marshal(v)
	if err != nil {
		problem.Write(ctx, w, problem.Internal(err))
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if _, err := w.Write(out); err != nil {
		slog.WarnContext(ctx, "failed to write response", "error", err)
	}
}

type statusResponse struct {
	Status string `json:"status"`
}

func (h *srvHandler) readyzHandler(w http.ResponseWriter, r *http.Request) {
	if !h.ready.Load() {
		problem.Write(r.Context(), w,
			problem.Unavailable("Service is not ready.", 5).
				WithType(problem.TypeNotReady).
				WithInstance(r.URL.Path))
		return
	}
	writeJSON(r.Context(), w, statusResponse{Status: "ready"})
}

func (h *srvHandler) livezHandler(w http.ResponseWriter, r *http.Request) {
	writeJSON(r.Context(), w, statusResponse{Status: "ok"})
}

const maxDemoItems = 100

// demoTraceHandler does a unit of simulated work (?items=N, ?fail=true) in a
// child span, recording the basement.demo.work.* metrics defined in model/.
func (h *srvHandler) demoTraceHandler(w http.ResponseWriter, r *http.Request) {
	items := 1 + rand.IntN(10)
	if v := r.URL.Query().Get("items"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > maxDemoItems {
			problem.Write(r.Context(), w, problem.InvalidParams(problem.InvalidParam{
				Name:   "items",
				Reason: fmt.Sprintf("must be an integer between 1 and %d", maxDemoItems),
			}).WithInstance(r.URL.Path))
			return
		}
		items = n
	}
	fail := r.URL.Query().Get("fail") == "true"

	start := time.Now()
	ctx, span := h.tracer.Start(r.Context(), "demo.work")
	defer span.End()

	time.Sleep(time.Duration(items) * 200 * time.Microsecond)

	result := semconv.BasementDemoWorkResultOK
	if fail {
		result = semconv.BasementDemoWorkResultError
	}
	span.SetAttributes(result, semconv.BasementDemoWorkItemCount(items))
	attrs := metric.WithAttributes(result)
	h.demo.items.Add(ctx, int64(items), attrs)
	h.demo.duration.Record(ctx, time.Since(start).Seconds(), attrs)

	if fail {
		span.SetStatus(codes.Error, "demo work failed")
		problem.Write(ctx, w, problem.Internal(errors.New("demo work failed on request")).WithInstance(r.URL.Path))
		return
	}

	slog.InfoContext(ctx, "demo work done", string(semconv.BasementDemoWorkItemCountKey), items)

	writeJSON(ctx, w, statusResponse{Status: "ok"})
}

// demoValidationHandler returns a 400 problem with field-level errors.
func (h *srvHandler) demoValidationHandler(w http.ResponseWriter, r *http.Request) {
	problem.Write(r.Context(), w,
		problem.InvalidParams(
			problem.InvalidParam{Name: "age", Reason: "must be a positive integer"},
			problem.InvalidParam{Name: "email", Reason: "must be a valid address"},
		).WithInstance(r.URL.Path))
}

// demoPanicHandler panics, to show problem.Recoverer turning it into a 500.
func (h *srvHandler) demoPanicHandler(w http.ResponseWriter, r *http.Request) {
	panic("demo panic: this should surface as a 500 problem+json")
}
