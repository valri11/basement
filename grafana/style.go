package main

import (
	"github.com/grafana/grafana-foundation-sdk/go/cog"
	"github.com/grafana/grafana-foundation-sdk/go/common"
	"github.com/grafana/grafana-foundation-sdk/go/dashboard"
	"github.com/grafana/grafana-foundation-sdk/go/logs"
	"github.com/grafana/grafana-foundation-sdk/go/loki"
	"github.com/grafana/grafana-foundation-sdk/go/prometheus"
	"github.com/grafana/grafana-foundation-sdk/go/stat"
	"github.com/grafana/grafana-foundation-sdk/go/table"
	"github.com/grafana/grafana-foundation-sdk/go/tempo"
	"github.com/grafana/grafana-foundation-sdk/go/timeseries"
)

// The datasource uids are the ones grafana/otel-lgtm provisions, and the ones
// the homelab cluster's Grafana uses too.
var (
	promDS  = datasource("prometheus", "prometheus")
	tempoDS = datasource("tempo", "tempo")
	lokiDS  = datasource("loki", "loki")
)

func datasource(typ, uid string) common.DataSourceRef {
	return common.DataSourceRef{Type: cog.ToPtr(typ), Uid: cog.ToPtr(uid)}
}

// Colours used for fixed series, so the same meaning keeps the same colour
// across panels: green for good or fast, yellow for the middle, red for bad.
const (
	colorGood = "green"
	colorWarn = "#EAB839"
	colorBad  = "red"
)

// promQuery is the Prometheus query builder, named for the helpers below.
type promQuery = prometheus.DataqueryBuilder

// prom is a range query with a legend.
func prom(expr, legend string) *promQuery {
	return prometheus.NewDataqueryBuilder().
		Datasource(promDS).
		Expr(expr).
		LegendFormat(legend).
		Range().
		RefId("A")
}

// withExemplars is prom with exemplars shown, for histograms whose samples
// carry a trace id.
func withExemplars(expr, legend string) *promQuery {
	return prom(expr, legend).Exemplar(true)
}

// step is one threshold. A nil value is the base step, which applies below
// every other one.
func step(value *float64, color string) dashboard.Threshold {
	return dashboard.Threshold{Value: value, Color: color}
}

func thresholds(steps ...dashboard.Threshold) *dashboard.ThresholdsConfigBuilder {
	return dashboard.NewThresholdsConfigBuilder().
		Mode(dashboard.ThresholdsModeAbsolute).
		Steps(steps)
}

// statPanel is a single number with a sparkline, coloured by its thresholds.
func statPanel(title, unit string) *stat.PanelBuilder {
	return stat.NewPanelBuilder().
		Title(title).
		Datasource(promDS).
		Unit(unit).
		NoValue("0").
		ColorScheme(dashboard.NewFieldColorBuilder().Mode(dashboard.FieldColorModeIdThresholds)).
		ReduceOptions(common.NewReduceDataOptionsBuilder().Calcs([]string{"lastNotNull"}).Values(false)).
		ColorMode(common.BigValueColorModeValue).
		GraphMode(common.BigValueGraphModeArea).
		JustifyMode(common.BigValueJustifyModeCenter).
		TextMode(common.BigValueTextModeValue).
		WideLayout(true)
}

// timeseriesPanel is the line style every graph here shares. stacked stacks
// the series, which suits rates that add up to a total; latencies and gauges
// are not stacked.
func timeseriesPanel(title, unit string, stacked bool) *timeseries.PanelBuilder {
	stacking := common.StackingModeNone
	if stacked {
		stacking = common.StackingModeNormal
	}
	return timeseries.NewPanelBuilder().
		Title(title).
		Datasource(promDS).
		Unit(unit).
		ColorScheme(dashboard.NewFieldColorBuilder().Mode(dashboard.FieldColorModeIdPaletteClassic)).
		DrawStyle(common.GraphDrawStyleLine).
		LineInterpolation(common.LineInterpolationSmooth).
		LineWidth(2).
		FillOpacity(18).
		GradientMode(common.GraphGradientModeOpacity).
		ShowPoints(common.VisibilityModeNever).
		SpanNulls(common.BoolOrFloat64{Bool: cog.ToPtr(true)}).
		AxisBorderShow(false).
		AxisSoftMin(0).
		Stacking(common.NewStackingConfigBuilder().Mode(stacking).Group("A")).
		Legend(common.NewVizLegendOptionsBuilder().
			DisplayMode(common.LegendDisplayModeList).
			Placement(common.LegendPlacementBottom).
			ShowLegend(true).
			Calcs([]string{})).
		Tooltip(common.NewVizTooltipOptionsBuilder().
			Mode(common.TooltipDisplayModeMulti).
			Sort(common.SortOrderDescending))
}

// fixedColor is an override property that pins a series to one colour.
func fixedColor(color string) dashboard.DynamicConfigValue {
	return dashboard.DynamicConfigValue{
		Id:    "color",
		Value: map[string]any{"mode": "fixed", "fixedColor": color},
	}
}

// tracesTable lists traces from a TraceQL query, newest first.
func tracesTable(title, query string) *table.PanelBuilder {
	return table.NewPanelBuilder().
		Title(title).
		Datasource(tempoDS).
		WithTarget(tempo.NewDataqueryBuilder().
			Datasource(tempoDS).
			QueryType(string(tempo.TempoQueryTypeTraceql)).
			Query(query).
			Limit(20).
			TableType(tempo.SearchTableTypeTraces).
			RefId("A")).
		Align(common.FieldTextAlignmentAuto).
		CellOptions(common.TableCellOptions{TableAutoCellOptions: &common.TableAutoCellOptions{Type: "auto"}}).
		ShowHeader(true).
		CellHeight(common.TableCellHeightSm).
		SortBy([]cog.Builder[common.TableSortByFieldState]{
			common.NewTableSortByFieldStateBuilder().DisplayName("Start time").Desc(true),
		}).
		// Duration as a bar, so a slow trace stands out in the list.
		OverrideByName("Duration", []dashboard.DynamicConfigValue{
			{Id: "unit", Value: "ms"},
			{Id: "custom.cellOptions", Value: map[string]any{"type": "gauge", "mode": "lcd"}},
			{Id: "color", Value: map[string]any{"mode": "continuous-GrYlRd"}},
			{Id: "max", Value: 1000},
		})
}

// logsPanel shows log lines newest first, with details expandable so a line's
// trace_id can be followed to its trace.
func logsPanel(title, expr string) *logs.PanelBuilder {
	return logs.NewPanelBuilder().
		Title(title).
		Datasource(lokiDS).
		WithTarget(loki.NewDataqueryBuilder().
			Datasource(lokiDS).
			Expr(expr).
			QueryType("range").
			RefId("A")).
		ShowTime(true).
		WrapLogMessage(true).
		PrettifyLogMessage(false).
		EnableLogDetails(true).
		DedupStrategy(common.LogsDedupStrategyNone).
		SortOrder(common.LogsSortOrderDescending)
}
