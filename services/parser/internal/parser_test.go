package parser_test

import (
	"testing"

	parser "github.com/Vaivaswat2244/OptiFuse_go/services/parser/internal"
)

// exampleYAML is the "image processing" application used throughout OptiFuse.
// services/optimizer/internal/algo/algo_test.go builds the same graph in Go, and
// optifuse-image-processing-test carries the same custom.optifuse block — keep
// the three in sync.
//
//	upload ─┬─► resize ────► watermark ─┐
//	        │                            ├─► store
//	        └─► filter ────► optimize ──┘
const exampleYAML = `
service: optifuse-image-processing

provider:
  name: aws
  runtime: nodejs18.x
  region: ap-south-1

functions:
  upload:
    handler: handler.upload
    memorySize: 256
  resize:
    handler: handler.resize
    memorySize: 512
  filter:
    handler: handler.filter
    memorySize: 512
  watermark:
    handler: handler.watermark
    memorySize: 256
  optimize:
    handler: handler.optimize
    memorySize: 512
  store:
    handler: handler.store
    memorySize: 128

custom:
  optifuse:
    topology:
      upload:
        children:
          resize: 5242880
          filter: 5242880
      resize:
        children:
          watermark: 2097152
      filter:
        children:
          optimize: 3145728
      watermark:
        children:
          store: 2097152
      optimize:
        children:
          store: 1048576
    criticalPath:
      - upload
      - resize
      - watermark
      - store
    functions:
      upload:    { avgDurationMs: 100 }
      resize:    { avgDurationMs: 300 }
      filter:    { avgDurationMs: 250 }
      watermark: { avgDurationMs: 150 }
      optimize:  { avgDurationMs: 200 }
      store:     { avgDurationMs: 80 }
    constraints:
      maxMemoryMB: 1024
      maxLatencyMS: 700
      networkHopMS: 10
`

// loadExample returns the shared example serverless.yml used across all tests.
func loadExample(t *testing.T) []byte {
	t.Helper()
	return []byte(exampleYAML)
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

func TestParse_ServiceName(t *testing.T) {
	// The repo name and the YAML `service:` field are different things; the
	// enricher builds CloudWatch log group names from the latter.
	graph, err := parser.Parse("image-processor", loadExample(t))
	if err != nil {
		t.Fatalf("Parse() error: %v", err)
	}
	if graph.ServiceName != "optifuse-image-processing" {
		t.Errorf("ServiceName: want %q, got %q", "optifuse-image-processing", graph.ServiceName)
	}
	if graph.Name != "image-processor" {
		t.Errorf("Name should stay the repo name, got %q", graph.Name)
	}
}

func TestParse_DurationEstimates(t *testing.T) {
	graph, err := parser.Parse("image-processor", loadExample(t))
	if err != nil {
		t.Fatalf("Parse() error: %v", err)
	}
	want := map[string]float64{
		"upload": 100, "resize": 300, "filter": 250,
		"watermark": 150, "optimize": 200, "store": 80,
	}
	for _, f := range graph.Functions {
		if got := f.AvgDurationMs; got != want[f.ID] {
			t.Errorf("%s: expected avgDurationMs %.0f, got %.0f", f.ID, want[f.ID], got)
		}
	}
}

func TestParse_DurationEstimatesOptional(t *testing.T) {
	// Omitting the estimates must leave AvgDurationMs at zero so the optimizer
	// falls back to the timeout, rather than producing a bogus runtime.
	yaml := []byte(`
service: no-estimates
provider:
  name: aws
  memorySize: 512
  timeout: 3
functions:
  hello:
    handler: src/hello.handler
`)
	graph, err := parser.Parse("no-estimates", yaml)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if graph.Functions[0].AvgDurationMs != 0 {
		t.Errorf("expected AvgDurationMs 0 when unspecified, got %.0f", graph.Functions[0].AvgDurationMs)
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
