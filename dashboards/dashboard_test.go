package dashboards

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"
)

const (
	bundlePath = "rdma_exporter_dashboard.json"
	gcomPath   = "rdma_exporter_dashboard.grafana.com.json"
	wantUID    = "rdma-exporter"
	wantVer    = 4
)

func TestBundledDashboardContract(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile(bundlePath)
	if err != nil {
		t.Fatalf("read bundle: %v", err)
	}
	var dash map[string]any
	if err := json.Unmarshal(raw, &dash); err != nil {
		t.Fatalf("parse bundle: %v", err)
	}
	if _, ok := dash["__inputs"]; ok {
		t.Fatal("bundled JSON must stay in provisioning form (no __inputs)")
	}
	if dash["uid"] != wantUID {
		t.Fatalf("uid = %v, want %s", dash["uid"], wantUID)
	}
	if versionOf(dash) != wantVer {
		t.Fatalf("version = %v, want %d", dash["version"], wantVer)
	}

	panels := panelList(t, dash)
	ids := map[int]string{}
	titles := map[string]map[string]any{}
	for _, p := range panels {
		id := intOf(p["id"])
		title, _ := p["title"].(string)
		if prev, ok := ids[id]; ok {
			t.Fatalf("duplicate panel id %d (%q and %q)", id, prev, title)
		}
		ids[id] = title
		titles[title] = p
	}

	wantTitles := []string{
		"Congestion: Optional CC [events/s] / port",
		"Congestion: Optional CC enabled / port",
		"Optional: port traffic [B/s] / port",
		"Optional: port traffic [pkt/s] / port",
	}
	for _, title := range wantTitles {
		if _, ok := titles[title]; !ok {
			t.Errorf("missing panel %q", title)
		}
	}

	mustExpr(t, titles["Congestion: Optional CC [events/s] / port"],
		"rate(rdma_cc_rx_ce_pkts_total{",
		"rate(rdma_cc_rx_cnp_pkts_total{",
		"rate(rdma_cc_tx_cnp_pkts_total{",
	)
	mustUnit(t, titles["Congestion: Optional CC [events/s] / port"], "pps")
	mustExpr(t, titles["Congestion: Optional CC enabled / port"],
		`rdma_optional_counter_enabled{`,
		`^cc_.*`,
	)
	forbidExpr(t, titles["Congestion: Optional CC enabled / port"], "rate(")
	mustUnit(t, titles["Congestion: Optional CC enabled / port"], "none")
	mustExpr(t, titles["Optional: port traffic [B/s] / port"],
		"rate(rdma_optional_rx_bytes_total{",
		"rate(rdma_optional_tx_bytes_total{",
	)
	forbidExpr(t, titles["Optional: port traffic [B/s] / port"], "4 *", "4*", "qp_type")
	mustUnit(t, titles["Optional: port traffic [B/s] / port"], "Bps")
	mustExpr(t, titles["Optional: port traffic [pkt/s] / port"],
		"rate(rdma_optional_rx_packets_total{",
		"rate(rdma_optional_tx_packets_total{",
	)
	forbidExpr(t, titles["Optional: port traffic [pkt/s] / port"], "4 *", "4*", "qp_type")
	mustUnit(t, titles["Optional: port traffic [pkt/s] / port"], "pps")
}

