package main

import (
	"github.com/grafana/grafana-foundation-sdk/go/cog"
	"github.com/grafana/grafana-foundation-sdk/go/common"
	"github.com/grafana/grafana-foundation-sdk/go/dashboard"
)

// Label selectors shared by the queries. $service is the job (the OTel
// service.name) and $route the http.route; the Go runtime and demo-work
// metrics have no route, so they filter on the service only.
const (
	byService      = `job=~"$service"`
	byServiceRoute = `job=~"$service", http_route=~"$route"`
)

// basementDashboard shows RED metrics, Go runtime, traces and logs for a
// service instrumented with go-servicepack, plus the demo-work metrics from
// basement's own registry (model/).
func basementDashboard() *dashboard.DashboardBuilder {
	return dashboard.NewDashboardBuilder("Basement").
		Uid("basement-overview").
		Description("RED metrics, Go runtime, traces and logs for services instrumented with go-servicepack.").
		Tags([]string{"basement", "otel", "go"}).
		Editable().
		Tooltip(dashboard.DashboardCursorSyncCrosshair).
		Refresh("10s").
		Time("now-30m", "now").
		Link(dashboard.NewDashboardLinkBuilder("Explore traces").
			Type(dashboard.DashboardLinkTypeLink).
			Icon("external link").
			Url("/a/grafana-exploretraces-app/explore").
			TargetBlank(false)).
		WithVariable(serviceVariable()).
		WithVariable(routeVariable()).
		WithRow(dashboard.NewRowBuilder("Overview")).
		WithPanel(requestRateStat()).
		WithPanel(errorRatioStat("Server errors (5xx)", "5..",
			"Share of responses with a 5xx status.",
			thresholds(step(nil, colorGood), step(cog.ToPtr(0.01), "orange"), step(cog.ToPtr(0.05), colorBad)))).
		WithPanel(errorRatioStat("Client errors (4xx)", "4..",
			"Share of responses with a 4xx status. These leave spans unset, but are still worth watching.",
			thresholds(step(nil, colorGood), step(cog.ToPtr(0.1), colorWarn), step(cog.ToPtr(0.25), "orange")))).
		WithPanel(latencyStat()).
		WithPanel(requestsByRoute()).
		WithPanel(latency()).
		WithPanel(errorsByProblemType()).
		WithPanel(responsesByStatus()).
		WithRow(dashboard.NewRowBuilder("Demo work (basement registry)")).
		WithPanel(itemsProcessed()).
		WithPanel(workDuration()).
		WithRow(dashboard.NewRowBuilder("Go runtime")).
		WithPanel(memoryInUse()).
		WithPanel(goroutines()).
		WithPanel(allocationRate()).
		WithRow(dashboard.NewRowBuilder("Traces & logs")).
		WithPanel(tracesTable("Recent server traces", `{ resource.service.name =~ "$service" && kind = server }`).
			Description("Click a trace ID to open it in Tempo.").
			Span(24).Height(9)).
		WithPanel(logsPanel("Logs", `{service_name=~"$service"}`).
			Description("Expand a line and follow trace_id to jump to its trace.").
			Span(24).Height(12))
}

func serviceVariable() *dashboard.QueryVariableBuilder {
	query := "label_values(http_server_request_duration_seconds_count, job)"
	return dashboard.NewQueryVariableBuilder("service").
		Label("Service").
		Datasource(promDS).
		Query(dashboard.StringOrMap{Map: map[string]any{"query": query, "refId": "service"}}).
		Definition(query).
		Current(dashboard.VariableOption{
			Text:  dashboard.StringOrArrayOfString{String: cog.ToPtr("basement")},
			Value: dashboard.StringOrArrayOfString{String: cog.ToPtr("basement")},
		}).
		Refresh(dashboard.VariableRefreshOnTimeRangeChanged).
		Sort(dashboard.VariableSortAlphabeticalAsc).
		IncludeAll(false).
		Multi(false)
}

