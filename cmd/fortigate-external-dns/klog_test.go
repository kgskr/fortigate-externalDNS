package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"k8s.io/klog/v2"
)

func TestKlogRoutesThroughConfiguredSlogHandler(t *testing.T) {
	var out bytes.Buffer
	klog.SetSlogLogger(buildLogger(&out, "json", "info"))
	t.Cleanup(klog.ClearLogger)

	klog.InfoS("leader election message", "key", "value")
	klog.Flush()

	line := strings.TrimSpace(out.String())
	var record map[string]any
	if err := json.Unmarshal([]byte(line), &record); err != nil {
		t.Fatalf("klog output is not JSON (%v): %q", err, line)
	}
	if record["msg"] != "leader election message" || record["key"] != "value" {
		t.Fatalf("unexpected record: %#v", record)
	}
}
