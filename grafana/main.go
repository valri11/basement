// Command dashboards writes basement's Grafana dashboards as JSON.
//
// The dashboards are defined in Go with the Grafana Foundation SDK, and the
// JSON in dashboards/ is generated from them. Edit the Go, then run
// `task dashboards:generate`; `task dashboards:verify` fails when the
// committed JSON is out of date.
//
// This is a module of its own so that the SDK is not a dependency of the
// service.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/grafana/grafana-foundation-sdk/go/cog"
	"github.com/grafana/grafana-foundation-sdk/go/dashboard"
)

// dashboards maps each output file name to the builder that produces it.
var dashboards = map[string]func() *dashboard.DashboardBuilder{
	"basement.json": basementDashboard,
}

func main() {
	out := flag.String("out", "dashboards", "directory to write the dashboard JSON into")
	flag.Parse()

	for name, build := range dashboards {
		b, err := render(build())
		if err != nil {
			log.Fatalf("%s: %v", name, err)
		}
		path := filepath.Join(*out, name)
		if err := os.WriteFile(path, b, 0o644); err != nil {
			log.Fatal(err)
		}
		fmt.Println("wrote", path)
	}
}

// render builds a dashboard and marshals it the way it is committed: indented,
// with a trailing newline, so a regenerated file diffs cleanly.
func render(b *dashboard.DashboardBuilder) ([]byte, error) {
	d, err := b.Build()
	if err != nil {
		return nil, err
	}
	numberPanels(&d)
	linksAbovePanels(&d)
	out, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(out, '\n'), nil
}

// numberPanels gives every panel and row an id, in layout order. The SDK
// leaves them unset; Grafana would assign its own on load, but a fixed id keeps
// a panel's viewPanel link the same across regenerations.
func numberPanels(d *dashboard.Dashboard) {
	var next uint32
	id := func() uint32 { next++; return next }
	for _, p := range d.Panels {
		switch {
		case p.Panel != nil:
			p.Panel.Id = cog.ToPtr(id())
		case p.RowPanel != nil:
			p.RowPanel.Id = id()
			for i := range p.RowPanel.Panels {
				p.RowPanel.Panels[i].Id = cog.ToPtr(id())
			}
		}
	}
}

// linksAbovePanels clears each dashboard link's placement. The SDK defaults it
// to "inControlsMenu", which hides the link behind the controls menu; leaving
// it unset is what shows the link in the bar above the panels, and the SDK has
// no constant for that.
func linksAbovePanels(d *dashboard.Dashboard) {
	for i := range d.Links {
		d.Links[i].Placement = nil
	}
}
