package dashboards

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
)

const (
	GrafanaComID    = 24241
	grafanaComTitle = "RDMA / RoCE NIC Telemetry"
)

var panelRequireMeta = map[string]struct{ ID, Name string }{
	"stat":       {ID: "stat", Name: "Stat"},
	"table":      {ID: "table", Name: "Table"},
	"timeseries": {ID: "timeseries", Name: "Time series"},
}

// ExportForGrafanaCom wraps the provisioning dashboard in Grafana.com
// "Export for sharing externally" form so revision uploads keep gnetId 24241.
func ExportForGrafanaCom(bundle []byte) ([]byte, error) {
	var dash map[string]any
	if err := json.Unmarshal(bundle, &dash); err != nil {
		return nil, fmt.Errorf("parse bundle: %w", err)
	}
	replaceDatasource(dash)
	dash["title"] = grafanaComTitle
	dash["gnetId"] = GrafanaComID
	dash["__inputs"] = []map[string]any{{
		"name":        "DS_PROMETHEUS",
		"label":       "prometheus",
		"description": "",
		"type":        "datasource",
		"pluginId":    "prometheus",
		"pluginName":  "Prometheus",
	}}
	dash["__elements"] = map[string]any{}
	dash["__requires"] = grafanaComRequires(dash)

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(dash); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func grafanaComRequires(dash map[string]any) []map[string]string {
	requires := []map[string]string{
		{"type": "grafana", "id": "grafana", "name": "Grafana", "version": "10.4.0"},
		{"type": "datasource", "id": "prometheus", "name": "Prometheus", "version": "1.0.0"},
	}
	seen := map[string]struct{}{}
	panels, _ := dash["panels"].([]any)
	for _, item := range panels {
		p, _ := item.(map[string]any)
		typ, _ := p["type"].(string)
		meta, ok := panelRequireMeta[typ]
		if !ok {
			continue
		}
		if _, dup := seen[meta.ID]; dup {
			continue
		}
		seen[meta.ID] = struct{}{}
		requires = append(requires, map[string]string{
			"type":    "panel",
			"id":      meta.ID,
			"name":    meta.Name,
			"version": "1.0.0",
		})
	}
	sort.SliceStable(requires[2:], func(i, j int) bool {
		return requires[i+2]["id"] < requires[j+2]["id"]
	})
	return requires
}

func replaceDatasource(v any) {
	switch x := v.(type) {
	case map[string]any:
		if ds, ok := x["datasource"].(string); ok && ds == "$ds" {
			x["datasource"] = map[string]any{
				"type": "prometheus",
				"uid":  "${DS_PROMETHEUS}",
			}
		}
		for _, child := range x {
			replaceDatasource(child)
		}
	case []any:
		for _, child := range x {
			replaceDatasource(child)
		}
	}
}
