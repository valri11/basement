
#export OTEL_TRACES_EXPORTER=otlp,console
#export OTEL_LOGS_EXPORTER=otlp,console
#export OTEL_METRICS_EXPORTER=otlp,console

export OTEL_METRIC_EXPORT_INTERVAL=15000

./basement server
