# Observability tutorial: from instrumented code to a deployed dashboard

This tutorial follows one piece of telemetry through its whole life in basement: the
`basement.demo.work.*` metrics and the `demo.work` span. You will see where they are defined,
how the code records them, how they arrive in Prometheus, Tempo and Loki, how the Grafana
dashboard that shows them is written in Go, and how that dashboard reaches the homelab
Grafana. To add telemetry of your own, repeat the same steps with your own names.

You need Go 1.26, Docker, and [Task](https://taskfile.dev). Every command below runs from
the root of the basement repo.

## Contents

1. [Run the stack and look at the three signals](#1-run-the-stack-and-look-at-the-three-signals)
2. [What go-servicepack records for you](#2-what-go-servicepack-records-for-you)
3. [Define your telemetry in the registry](#3-define-your-telemetry-in-the-registry)
4. [Record it in the code](#4-record-it-in-the-code)
5. [Check what the service emits](#5-check-what-the-service-emits)
6. [Find your metric in Prometheus](#6-find-your-metric-in-prometheus)
7. [Code the dashboard](#7-code-the-dashboard)
8. [Deploy the service and the dashboard](#8-deploy-the-service-and-the-dashboard)
9. [Troubleshooting](#9-troubleshooting)

---

## 1. Run the stack and look at the three signals

Start basement together with [grafana/otel-lgtm](https://github.com/grafana/docker-otel-lgtm),
a single container that runs an OpenTelemetry collector, Prometheus, Tempo, Loki and Grafana:

```sh
docker compose -f docker-compose.lgtm.yml up -d --build
task demo DEMO_DURATION=60
```

`task demo` calls each demo endpoint in a loop: successful work, failed work, a validation
error, a panic and an unknown route. Open **http://localhost:3000**. Grafana opens on the
Basement dashboard, which shows all three signals:

| Signal | Where it goes | What to look at |
|---|---|---|
| Metrics | Prometheus | The Overview and Demo work rows |
| Traces | Tempo | *Recent server traces*; click a trace ID to open the waterfall |
| Logs | Loki | *Logs*; expand a line and follow `trace_id` to its trace |

The signals are linked to each other. The dots on the *Latency* and *Work duration* graphs
are exemplars: each one is a single request that carries a trace ID, and clicking it opens
that trace. A log line written during a request carries the request's `trace_id`, so a log
line leads to its trace and a span leads to its log lines.

Stop the stack with `docker compose -f docker-compose.lgtm.yml down` when you are done.

---

## 2. What go-servicepack records for you

Most of basement's telemetry needs no code in basement itself.
[go-servicepack](https://github.com/valri11/go-servicepack) provides it, and `cmd/server.go`
wires it up in two places.

**At startup**, `telemetry.InitProviders` creates the OpenTelemetry trace, metric and log
providers and connects them to an OTLP exporter:

```go
shutdownTelemetry, err := telemetry.InitProviders(ctx,
	cfg.Server.DisableTelemetry,
	serviceName,
	cfg.Server.TelemetryCollector,
	telemetry.WithLogLevel(logLevel),
	telemetry.WithServiceVersion(version),
	telemetry.WithEnvironment(cfg.Server.Environment),
)
```

It also replaces the default `slog` logger. Every log line goes to stdout as JSON and, over
OTLP, to Loki. The collector address comes from `OTEL_EXPORTER_OTLP_ENDPOINT` unless
`--telemetry-collector` is set, and `OTEL_{TRACES,METRICS,LOGS}_EXPORTER` choose `otlp`,
`console` or `none` for each signal (`task run:console` prints all three to stdout).
`serviceName` becomes the `service.name` resource attribute, which every signal carries and
every dashboard query filters on.

**Around each route**, a middleware chain records each request:

```go
chain := alice.New(
	telemetry.HTTPMiddleware(),   // SERVER span and http.server.* metrics
	telemetry.WithRequestLog(),   // one debug log line per request, with its trace ID
	metrics.WithMetrics(h.metrics), // servicepack.http.server.problems, by RFC 9457 type
	problem.Recoverer,            // a panic becomes a 500 problem+json, recorded on the span
).Then
```

That chain gives every route a SERVER span named after its route pattern (for example
`GET /demo/trace`), the `http.server.request.duration` histogram, and an error count broken
down by problem type. The probes (`/livez`, `/readyz`) are left outside the chain on
purpose: the kubelet calls them every few seconds, and their spans would crowd out real
requests.

Section 4 covers what this chain cannot know: what a handler actually does. That is the
telemetry you define yourself.

---

## 3. Define your telemetry in the registry

basement defines every attribute, metric and span it emits in a telemetry registry in
`model/`, using [OpenTelemetry Weaver](https://github.com/open-telemetry/weaver). The code
never spells a telemetry name as a string. It uses constants generated from the registry, so
a name cannot drift between the code, the documentation and the dashboard.

The demo work is defined in `model/demo.yaml`. First the attributes:

```yaml
attributes:
  - key: basement.demo.work.result
    type:
      members:
        - id: ok
          value: ok
          brief: The work unit completed.
          stability: development
        - id: error
          value: error
          brief: The work unit failed.
          stability: development
    stability: development
    brief: Outcome of a demo work unit.
```

`basement.demo.work.result` is an enum with two values, which also keeps the number of time
series fixed: a metric attribute becomes a label, and every distinct label value is a
separate time series in Prometheus. A value taken from user input, such
as a user ID or a raw URL, would create a new series for each new value.

Then the metrics that use it:

```yaml
metrics:
  - name: basement.demo.work.duration
    instrument: histogram
    unit: s
    brief: Duration of demo work units.
    attributes:
      - ref: basement.demo.work.result
        requirement_level: required
  - name: basement.demo.work.items
    instrument: counter
    unit: "{item}"
    brief: Number of items processed by demo work units.
    attributes:
      - ref: basement.demo.work.result
        requirement_level: required
```

The names follow the OpenTelemetry conventions: lower case, dot-separated, and prefixed with
a namespace (`basement.`) that no other project uses. Durations are histograms in seconds, and
counts are counters with a unit in braces. The file also defines the `demo.work` span and its
attributes.

When you change the registry, validate it and regenerate the constants:

```sh
task semconv:check      # checks model/ against the OTel naming policies
task semconv:generate   # regenerates internal/semconv and docs/telemetry
```

`internal/semconv/semconv.go` now holds, among others:

```go
BasementDemoWorkResultKey = attribute.Key("basement.demo.work.result")
BasementDemoWorkResultOK    = BasementDemoWorkResultKey.String("ok")
BasementDemoWorkResultError = BasementDemoWorkResultKey.String("error")
func BasementDemoWorkItemCount(val int) attribute.KeyValue
```

and the metric names, units and descriptions such as `BasementDemoWorkDurationName`.
`docs/telemetry` is regenerated at the same time, as reference documentation for everything
the service emits.

---

## 4. Record it in the code

All the code for the demo work is in `cmd/server.go`.

**Create the instruments once, at startup.** A meter and a tracer are named after the code's
instrumentation scope, and the instruments take their names, units and descriptions from the
generated constants:

```go
meter := otel.Meter(scopeName)

duration, err := meter.Float64Histogram(semconv.BasementDemoWorkDurationName,
	metric.WithDescription(semconv.BasementDemoWorkDurationDescription),
	metric.WithUnit(semconv.BasementDemoWorkDurationUnit),
	metric.WithExplicitBucketBoundaries(0.001, 0.0025, 0.005, 0.01, 0.025, 0.05, 0.1),
)
```

Choose the histogram's bucket boundaries around the latencies you expect. The SDK's
defaults go from 0 to 10,000 in the metric's unit, which suits milliseconds; for a duration in
seconds that takes a few milliseconds, every sample would fall into the first bucket and the
percentiles on the dashboard would be meaningless.

**Record them in the handler.** `demoTraceHandler` starts a child span for the work, sets
its attributes, and records both metrics with the same attribute:

```go
ctx, span := h.tracer.Start(r.Context(), "demo.work")
defer span.End()

// ... the work ...

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
```

Four details in this code are what make the signals line up:

- **Pass the span's context on.** `tracer.Start` returns a new `ctx`, and everything after
  it uses that `ctx`. If you discard it (`_, span := tracer.Start(...)`), the span has no
  children, and the metrics and logs recorded afterwards are not linked to it.
- **Record metrics with that context.** When `Record` receives a context that holds a
  sampled span, the SDK attaches the span's trace ID to the sample as an exemplar. That
  trace ID is what the exemplar dots on the dashboard link to.
- **Log with `slog.InfoContext(ctx, ...)`.** The `Context` variants are the ones that put
  `trace_id` and `span_id` on the log line. `slog.Info` without a context writes a line that
  cannot be linked to any trace.
- **Set the span status to Error only for failures of your own.** The work failing is a
  server error, so the span gets `codes.Error`. A 4xx response caused by the caller, such as
  the invalid `?items=` value, leaves the status unset, as the OpenTelemetry HTTP conventions
  specify. Otherwise error-rate queries in Tempo would count client mistakes as failures.

---

## 5. Check what the service emits

Two checks confirm that the service emits what the registry says, before anyone looks at a
dashboard:

```sh
task live-check   # runs basement against Weaver and fails on any violation
task oats         # acceptance tests against the LGTM stack
```

`task live-check` starts `weaver registry live-check` as the service's OTLP receiver, calls
the demo endpoints, and fails if basement emits anything the registry does not define, such
as a new metric missing from `model/` or a misspelled attribute. CI runs it on every push,
together with `task semconv:verify`, which fails if `internal/semconv` or `docs/telemetry` is
out of date.

`task oats` runs [OATS](https://github.com/grafana/oats): it starts the service and the LGTM
stack, calls the endpoints listed in `oats.yaml`, and checks the resulting traces, metrics
and logs with TraceQL, PromQL and LogQL. `oats.yaml` is also a useful collection of working
queries to start a dashboard from.

---

## 6. Find your metric in Prometheus

Before writing a dashboard query you need the metric's name in Prometheus, which is not the
name in the registry. Prometheus receives the metrics over OTLP and translates them:

- Dots become underscores.
- The unit is appended as a suffix (`_seconds`, `_bytes`). A unit in braces, such as
  `{item}`, is an annotation and adds nothing.
- A counter gets `_total`, and a histogram becomes three series: `_bucket`, `_sum` and
  `_count`.
- The `service.name` resource attribute becomes the `job` label, and attribute names are
  translated like metric names.

For basement:

| In the registry | In Prometheus |
|---|---|
| `basement.demo.work.items` (counter, `{item}`) | `basement_demo_work_items_total` |
| `basement.demo.work.duration` (histogram, `s`) | `basement_demo_work_duration_seconds_bucket`, `_sum`, `_count` |
| attribute `basement.demo.work.result` | label `basement_demo_work_result` |
| `http.server.request.duration` (from go-servicepack) | `http_server_request_duration_seconds_bucket`, … |
| `servicepack.http.server.problems` (from go-servicepack) | `servicepack_http_server_problems_total` |
| `service.name` = `basement` | `job="basement"` |

To check a name, open **Explore → Prometheus** in Grafana and type the start of it; the
query editor completes the metric names that exist. The PromQL for a panel then follows
from what the metric is:

```promql
# items per second, by outcome
sum by (basement_demo_work_result) (rate(basement_demo_work_items_total{job="basement"}[$__rate_interval]))

# 95th percentile work duration
histogram_quantile(0.95, sum by (le) (rate(basement_demo_work_duration_seconds_bucket{job="basement"}[$__rate_interval])))
```

Use `rate()` on counters and on histogram buckets, never the raw value: a counter only grows,
and it restarts from zero when the process restarts. `$__rate_interval` is a Grafana variable
that picks a range long enough to contain several samples.

---

## 7. Code the dashboard

The dashboard is written in Go with the
[Grafana Foundation SDK](https://github.com/grafana/grafana-foundation-sdk), in `grafana/`.
The JSON that Grafana loads, `grafana/dashboards/basement.json`, is generated from it and
is never edited by hand. `grafana/` is a Go module of its own, so the SDK is not a
dependency of the service.

| File | What it holds |
|---|---|
| `grafana/basement.go` | The Basement dashboard: its variables, rows and panels |
| `grafana/style.go` | Helpers shared by every panel: datasource references, the stat and timeseries styles, query helpers, the traces table and the logs panel |
| `grafana/main.go` | Writes each dashboard to `grafana/dashboards/` |
| `grafana/dashboards_test.go` | Fails if the committed JSON does not match the Go |

### Read a panel

Here is the *Items processed* panel from `basement.go`:

```go
func itemsProcessed() cog.Builder[dashboard.Panel] {
	return timeseriesPanel("Items processed", "short", true).
		Description("basement.demo.work.items, defined in basement's model/.").
		WithTarget(prom(
			`sum by (basement_demo_work_result) (rate(basement_demo_work_items_total{`+byService+`}[$__rate_interval]))`,
			"{{basement_demo_work_result}}")).
		OverrideByName("ok", []dashboard.DynamicConfigValue{fixedColor(colorGood)}).
		OverrideByName("error", []dashboard.DynamicConfigValue{fixedColor(colorBad)}).
		Span(12).Height(7)
}
```

- `timeseriesPanel(title, unit, stacked)` returns a timeseries panel in the line style that
  every graph on the dashboard shares. `stacked` is true because the two outcomes add up to
  the total rate.
- `prom(expr, legend)` is a Prometheus range query. `byService` is `job=~"$service"`, so the
  panel follows the dashboard's *Service* variable. The legend `{{basement_demo_work_result}}`
  names each series after its label value.
- `OverrideByName` pins the `ok` series to green and `error` to red.
- `Span(12).Height(7)` sets the panel's size on Grafana's 24-column grid. The SDK places
  panels left to right in the order they are added to the dashboard and starts a new line
  when a line is full, so the Go code never sets positions.

The dashboard itself is a chain of rows and panels in `basementDashboard()`:

```go
WithRow(dashboard.NewRowBuilder("Demo work (basement registry)")).
WithPanel(itemsProcessed()).
WithPanel(workDuration()).
```

### Add a panel

Suppose you want a panel showing the average number of items per work unit. The counter
gives items per second, and the histogram's `_count` gives work units per second, so their
ratio is the average:

1. Add a function to `grafana/basement.go`:

   ```go
   func itemsPerUnit() cog.Builder[dashboard.Panel] {
   	return timeseriesPanel("Items per work unit", "short", false).
   		Description("Average basement.demo.work.item_count per work unit.").
   		WithTarget(prom(
   			`sum(rate(basement_demo_work_items_total{`+byService+`}[$__rate_interval]))`+
   				` / sum(rate(basement_demo_work_duration_seconds_count{`+byService+`}[$__rate_interval]))`,
   			"items / unit")).
   		Span(8).Height(7)
   }
   ```

2. Add it to the dashboard, in the row where it belongs. To keep the row's line full, change
   the two existing panels in that row from `Span(12)` to `Span(8)`:

   ```go
   WithRow(dashboard.NewRowBuilder("Demo work (basement registry)")).
   WithPanel(itemsProcessed()).
   WithPanel(workDuration()).
   WithPanel(itemsPerUnit()).
   ```

3. Regenerate the JSON and run the tests:

   ```sh
   task dashboards:generate
   task dashboards:verify
   ```

4. With the local stack running (section 1), reload the dashboard in the browser. Grafana
   rescans `grafana/dashboards/` every ten seconds, so the new panel appears without a
   restart.

5. Commit both the Go and the regenerated JSON. CI fails the build if the JSON does not match
   the Go.

If a query returns nothing, run it in **Explore** first, with the variables replaced by real
values (`job="basement"` instead of `job=~"$service"`). A query that works in Explore and not
in the panel usually means a variable is not matching.

### Why the dashboard is code

A dashboard edited in the Grafana UI lives in Grafana's database. Its exported JSON is
thousands of lines in which a one-word change to a query shows up as a large diff, and a
second service that wants the same panels has to copy them. As Go:

- The review diff for a changed query is the query.
- Panels share one style, from `style.go`, so they look the same without anyone keeping them
  in sync.
- The compiler and the test catch a broken dashboard before it is deployed, rather than an
  empty panel after it is deployed.
- Another service's dashboard can start from the same helpers.

If you prefer to try a panel in the Grafana UI first, build it there, open **Inspect → Panel
JSON** to see its settings, and then write the same settings in Go. Grafana does not save
changes to a provisioned dashboard, so the Go is the only place a change can be kept.

---

## 8. Deploy the service and the dashboard

The homelab runs basement from the Helm chart in
[homelab-infra](http://datavault2.home.lab:15300/val/homelab-infra) (`k8s/apps/basement`), deployed by
Flux from `k8s/clusters/tg-dev/apps/basement.yaml`. The chart installs the dashboard as
well, so the service and the dashboard that describes it are released together.

### How the dashboard gets into Grafana

The homelab Grafana comes from the kube-prometheus-stack chart, which runs a sidecar next to
Grafana. The sidecar watches for ConfigMaps labelled `grafana_dashboard: "1"`, in every
namespace, and loads the JSON they contain. basement's chart has a template for that
ConfigMap, `templates/grafana-dashboard.yaml`:

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: {{ include "basement.fullname" . }}-grafana-dashboard
  labels:
    {{- include "basement.labels" . | nindent 4 }}
    grafana_dashboard: "1"
data:
  basement.json: |-
{{ .Files.Get "dashboards/basement.json" | indent 4 }}
```

`.Files.Get` reads `k8s/apps/basement/dashboards/basement.json`, a copy of the generated
file. The `grafanaDashboard.enabled` value, on by default, turns the template on.

The datasource references in the generated JSON (`prometheus`, `tempo` and `loki`) are the
uids of the datasources in both the local LGTM stack and the homelab Grafana, so the same
file works in both places.

### Deploy a change

1. **Push the basement change.** Gitea Actions tests it, runs the telemetry and dashboard
   checks, and pushes the image `datavault2.home.lab:15300/val/basement:<commit-sha>`.

2. **Copy the dashboard into homelab-infra**, if it changed:

   ```sh
   cp grafana/dashboards/basement.json ../homeLab/homelab-infra/k8s/apps/basement/dashboards/basement.json
   ```

   Adjust the path to where your homelab-infra checkout is.

3. **Set the new image.** In `k8s/clusters/tg-dev/apps/basement.yaml`, set `image.tag` to the
   commit SHA from step 1. Deploy the image and the dashboard together, so that new panels
   do not show up before the metrics they query.

4. **Push homelab-infra.** The HelmRelease uses `reconcileStrategy: Revision`, so Flux
   upgrades the release on every new commit, including a commit that changes only the
   dashboard.

5. **Check it in Grafana** at **http://grafana.home.lab**: open *Dashboards → Basement*,
   and confirm that the panels show data for `Service = basement`. The chart's
   HOWTO, `k8s/apps/basement/HOWTO.md` in homelab-infra, lists Explore queries that check
   each signal on its own.

In the cluster, basement does not send to Prometheus, Tempo and Loki directly. It sends
OTLP to the OpenTelemetry collector on its node (`OTEL_EXPORTER_OTLP_ENDPOINT` is set to
the node's address by the chart), and the collector forwards each signal to its backend and
adds the Kubernetes resource attributes. The service code is the same as locally; only the
endpoint differs.

---

## 9. Troubleshooting

**A new metric is missing from Prometheus.** Metrics are exported every 60 seconds by
default; the local stack sets `OTEL_METRIC_EXPORT_INTERVAL` to 5 seconds. A counter has no
series until its first `Add`, so make a request that records it. Then search for the
translated name (section 6), not the registry name.

**A panel shows "No data" but Explore shows the metric.** Check the label values against the
dashboard's variables. In the cluster, `job` is `service.name`, which the chart sets from
`otel.serviceName`.

**Exemplar dots are missing.** The metric must be recorded with the request's context
(section 4), and the span must be sampled. In the cluster, Prometheus stores exemplars only
with its `exemplar-storage` feature enabled.

**Log lines have no `trace_id`.** The code logged with `slog.Info` rather than
`slog.InfoContext(ctx, ...)`, or it logged outside any request.

**`task dashboards:verify` fails.** The JSON in `grafana/dashboards/` does not match the Go.
Run `task dashboards:generate` and commit the result; do not edit the JSON.

**The dashboard shows up twice in the homelab Grafana.** An older copy of it exists as a
ConfigMap outside the chart. Delete it, and keep the one the chart installs.
