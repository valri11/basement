package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// TestCommittedDashboardsAreGenerated fails when a file in dashboards/ differs
// from what the Go definitions produce: either the JSON was edited by hand, or
// the Go was changed without running `task dashboards:generate`.
func TestCommittedDashboardsAreGenerated(t *testing.T) {
	for name, build := range dashboards {
		t.Run(name, func(t *testing.T) {
			want, err := render(build())
			if err != nil {
				t.Fatal(err)
			}
			got, err := os.ReadFile(filepath.Join("dashboards", name))
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, want) {
				t.Errorf("dashboards/%s is out of date; run `task dashboards:generate`", name)
			}
		})
	}
}

// TestPanelIDsAreUnique guards numberPanels: Grafana keys panel state and
// viewPanel links on the id, so two panels sharing one would be confused.
func TestPanelIDsAreUnique(t *testing.T) {
	for name, build := range dashboards {
		d, err := build().Build()
		if err != nil {
			t.Fatal(err)
		}
		numberPanels(&d)
		seen := map[uint32]bool{}
		for _, p := range d.Panels {
			var ids []uint32
			switch {
			case p.Panel != nil:
				ids = append(ids, *p.Panel.Id)
			case p.RowPanel != nil:
				ids = append(ids, p.RowPanel.Id)
				for _, c := range p.RowPanel.Panels {
					ids = append(ids, *c.Id)
				}
			}
			for _, id := range ids {
				if seen[id] {
					t.Errorf("%s: panel id %d is used twice", name, id)
				}
				seen[id] = true
			}
		}
	}
}
