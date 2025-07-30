package telemetry

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"time"

	slogmulti "github.com/samber/slog-multi"
	"go.opentelemetry.io/contrib/bridges/otelslog"
	"go.opentelemetry.io/contrib/instrumentation/runtime"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploggrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/log/global"
	"go.opentelemetry.io/otel/propagation"
	otelsdklog "go.opentelemetry.io/otel/sdk/log"
	"go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	"go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.34.0"
)

func InitProviders(ctx context.Context,
	enableTracing bool,
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
	slog.Info("init OTEL providers", "endpoint",
		otelEndpoint, "service", serviceName, "enableTracing", enableTracing)

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

	if !enableTracing {
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

	traceClient := otlptracegrpc.NewClient(
		otlptracegrpc.WithInsecure(),
		otlptracegrpc.WithEndpoint(otelEndpoint),
	)
	traceExporter, err := otlptrace.New(ctx, traceClient)
	if err != nil {
		return nil, fmt.Errorf("failed to create trace exporter: %w", err)
	}
	tracerProvider := trace.NewTracerProvider(
		trace.WithResource(res),
		trace.WithBatcher(traceExporter),
	)

	shutdownFuncs = append(shutdownFuncs, tracerProvider.Shutdown)
	otel.SetTracerProvider(tracerProvider)

	// setup logging

	logExporterGrpc, err := otlploggrpc.New(ctx,
		otlploggrpc.WithInsecure(),
		otlploggrpc.WithEndpoint(otelEndpoint),
	)
	if err != nil {
		err = handleErr(err)
		return nil, err
	}

	/*
		logExporterConsole, err := stdoutlog.New()
		if err != nil {
			err = handleErr(err)
			return nil, err
		}
	*/

	logProvider := otelsdklog.NewLoggerProvider(
		otelsdklog.WithProcessor(otelsdklog.NewBatchProcessor(logExporterGrpc)),
		//otelsdklog.WithProcessor(otelsdklog.NewBatchProcessor(logExporterConsole)),
		otelsdklog.WithResource(res),
	)

	global.SetLoggerProvider(logProvider)
	shutdownFuncs = append(shutdownFuncs, logProvider.Shutdown)

	//slog.SetDefault(otelslog.NewLogger(serviceName, otelslog.WithLoggerProvider(logProvider)))

	// create slog handler that will send log to otel collector
	otelSlogHandler := otelslog.NewHandler(serviceName, otelslog.WithLoggerProvider(logProvider))

	slogHandler := slog.NewJSONHandler(os.Stdout, nil)

	// create new logger that wrap 2 handlers
	logger := slog.New(slogmulti.Fanout(
		//slog.Default().Handler(),
		slogHandler,
		otelSlogHandler,
	))

	// set new logger as default
	slog.SetDefault(logger)

	// setup metrics

	metricExporter, err := otlpmetricgrpc.New(ctx,
		otlpmetricgrpc.WithInsecure(),
		otlpmetricgrpc.WithEndpoint(otelEndpoint),
	)
	if err != nil {
		err = handleErr(err)
		return nil, err
	}

	meterProvider := metric.NewMeterProvider(
		metric.WithReader(metric.NewPeriodicReader(metricExporter)),
		metric.WithResource(res),
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
