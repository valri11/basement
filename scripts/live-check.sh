#!/usr/bin/env bash
# Runs basement against `weaver registry live-check`, exercises the demo
# endpoints, and exits non-zero on any violation of the basement registry
# (model/, which builds on the OTel semantic conventions).
set -euo pipefail

WEAVER=${WEAVER:-weaver}
BIN=${BIN:-./bin/basement}
APP_PORT=${APP_PORT:-18090}
OTLP_PORT=${OTLP_PORT:-14317}
ADMIN_PORT=${ADMIN_PORT:-14320}

wait_for() {
	for _ in $(seq 60); do
		curl -sf "$1" >/dev/null && return 0
		kill -0 "$2" 2>/dev/null || { echo "process $2 exited" >&2; return 1; }
		sleep 0.5
	done
	echo "timed out waiting for $1" >&2
	return 1
}

"$WEAVER" registry live-check --no-stream \
	--otlp-grpc-port "$OTLP_PORT" --admin-port "$ADMIN_PORT" --inactivity-timeout 120 &
weaver_pid=$!
trap 'kill "$weaver_pid" 2>/dev/null || true' EXIT
wait_for "http://127.0.0.1:$ADMIN_PORT/health" "$weaver_pid"

OTEL_EXPORTER_OTLP_ENDPOINT="http://127.0.0.1:$OTLP_PORT" \
OTEL_METRIC_EXPORT_INTERVAL=1000 \
	"$BIN" server --port "$APP_PORT" --log-level debug >/dev/null &
app_pid=$!
wait_for "http://127.0.0.1:$APP_PORT/livez" "$app_pid"

for path in /demo/trace "/demo/trace?items=5" "/demo/trace?fail=true" /demo/validation /demo/panic /no-such-route; do
	curl -s -o /dev/null "http://127.0.0.1:$APP_PORT$path"
done
sleep 2

# SIGTERM makes basement flush all signals before exiting.
kill -TERM "$app_pid"
wait "$app_pid" || true

curl -s -X POST "http://127.0.0.1:$ADMIN_PORT/stop" >/dev/null
trap - EXIT
wait "$weaver_pid"
