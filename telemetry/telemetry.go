package telemetry

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"slices"
	"strings"
	"time"

	slogmulti "github.com/samber/slog-multi"
	"go.opentelemetry.io/contrib/bridges/otelslog"
	"go.opentelemetry.io/contrib/instrumentation/runtime"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploggrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/exporters/stdout/stdoutlog"
	"go.opentelemetry.io/otel/exporters/stdout/stdoutmetric"
	"go.opentelemetry.io/otel/exporters/stdout/stdouttrace"
	"go.opentelemetry.io/otel/log/global"
	"go.opentelemetry.io/otel/propagation"
	otelsdklog "go.opentelemetry.io/otel/sdk/log"
	"go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	"go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.34.0"
)

func InitProviders(ctx context.Context,
	disableTelemetry bool,
	serviceName string,
	otelEndpoint string,
) (func(context.Context) error, error) {
	var shutdownFuncs []func(context.Context) error

	if otelEndpoint == "" {
		otelEndpoint = os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")
		if otelEndpoint == "" {
			otelEndpoint = "localhost:4317"
		}
	}
	slog.Debug("init OTEL providers",
		"endpoint", otelEndpoint,
		"service", serviceName,
		"disableTelemetry", disableTelemetry,
	)

	// shutdown calls cleanup functions registered via shutdownFuncs.
	// The errors from the calls are joined.
	// Each registered cleanup will be invoked once.
	shutdown := func(ctx context.Context) error {
		var err error
		for _, fn := range shutdownFuncs {
			err = errors.Join(err, fn(ctx))
		}
		shutdownFuncs = nil
		return err
	}

	if disableTelemetry {
		return shutdown, nil
	}

	// handleErr calls shutdown for cleanup and makes sure that all errors are returned.
	handleErr := func(inErr error) error {
		return errors.Join(inErr, shutdown(ctx))
	}

	res, err := resource.New(ctx,
		resource.WithAttributes(
			// the service name used to display traces in backends
			semconv.ServiceName(serviceName),
		),
		resource.WithHost(),
		resource.WithFromEnv(),
		resource.WithTelemetrySDK(),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create resource: %w", err)
	}

	// setup tracing

	prop := propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	)
	otel.SetTextMapPropagator(prop)

	traceProviderOptions := []trace.TracerProviderOption{
		trace.WithResource(res),
	}

	envTraceExporters := strings.Split(os.Getenv("OTEL_TRACES_EXPORTER"), ",")

	if len(envTraceExporters) == 0 || slices.Contains(envTraceExporters, "otlp") {
		traceClient := otlptracegrpc.NewClient(
			otlptracegrpc.WithInsecure(),
			otlptracegrpc.WithEndpoint(otelEndpoint),
		)
		traceExporter, err := otlptrace.New(ctx, traceClient)
		if err != nil {
			return nil, fmt.Errorf("failed to create trace exporter: %w", err)
		}
		traceProviderOptions = append(traceProviderOptions,
			trace.WithBatcher(traceExporter))
	}

	if slices.Contains(envTraceExporters, "console") {
		traceConsoleExporter, err := stdouttrace.New(stdouttrace.WithPrettyPrint())
		if err != nil {
			return nil, fmt.Errorf("failed to create trace console exporter: %w", err)
		}
		traceProviderOptions = append(traceProviderOptions,
			trace.WithBatcher(traceConsoleExporter))
	}

	tracerProvider := trace.NewTracerProvider(traceProviderOptions...)

	shutdownFuncs = append(shutdownFuncs, tracerProvider.Shutdown)
	otel.SetTracerProvider(tracerProvider)

	// setup logging

	logsProviderOptions := []otelsdklog.LoggerProviderOption{
		otelsdklog.WithResource(res),
	}

	envLogsExporters := strings.Split(os.Getenv("OTEL_LOGS_EXPORTER"), ",")

	if len(envLogsExporters) == 0 || slices.Contains(envLogsExporters, "otlp") {
		logExporterGrpc, err := otlploggrpc.New(ctx,
			otlploggrpc.WithInsecure(),
			otlploggrpc.WithEndpoint(otelEndpoint),
		)
		if err != nil {
			err = handleErr(err)
			return nil, err
		}
		logsProviderOptions = append(logsProviderOptions,
			otelsdklog.WithProcessor(otelsdklog.NewBatchProcessor(logExporterGrpc)))
	}

	if slices.Contains(envLogsExporters, "console") {
		logExporterConsole, err := stdoutlog.New()
		if err != nil {
			err = handleErr(err)
			return nil, err
		}
		logsProviderOptions = append(logsProviderOptions,
			otelsdklog.WithProcessor(otelsdklog.NewBatchProcessor(logExporterConsole)))
	}

	logProvider := otelsdklog.NewLoggerProvider(logsProviderOptions...)

	global.SetLoggerProvider(logProvider)
	shutdownFuncs = append(shutdownFuncs, logProvider.Shutdown)

	//slog.SetDefault(otelslog.NewLogger(serviceName, otelslog.WithLoggerProvider(logProvider)))

	// create slog handler that will send log to otel collector
	otelSlogHandler := otelslog.NewHandler(serviceName, otelslog.WithLoggerProvider(logProvider))

	slogHandler := slog.NewJSONHandler(os.Stdout, nil)

	// create new logger that wrap 2 handlers
	logger := slog.New(slogmulti.Fanout(
		slogHandler,
		otelSlogHandler,
	))

	// set new logger as default
	slog.SetDefault(logger)

	// setup metrics

	metricProviderOptions := []metric.Option{
		metric.WithResource(res),
	}

	envMetricExporters := strings.Split(os.Getenv("OTEL_METRICS_EXPORTER"), ",")

	if len(envMetricExporters) == 0 || slices.Contains(envMetricExporters, "otlp") {
		metricExporter, err := otlpmetricgrpc.New(ctx,
			otlpmetricgrpc.WithInsecure(),
			otlpmetricgrpc.WithEndpoint(otelEndpoint),
		)
		if err != nil {
			err = handleErr(err)
			return nil, err
		}
		metricProviderOptions = append(metricProviderOptions,
			metric.WithReader(metric.NewPeriodicReader(metricExporter)),
		)
	}

	if slices.Contains(envMetricExporters, "console") {
		metricExporterConsole, err := stdoutmetric.New()
		if err != nil {
			err = handleErr(err)
			return nil, err
		}
		metricProviderOptions = append(metricProviderOptions,
			metric.WithReader(metric.NewPeriodicReader(metricExporterConsole)),
		)
	}

	meterProvider := metric.NewMeterProvider(
		metricProviderOptions...,
	)

	otel.SetMeterProvider(meterProvider)
	shutdownFuncs = append(shutdownFuncs, meterProvider.Shutdown)

	err = runtime.Start(runtime.WithMinimumReadMemStatsInterval(time.Second))
	if err != nil {
		err = handleErr(err)
		return nil, err
	}

	return shutdown, nil
}
