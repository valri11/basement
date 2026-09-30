Demo application to show integration with base infra: logging, metrics, traces

Uses [Task](https://taskfile.dev); `task --list` shows all tasks.

Start Grafana LGTM
```
task lgtm
```

Start application (builds first), exporting all signals over OTLP
```
task run
```

HTTP server started on default port 8080
Send queries
```
task demo
```

Go to Grafana UI: "http://localhost:3000"; the Basement dashboard
(`grafana/dashboards/basement.json`) opens as home: RED metrics, errors by
problem type, Go runtime, recent traces and logs

Print signals to stdout instead, no collector needed
```
task run:console   # traces, metrics and logs
task run:traces    # traces only
task run:metrics   # metrics only
task run:logs      # OTLP log records only
```

Or pick exporters per signal (`otlp`, `console`, `otlp,console`, `none`)
```
task run TRACES=otlp,console METRICS=none LOGS=otlp
```

Test OTEL instrumentation with oats (needs Docker)
```
task oats
```

## Telemetry schema (Weaver)

basement is the showcase for [OpenTelemetry Weaver](https://github.com/open-telemetry/weaver):
every metric, span and attribute it emits is defined in a telemetry registry in `model/`,
built on the OTel semantic conventions (v1.44.0).

- `model/demo.yaml`: basement's own telemetry (`basement.demo.work.*` metrics, span, attributes).
- `model/servicepack.yaml`: what it emits through go-servicepack, plus the upstream HTTP,
  Go runtime and resource conventions it imports. go-servicepack itself documents this in
  its `docs/metrics.md`, checked by a Go test rather than Weaver.

```
task semconv:check      # validate model/ against OTel naming policies
task semconv:generate   # regenerate internal/semconv (Go constants) and docs/telemetry
task live-check         # run basement against weaver; fail on any violation
```

The code uses only the generated constants in `internal/semconv`. `live-check` runs
basement against `weaver registry live-check` as its OTLP receiver, calls the demo
endpoints, and fails on anything the registry doesn't define, such as a new metric
missing from `model/` or a misnamed attribute. Settings and accepted exceptions are in
`.weaver.toml`. CI runs all three on every push. Reference docs: [docs/telemetry](docs/telemetry/README.md).

To add a metric: define it in `model/`, run `task semconv:generate`, use the generated
constants, and run `task live-check`.

Weaver pays off when many services or teams share conventions. For a single service or
library, a hand-written catalog plus a test (as in go-servicepack) is lighter.