func routeVariable() *dashboard.QueryVariableBuilder {
	query := `label_values(http_server_request_duration_seconds_count{job=~"$service"}, http_route)`
	return dashboard.NewQueryVariableBuilder("route").
		Label("Route").
		Datasource(promDS).
		Query(dashboard.StringOrMap{Map: map[string]any{"query": query, "refId": "route"}}).
		Definition(query).
		Current(dashboard.VariableOption{
			Text:  dashboard.StringOrArrayOfString{ArrayOfString: []string{"All"}},
			Value: dashboard.StringOrArrayOfString{ArrayOfString: []string{"$__all"}},
		}).
		Refresh(dashboard.VariableRefreshOnTimeRangeChanged).
		Sort(dashboard.VariableSortAlphabeticalAsc).
		IncludeAll(true).
		AllValue(".*").
		Multi(true)
}

// --- Overview ----------------------------------------------------------------

func requestRateStat() cog.Builder[dashboard.Panel] {
	return statPanel("Requests / s", "reqps").
		Description("Rate of requests handled by traced routes (probes excluded).").
		Thresholds(thresholds(step(nil, "blue"))).
		WithTarget(prom(
			`sum(rate(http_server_request_duration_seconds_count{`+byServiceRoute+`}[$__rate_interval]))`,
			"Requests / s")).
		Span(6).Height(4)
}

// errorRatioStat is the share of responses whose status matches class, a
// regex such as "5..". The "or vector(0)" keeps the ratio at 0 rather than
// empty while no response of that class has been seen.
func errorRatioStat(title, class, description string, t *dashboard.ThresholdsConfigBuilder) cog.Builder[dashboard.Panel] {
	return statPanel(title, "percentunit").
		Description(description).
		Decimals(1).
		Thresholds(t).
		WithTarget(prom(
			`(sum(rate(http_server_request_duration_seconds_count{`+byServiceRoute+`, http_response_status_code=~"`+class+`"}[$__rate_interval])) or vector(0))`+
				` / sum(rate(http_server_request_duration_seconds_count{`+byServiceRoute+`}[$__rate_interval]))`,
			title)).
		Span(6).Height(4)
}

func latencyStat() cog.Builder[dashboard.Panel] {
	return statPanel("Latency p95", "s").
		Decimals(2).
		Thresholds(thresholds(step(nil, colorGood), step(cog.ToPtr(0.25), "orange"), step(cog.ToPtr(1.0), colorBad))).
		WithTarget(prom(
			`histogram_quantile(0.95, sum by (le) (rate(http_server_request_duration_seconds_bucket{`+byServiceRoute+`}[$__rate_interval])))`,
			"Latency p95")).
		Span(6).Height(4)
}

func requestsByRoute() cog.Builder[dashboard.Panel] {
	return timeseriesPanel("Requests by route", "reqps", true).
		WithTarget(prom(
			`sum by (http_route) (rate(http_server_request_duration_seconds_count{`+byServiceRoute+`}[$__rate_interval]))`,
			"{{http_route}}")).
		Span(12).Height(8)
}

func latency() cog.Builder[dashboard.Panel] {
	quantile := func(q, legend, ref string) *promQuery {
		return withExemplars(
			`histogram_quantile(`+q+`, sum by (le) (rate(http_server_request_duration_seconds_bucket{`+byServiceRoute+`}[$__rate_interval])))`,
			legend).RefId(ref)
	}
	return timeseriesPanel("Latency", "s", false).
		Description("Exemplar dots link to the trace.").
		Legend(common.NewVizLegendOptionsBuilder().
			DisplayMode(common.LegendDisplayModeTable).
			Placement(common.LegendPlacementRight).
			ShowLegend(true).
			Calcs([]string{"mean", "max"})).
		WithTarget(quantile("0.5", "p50", "A")).
		WithTarget(quantile("0.95", "p95", "B")).
		WithTarget(quantile("0.99", "p99", "C")).
		OverrideByName("p50", []dashboard.DynamicConfigValue{fixedColor(colorGood)}).
		OverrideByName("p95", []dashboard.DynamicConfigValue{fixedColor(colorWarn)}).
		OverrideByName("p99", []dashboard.DynamicConfigValue{fixedColor(colorBad)}).
		Span(12).Height(8)
}