func TestGrafanaComExportMatchesBundle(t *testing.T) {
	t.Parallel()

	bundle, err := os.ReadFile(bundlePath)
	if err != nil {
		t.Fatalf("read bundle: %v", err)
	}
	got, err := os.ReadFile(gcomPath)
	if err != nil {
		t.Fatalf("read grafana.com export: %v", err)
	}
	want, err := ExportForGrafanaCom(bundle)
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("%s is stale; regenerate with go run ./dashboards/cmd/grafana-com-export", gcomPath)
	}

	var dash map[string]any
	if err := json.Unmarshal(got, &dash); err != nil {
		t.Fatalf("parse grafana.com export: %v", err)
	}
	if versionOf(dash) != wantVer {
		t.Fatalf("grafana.com version = %v, want %d", dash["version"], wantVer)
	}
	if intOf(dash["gnetId"]) != GrafanaComID {
		t.Fatalf("gnetId = %v, want %d", dash["gnetId"], GrafanaComID)
	}
	if dash["uid"] != wantUID {
		t.Fatalf("grafana.com uid = %v, want %s", dash["uid"], wantUID)
	}
	if dash["title"] != "RDMA / RoCE NIC Telemetry" {
		t.Fatalf("grafana.com title = %v, want listing title", dash["title"])
	}
	inputs, _ := dash["__inputs"].([]any)
	if len(inputs) != 1 {
		t.Fatalf("__inputs len = %d, want 1", len(inputs))
	}
	if leftover := leftoverDollarDS(dash); leftover > 0 {
		t.Fatalf("grafana.com export still has %d datasource=$ds fields", leftover)
	}
	if !bytes.Contains(got, []byte(`${DS_PROMETHEUS}`)) {
		t.Fatal("grafana.com export missing ${DS_PROMETHEUS}")
	}
	if bytes.Contains(got, []byte(`\u0026`)) || bytes.Contains(got, []byte(`\u003c`)) {
		t.Fatal("grafana.com export still HTML-escapes JSON strings")
	}
	if grafanaRequireVersion(dash) != "10.4.0" {
		t.Fatalf("grafana __requires version = %q, want 10.4.0", grafanaRequireVersion(dash))
	}
}

func panelList(t *testing.T, dash map[string]any) []map[string]any {
	t.Helper()
	raw, ok := dash["panels"].([]any)
	if !ok {
		t.Fatal("panels missing")
	}
	out := make([]map[string]any, 0, len(raw))
	for _, item := range raw {
		p, ok := item.(map[string]any)
		if !ok {
			t.Fatalf("panel is %T, want object", item)
		}
		out = append(out, p)
	}
	return out
}

func mustExpr(t *testing.T, panel map[string]any, needles ...string) {
	t.Helper()
	if panel == nil {
		return
	}
	blob := targetBlob(t, panel)
	for _, needle := range needles {
		if !bytes.Contains(blob, []byte(needle)) {
			t.Errorf("panel %q missing expr fragment %q", panel["title"], needle)
		}
	}
}

func forbidExpr(t *testing.T, panel map[string]any, needles ...string) {
	t.Helper()
	if panel == nil {
		return
	}
	blob := targetBlob(t, panel)
	for _, needle := range needles {
		if bytes.Contains(blob, []byte(needle)) {
			t.Errorf("panel %q must not contain %q", panel["title"], needle)
		}
	}
}

func mustUnit(t *testing.T, panel map[string]any, want string) {
	t.Helper()
	if panel == nil {
		return
	}
	fieldConfig, _ := panel["fieldConfig"].(map[string]any)
	defaults, _ := fieldConfig["defaults"].(map[string]any)
	if got, _ := defaults["unit"].(string); got != want {
		t.Errorf("panel %q unit = %q, want %q", panel["title"], got, want)
	}
}

func targetBlob(t *testing.T, panel map[string]any) []byte {
	t.Helper()
	blob, err := json.Marshal(panel["targets"])
	if err != nil {
		t.Fatal(err)
	}
	return blob
}

func leftoverDollarDS(v any) int {
	switch x := v.(type) {
	case map[string]any:
		n := 0
		if ds, ok := x["datasource"].(string); ok && ds == "$ds" {
			n++
		}
		for _, child := range x {
			n += leftoverDollarDS(child)
		}
		return n
	case []any:
		n := 0
		for _, child := range x {
			n += leftoverDollarDS(child)
		}
		return n
	default:
		return 0
	}
}

func grafanaRequireVersion(dash map[string]any) string {
	requires, _ := dash["__requires"].([]any)
	for _, item := range requires {
		req, _ := item.(map[string]any)
		if req["type"] == "grafana" {
			s, _ := req["version"].(string)
			return s
		}
	}
	return ""
}

func versionOf(dash map[string]any) int {
	return intOf(dash["version"])
}

func intOf(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	default:
		return -1
	}
}
