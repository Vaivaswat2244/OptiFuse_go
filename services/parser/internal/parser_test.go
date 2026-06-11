package parser_test

import (
	"os"
	"testing"

	parser "github.com/Vaivaswat2244/OptiFuse_go/services/parser/internal"
)

// loadExample reads the shared example serverless.yml used across all tests.
func loadExample(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile("../../../examples/serverless.yml")
	if err != nil {
		t.Fatalf("could not read example YAML: %v", err)
	}
	return b
}

func TestParse_FunctionCount(t *testing.T) {
	graph, err := parser.Parse("image-processor", loadExample(t))
	if err != nil {
		t.Fatalf("Parse() error: %v", err)
	}
	if len(graph.Functions) != 6 {
		t.Errorf("expected 6 functions, got %d", len(graph.Functions))
	}
}

func TestParse_FunctionIDs(t *testing.T) {
	graph, err := parser.Parse("image-processor", loadExample(t))
	if err != nil {
		t.Fatalf("Parse() error: %v", err)
	}
	ids := make(map[string]bool)
	for _, f := range graph.Functions {
		ids[f.ID] = true
	}
	for _, want := range []string{"upload", "resize", "filter", "watermark", "optimize", "store"} {
		if !ids[want] {
			t.Errorf("missing function %q", want)
		}
	}
}

func TestParse_MemoryValues(t *testing.T) {
	graph, err := parser.Parse("image-processor", loadExample(t))
	if err != nil {
		t.Fatalf("Parse() error: %v", err)
	}
	want := map[string]int{
		"upload": 256, "resize": 512, "filter": 512,
		"watermark": 256, "optimize": 512, "store": 128,
	}
	fm := make(map[string]*parser.ParsedFunction)
	for _, f := range graph.Functions {
		fm[f.ID] = f
	}
	for id, mem := range want {
		if fm[id].MemoryMB != mem {
			t.Errorf("%s: expected memory %d, got %d", id, mem, fm[id].MemoryMB)
		}
	}
}

func TestParse_Edges(t *testing.T) {
	graph, err := parser.Parse("image-processor", loadExample(t))
	if err != nil {
		t.Fatalf("Parse() error: %v", err)
	}
	fm := make(map[string]*parser.ParsedFunction)
	for _, f := range graph.Functions {
		fm[f.ID] = f
	}

	edges := []struct {
		from  string
		to    string
		bytes int64
	}{
		{"upload", "resize", 5242880},
		{"upload", "filter", 5242880},
		{"resize", "watermark", 2097152},
		{"filter", "optimize", 3145728},
		{"watermark", "store", 2097152},
		{"optimize", "store", 1048576},
	}
	for _, e := range edges {
		got := fm[e.from].DataOutBytes[e.to]
		if got != e.bytes {
			t.Errorf("edge %s→%s: expected %d bytes, got %d", e.from, e.to, e.bytes, got)
		}
	}
}

func TestParse_CriticalPath(t *testing.T) {
	graph, err := parser.Parse("image-processor", loadExample(t))
	if err != nil {
		t.Fatalf("Parse() error: %v", err)
	}
	want := []string{"upload", "resize", "watermark", "store"}
	if len(graph.CriticalPath) != len(want) {
		t.Fatalf("critical path length: want %d, got %d", len(want), len(graph.CriticalPath))
	}
	for i, id := range want {
		if graph.CriticalPath[i] != id {
			t.Errorf("critical path[%d]: want %q, got %q", i, id, graph.CriticalPath[i])
		}
	}
}

func TestParse_Constraints(t *testing.T) {
	graph, err := parser.Parse("image-processor", loadExample(t))
	if err != nil {
		t.Fatalf("Parse() error: %v", err)
	}
	if graph.MaxMemoryMB != 1024 {
		t.Errorf("MaxMemoryMB: want 1024, got %d", graph.MaxMemoryMB)
	}
	if graph.MaxLatencyMS != 700 {
		t.Errorf("MaxLatencyMS: want 700, got %d", graph.MaxLatencyMS)
	}
	if graph.NetworkHopMS != 10 {
		t.Errorf("NetworkHopMS: want 10, got %d", graph.NetworkHopMS)
	}
}

func TestParse_MissingFunctions(t *testing.T) {
	_, err := parser.Parse("empty", []byte("service: x\nprovider:\n  name: aws\n"))
	if err == nil {
		t.Error("expected error for missing functions block")
	}
}

func TestParse_InvalidYAML(t *testing.T) {
	_, err := parser.Parse("bad", []byte("{not valid yaml: ["))
	if err == nil {
		t.Error("expected error for invalid YAML")
	}
}

func TestParse_MissingTopologyWarning(t *testing.T) {
	yaml := []byte(`
service: no-topo
provider:
  name: aws
  memorySize: 512
  timeout: 30
functions:
  hello:
    handler: src/hello.handler
`)
	graph, err := parser.Parse("no-topo", yaml)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(graph.Warnings) == 0 {
		t.Error("expected at least one warning about missing topology")
	}
}
