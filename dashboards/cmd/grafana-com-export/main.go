package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/yuuki/rdma_exporter/dashboards"
)

func main() {
	root := "."
	if len(os.Args) > 1 {
		root = os.Args[1]
	}
	bundlePath := filepath.Join(root, "dashboards", "rdma_exporter_dashboard.json")
	outPath := filepath.Join(root, "dashboards", "rdma_exporter_dashboard.grafana.com.json")
	bundle, err := os.ReadFile(bundlePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "read bundle: %v\n", err)
		os.Exit(1)
	}
	out, err := dashboards.ExportForGrafanaCom(bundle)
	if err != nil {
		fmt.Fprintf(os.Stderr, "export: %v\n", err)
		os.Exit(1)
	}
	if err := os.WriteFile(outPath, out, 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "write export: %v\n", err)
		os.Exit(1)
	}
}