func errorsByProblemType() cog.Builder[dashboard.Panel] {
	return timeseriesPanel("Errors by problem type", "reqps", true).
		Description(`servicepack.http.server.problems by RFC 9457 problem type; "unclassified" means the handler bypassed it.`).
		WithTarget(prom(
			`sum by (problem_type, http_response_status_code) (rate(servicepack_http_server_problems_total{`+byServiceRoute+`}[$__rate_interval]))`,
			"{{http_response_status_code}} {{problem_type}}")).
		Span(12).Height(8)
}

func responsesByStatus() cog.Builder[dashboard.Panel] {
	return timeseriesPanel("Responses by status", "reqps", true).
		WithTarget(prom(
			`sum by (http_response_status_code) (rate(http_server_request_duration_seconds_count{`+byServiceRoute+`}[$__rate_interval]))`,
			"{{http_response_status_code}}")).
		OverrideByRegexp("2..", []dashboard.DynamicConfigValue{fixedColor(colorGood)}).
		OverrideByRegexp("4..", []dashboard.DynamicConfigValue{fixedColor(colorWarn)}).
		OverrideByRegexp("5..", []dashboard.DynamicConfigValue{fixedColor(colorBad)}).
		Span(12).Height(8)
}

// --- Demo work -----------------------------------------------------------------

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

func workDuration() cog.Builder[dashboard.Panel] {
	quantile := func(q, legend, ref string) *promQuery {
		return withExemplars(
			`histogram_quantile(`+q+`, sum by (le) (rate(basement_demo_work_duration_seconds_bucket{`+byService+`}[$__rate_interval])))`,
			legend).RefId(ref)
	}
	return timeseriesPanel("Work duration", "s", false).
		Description("basement.demo.work.duration, defined in basement's model/.").
		WithTarget(quantile("0.5", "p50", "A")).
		WithTarget(quantile("0.95", "p95", "B")).
		OverrideByName("p50", []dashboard.DynamicConfigValue{fixedColor(colorGood)}).
		OverrideByName("p95", []dashboard.DynamicConfigValue{fixedColor(colorWarn)}).
		Span(12).Height(7)
}

// --- Go runtime ------------------------------------------------------------------

func memoryInUse() cog.Builder[dashboard.Panel] {
	return timeseriesPanel("Memory in use", "bytes", false).
		WithTarget(prom(`sum by (go_memory_type) (go_memory_used_bytes{`+byService+`})`, "{{go_memory_type}}").RefId("A")).
		WithTarget(prom(`sum(go_memory_gc_goal_bytes{`+byService+`})`, "GC goal").RefId("B")).
		// The GC goal is a target, not a quantity in use: a dashed line with no fill.
		OverrideByName("GC goal", []dashboard.DynamicConfigValue{
			{Id: "custom.fillOpacity", Value: 0},
			{Id: "custom.lineStyle", Value: map[string]any{"fill": "dash", "dash": []int{6, 4}}},
			fixedColor("text"),
		}).
		Span(8).Height(7)
}

func goroutines() cog.Builder[dashboard.Panel] {
	return timeseriesPanel("Goroutines", "short", false).
		WithTarget(prom(`sum(go_goroutine_count{`+byService+`})`, "goroutines")).
		Span(8).Height(7)
}

func allocationRate() cog.Builder[dashboard.Panel] {
	return timeseriesPanel("Allocation rate", "Bps", false).
		WithTarget(prom(`sum(rate(go_memory_allocated_bytes_total{`+byService+`}[$__rate_interval]))`, "allocated")).
		Span(8).Height(7)
}
