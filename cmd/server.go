/*
Copyright © 2025 NAME HERE <EMAIL ADDRESS>
*/
package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
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

// serverCmd represents the server command
var serverCmd = &cobra.Command{
	Use:   "server",
	Short: "A brief description of your command",
	Long: `A longer description that spans multiple lines and likely contains examples
and usage of using your command. For example:

Cobra is a CLI library for Go that empowers applications.
This application is a tool to generate the needed files
to quickly create a Cobra application.`,
	Run: doServerCmd,
}

type srvHandler struct {
	cfg     config.Configuration
	tracer  trace.Tracer
	metrics *metrics.AppMetrics
}

func init() {
	rootCmd.AddCommand(serverCmd)

	serverCmd.Flags().Int("port", 8080, "service port to listen")
	serverCmd.Flags().BoolP("disable-tls", "", false, "development mode (http on loclahost)")
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
	viper.BindPFlag("server.disablelemetry", serverCmd.Flags().Lookup("disable-telemetry"))
	viper.BindPFlag("server.telemetrycollector", serverCmd.Flags().Lookup("telemetry-collector"))

	viper.AutomaticEnv()
}

func newWebSrvHandler(cfg config.Configuration) (*srvHandler, error) {
	tracer := otel.Tracer(serviceName)

	metrics, err := metrics.NewAppMetrics(otel.GetMeterProvider().Meter(serviceName))
	if err != nil {
		panic(err)
	}

	srv := srvHandler{
		cfg:     cfg,
		tracer:  tracer,
		metrics: metrics,
	}

	return &srv, nil
}

func doServerCmd(cmd *cobra.Command, args []string) {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	}))
	slog.SetDefault(logger)

	var cfg config.Configuration
	err := viper.Unmarshal(&cfg)
	if err != nil {
		log.Fatalf("ERR: %v", err)
		return
	}
	slog.Debug("config", "cfg", cfg)

	ctx := context.Background()
	// Handle SIGINT (CTRL+C) gracefully.
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt)
	defer stop()

	shutdown, err := telemetry.InitProviders(context.Background(), cfg.Server.DisableTelemetry, serviceName, cfg.Server.TelemetryCollector)
	if err != nil {
		log.Fatal(err)
	}
	defer func() {
		if err := shutdown(context.Background()); err != nil {
			log.Fatal("failed to shutdown TracerProvider: %w", err)
		}
	}()

	h, err := newWebSrvHandler(cfg)
	if err != nil {
		log.Fatalf("ERR: %v", err)
		return
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

	// start server listen with error handling
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

	// Wait for interruption.
	select {
	case <-srvErr:
		// Error when starting HTTP server.
		return
	case <-ctx.Done():
		// Wait for first CTRL+C.
		// Stop receiving signal notifications as soon as possible.
		stop()
	}

	// When Shutdown is called, ListenAndServe immediately returns ErrServerClosed.
	srv.Shutdown(context.Background())
}

func (h *srvHandler) livezHandler(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	tracer := telemetry.MustTracerFromContext(ctx)
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
	w.Write(out)
}
