package parser_test

import (
	parser "_/home/vaivaswat/Documents/projects/optfuse_go/services/parser/internal/parser"
	"os"
	"testing"
)

func TestParse_ImageProcessor(t *testing.T) {
	yamlBytes, err := os.ReadFile("../../../examples/image-processor/serverless.yml")
	if err != nil {
		t.Fatalf("failed to read example YAML: %v", err)
	}

	graph, err := parser.Parse("image-processor", yamlBytes)
	if err != nil {
		t.Fatalf("Parse() error: %v", err)
	}

	// ── Function count ────────────────────────────────────────────────────────
	if len(graph.Functions) != 5 {
		t.Errorf("expected 5 functions, got %d", len(graph.Functions))
	}

	// ── Function IDs ──────────────────────────────────────────────────────────
	ids := make(map[string]bool)
	for _, f := range graph.Functions {
		ids[f.ID] = true
	}
	for _, expected := range []string{"upload", "resize", "filter", "watermark", "store"} {
		if !ids[expected] {
			t.Errorf("expected function '%s' not found", expected)
		}
	}

	// ── Memory values (from YAML) ─────────────────────────────────────────────
	memExpected := map[string]int{
		"upload":    256,
		"resize":    512,
		"filter":    512,
		"watermark": 256,
		"store":     128,
	}
	for _, f := range graph.Functions {
		if want, ok := memExpected[f.ID]; ok && f.MemoryMB != want {
			t.Errorf("function %s: expected memory %d, got %d", f.ID, want, f.MemoryMB)
		}
	}

	// ── Edges (DataOutBytes) ──────────────────────────────────────────────────
	funcMap := make(map[string]*parser.ParsedFunction)
	for _, f := range graph.Functions {
		funcMap[f.ID] = f
	}

	// upload → resize: 5 MiB
	if bytes := funcMap["upload"].DataOutBytes["resize"]; bytes != 5242880 {
		t.Errorf("upload→resize: expected 5242880 bytes, got %d", bytes)
	}
	// upload → filter: 5 MiB
	if bytes := funcMap["upload"].DataOutBytes["filter"]; bytes != 5242880 {
		t.Errorf("upload→filter: expected 5242880 bytes, got %d", bytes)
	}
	// watermark → store: 1 MiB
	if bytes := funcMap["watermark"].DataOutBytes["store"]; bytes != 1048576 {
		t.Errorf("watermark→store: expected 1048576 bytes, got %d", bytes)
	}

	// ── Critical path ─────────────────────────────────────────────────────────
	expectedCP := []string{"upload", "resize", "watermark", "store"}
	if len(graph.CriticalPath) != len(expectedCP) {
		t.Errorf("critical path length: expected %d, got %d", len(expectedCP), len(graph.CriticalPath))
	}
	for i, id := range expectedCP {
		if i < len(graph.CriticalPath) && graph.CriticalPath[i] != id {
			t.Errorf("critical path[%d]: expected '%s', got '%s'", i, id, graph.CriticalPath[i])
		}
	}

	// ── Constraints ───────────────────────────────────────────────────────────
	if graph.MaxMemoryMB != 1024 {
		t.Errorf("MaxMemoryMB: expected 1024, got %d", graph.MaxMemoryMB)
	}
	if graph.MaxLatencyMS != 5000 {
		t.Errorf("MaxLatencyMS: expected 5000, got %d", graph.MaxLatencyMS)
	}
	if graph.NetworkHopMS != 20 {
		t.Errorf("NetworkHopMS: expected 20, got %d", graph.NetworkHopMS)
	}
}

func TestParse_MissingFunctionsBlock(t *testing.T) {
	yaml := []byte(`
service: empty
provider:
  name: aws
`)
	_, err := parser.Parse("empty", yaml)
	if err == nil {
		t.Error("expected error for missing functions block, got nil")
	}
}

func TestParse_InvalidYAML(t *testing.T) {
	_, err := parser.Parse("bad", []byte("{ this is not valid yaml: ["))
	if err == nil {
		t.Error("expected error for invalid YAML, got nil")
	}
}

func TestParse_MissingTopologyWarning(t *testing.T) {
	yaml := []byte(`
service: no-topology
provider:
  name: aws
  memorySize: 512
  timeout: 30
functions:
  hello:
    handler: src/hello.handler
`)
	graph, err := parser.Parse("no-topology", yaml)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	hasWarning := false
	for _, w := range graph.Warnings {
		if len(w) > 0 {
			hasWarning = true
			break
		}
	}
	if !hasWarning {
		t.Error("expected a warning about missing topology block")
	}
}
