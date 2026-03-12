/*
Copyright © 2025 Val Gridnev <valer.gr@gmail.com>
*/
package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"time"

	"github.com/justinas/alice"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/trace"

	"github.com/valri11/basement/config"
	"github.com/valri11/basement/metrics"
	"github.com/valri11/basement/telemetry"
	"github.com/valri11/go-servicepack/middleware/cors"
)

const (
	serviceName = "basement"
)

var serverCmd = &cobra.Command{
	Use:   "server",
	Short: "Start the HTTP server with OpenTelemetry instrumentation",
	Long:  `Start the basement HTTP server with configurable OpenTelemetry traces, metrics, and logs export.`,
	Run:   doServerCmd,
}

type srvHandler struct {
	cfg     config.Configuration
	tracer  trace.Tracer
	metrics *metrics.AppMetrics
}

func init() {
	rootCmd.AddCommand(serverCmd)

	serverCmd.Flags().Int("port", 8080, "service port to listen")
	serverCmd.Flags().BoolP("disable-tls", "", false, "development mode (http on localhost)")
	serverCmd.Flags().String("tls-cert", "", "TLS certificate file")
	serverCmd.Flags().String("tls-cert-key", "", "TLS certificate key file")
	serverCmd.Flags().BoolP("disable-telemetry", "", false, "disable telemetry publishing")
	serverCmd.Flags().String("telemetry-collector", "", "open telemetry grpc collector")

	viper.BindEnv("server.disabletelemetry", "OTEL_SDK_DISABLED")
	viper.BindEnv("server.telemetrycollector", "OTEL_EXPORTER_OTLP_ENDPOINT")

	viper.BindPFlag("server.port", serverCmd.Flags().Lookup("port"))
	viper.BindPFlag("server.disabletls", serverCmd.Flags().Lookup("disable-tls"))
	viper.BindPFlag("server.tlscertfile", serverCmd.Flags().Lookup("tls-cert"))
	viper.BindPFlag("server.tlscertkeyfile", serverCmd.Flags().Lookup("tls-cert-key"))
	viper.BindPFlag("server.disabletelemetry", serverCmd.Flags().Lookup("disable-telemetry"))
	viper.BindPFlag("server.telemetrycollector", serverCmd.Flags().Lookup("telemetry-collector"))

	viper.AutomaticEnv()
}

func newWebSrvHandler(cfg config.Configuration) (*srvHandler, error) {
	tracer := otel.Tracer(serviceName)

	appMetrics, err := metrics.NewAppMetrics(otel.GetMeterProvider().Meter(serviceName))
	if err != nil {
		return nil, fmt.Errorf("failed to create metrics: %w", err)
	}

	srv := srvHandler{
		cfg:     cfg,
		tracer:  tracer,
		metrics: appMetrics,
	}

	return &srv, nil
}

func doServerCmd(cmd *cobra.Command, args []string) {
	// Set up early logger with debug level (telemetry.InitProviders will replace with fanout logger)
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	}))
	slog.SetDefault(logger)

	var cfg config.Configuration
	err := viper.Unmarshal(&cfg)
	if err != nil {
		slog.Error("failed to unmarshal config", "error", err)
		os.Exit(1)
	}
	slog.Debug("config", "cfg", cfg)

	ctx := context.Background()
	// Handle SIGINT (CTRL+C) gracefully.
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt)
	defer stop()

	shutdown, err := telemetry.InitProviders(context.Background(), cfg.Server.DisableTelemetry, serviceName, cfg.Server.TelemetryCollector)
	if err != nil {
		slog.Error("failed to init telemetry providers", "error", err)
		os.Exit(1)
	}
	defer func() {
		if err := shutdown(context.Background()); err != nil {
			slog.Error("failed to shutdown telemetry providers", "error", err)
		}
	}()

	h, err := newWebSrvHandler(cfg)
	if err != nil {
		slog.Error("failed to create server handler", "error", err)
		os.Exit(1)
	}

	mux := http.NewServeMux()

	mwChain := []alice.Constructor{
		cors.CORS,
		telemetry.WithOtelTracerContext(h.tracer),
		telemetry.WithRequestLog(),
		metrics.WithMetrics(h.metrics),
	}
	handlerChain := alice.New(mwChain...).Then

	mux.Handle("/livez",
		handlerChain(
			otelhttp.NewHandler(http.HandlerFunc(h.livezHandler), "livez")))

	srv := &http.Server{
		Addr:         fmt.Sprintf("0.0.0.0:%d", cfg.Server.Port),
		BaseContext:  func(_ net.Listener) context.Context { return ctx },
		Handler:      mux,
		IdleTimeout:  time.Minute,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 30 * time.Second,
	}

	srvErr := make(chan error, 1)
	go func() {
		slog.Info("server started", "port", cfg.Server.Port)
		if cfg.Server.DisableTLS {
			srvErr <- srv.ListenAndServe()
		} else {
			srvErr <- srv.ListenAndServeTLS(cfg.Server.TLSCertFile, cfg.Server.TLSCertKeyFile)
		}
	}()

	// Wait for interruption or server error.
	select {
	case err := <-srvErr:
		slog.Error("server failed to start", "error", err)
		return
	case <-ctx.Done():
		stop()
	}

	// Graceful shutdown with timeout.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		slog.Error("server shutdown failed", "error", err)
	}
}

func (h *srvHandler) livezHandler(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	tracer := telemetry.TracerOrDefault(ctx)
	_, span := tracer.Start(ctx, "livezHandler")
	defer span.End()

	slog.DebugContext(ctx, "livez")
	slog.InfoContext(ctx, "test log message")

	res := struct {
		Status string `json:"status"`
	}{
		Status: "ok",
	}

	out, err := json.Marshal(res)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if _, err := w.Write(out); err != nil {
		slog.WarnContext(ctx, "failed to write response", "error", err)
	}
}
