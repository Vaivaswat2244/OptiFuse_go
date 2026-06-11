package algo_test

import (
	"testing"

	"github.com/Vaivaswat2244/OptiFuse_go/services/optimizer/internal/algo"
	"github.com/Vaivaswat2244/OptiFuse_go/services/optimizer/internal/domain"
)

// buildImageProcessingApp constructs the exact application from the Python notebook.
// Spec: image_processing_baseline
//
//	upload(256MB, 100ms) → resize(512MB, 300ms) → watermark(256MB, 150ms) → store(128MB, 80ms)
//	upload → filter(512MB, 250ms) → optimize(512MB, 200ms) → store
//
// Constraints: max_memory=1024MB, max_latency=700ms, network_hop=10ms
func buildImageProcessingApp() *domain.Application {
	upload := &domain.LambdaFunction{ID: "upload", Name: "Upload", MemoryMB: 256, TimeoutSec: 1, LoadFactor: 1.0, DataOutBytes: make(map[string]int64)}
	resize := &domain.LambdaFunction{ID: "resize", Name: "Resize", MemoryMB: 512, TimeoutSec: 1, LoadFactor: 1.0, DataOutBytes: make(map[string]int64)}
	filter := &domain.LambdaFunction{ID: "filter", Name: "Filter", MemoryMB: 512, TimeoutSec: 1, LoadFactor: 1.0, DataOutBytes: make(map[string]int64)}
	watermark := &domain.LambdaFunction{ID: "watermark", Name: "Watermark", MemoryMB: 256, TimeoutSec: 1, LoadFactor: 1.0, DataOutBytes: make(map[string]int64)}
	optimize := &domain.LambdaFunction{ID: "optimize", Name: "Optimize", MemoryMB: 512, TimeoutSec: 1, LoadFactor: 1.0, DataOutBytes: make(map[string]int64)}
	store := &domain.LambdaFunction{ID: "store", Name: "Store", MemoryMB: 128, TimeoutSec: 1, LoadFactor: 1.0, DataOutBytes: make(map[string]int64)}

	// We set AvgDurationMs directly so RuntimeMs() uses these, matching the Python rt values.
	upload.AvgDurationMs = 100
	resize.AvgDurationMs = 300
	filter.AvgDurationMs = 250
	watermark.AvgDurationMs = 150
	optimize.AvgDurationMs = 200
	store.AvgDurationMs = 80

	upload.AddChild(resize, 5242880)
	upload.AddChild(filter, 5242880)
	resize.AddChild(watermark, 2097152)
	filter.AddChild(optimize, 3145728)
	watermark.AddChild(store, 2097152)
	optimize.AddChild(store, 1048576)

	return &domain.Application{
		Name:            "Image Processing",
		Functions:       []*domain.LambdaFunction{upload, resize, filter, watermark, optimize, store},
		CriticalPathIDs: []string{"upload", "resize", "watermark", "store"},
		MaxMemoryMB:     1024,
		MaxLatencyMS:    700,
		NetworkHopMS:    10,
	}
}

// ── NoFusion ──────────────────────────────────────────────────────────────────

func TestNoFusion_GroupCount(t *testing.T) {
	app := buildImageProcessingApp()
	result := (&algo.NoFusion{}).Optimize(app)
	if len(result.Groups) != 6 {
		t.Errorf("NoFusion: expected 6 groups (one per function), got %d", len(result.Groups))
	}
}

func TestNoFusion_EachGroupHasOneFunction(t *testing.T) {
	app := buildImageProcessingApp()
	result := (&algo.NoFusion{}).Optimize(app)
	for i, g := range result.Groups {
		if len(g) != 1 {
			t.Errorf("NoFusion: group %d should have 1 function, has %d", i, len(g))
		}
	}
}

func TestNoFusion_Feasible(t *testing.T) {
	app := buildImageProcessingApp()
	result := (&algo.NoFusion{}).Optimize(app)
	// NoFusion: each function is its own group, all under 1024MB → memory feasible.
	// Critical path latency: 100+300+150+80 = 630ms + 3 hops * 10ms = 660ms ≤ 700ms → feasible.
	if !result.Metrics.Feasible {
		t.Errorf("NoFusion: expected feasible, got infeasible (latency=%.0fms)", result.Metrics.LatencyMS)
	}
}

// ── Singleton ─────────────────────────────────────────────────────────────────

func TestSingleton_OneGroup(t *testing.T) {
	app := buildImageProcessingApp()
	result := (&algo.Singleton{}).Optimize(app)
	if len(result.Groups) != 1 {
		t.Errorf("Singleton: expected 1 group, got %d", len(result.Groups))
	}
}

func TestSingleton_AllFunctionsPresent(t *testing.T) {
	app := buildImageProcessingApp()
	result := (&algo.Singleton{}).Optimize(app)
	if len(result.Groups[0]) != 6 {
		t.Errorf("Singleton: expected all 6 functions in the group, got %d", len(result.Groups[0]))
	}
}

func TestSingleton_Infeasible(t *testing.T) {
	app := buildImageProcessingApp()
	result := (&algo.Singleton{}).Optimize(app)
	// Total memory: 256+512+512+256+512+128 = 2176MB > 1024MB → infeasible.
	if result.Metrics.Feasible {
		t.Errorf("Singleton: expected infeasible (total memory 2176MB > 1024MB limit)")
	}
}

func TestSingleton_FirstNodeIsRoot(t *testing.T) {
	app := buildImageProcessingApp()
	result := (&algo.Singleton{}).Optimize(app)
	if result.Groups[0][0].ID != "upload" {
		t.Errorf("Singleton: expected first node to be 'upload' (root), got %q", result.Groups[0][0].ID)
	}
}

// ── MinWCut ───────────────────────────────────────────────────────────────────

func TestMinWCut_MemoryConstraint(t *testing.T) {
	app := buildImageProcessingApp()
	result := (&algo.MinWCut{}).Optimize(app)
	for i, g := range result.Groups {
		mem := domain.GroupMemory(g)
		if mem > app.MaxMemoryMB {
			t.Errorf("MinWCut: group %d has %dMB, exceeds limit %dMB", i, mem, app.MaxMemoryMB)
		}
	}
}

func TestMinWCut_AllFunctionsAssigned(t *testing.T) {
	app := buildImageProcessingApp()
	result := (&algo.MinWCut{}).Optimize(app)
	total := 0
	for _, g := range result.Groups {
		total += len(g)
	}
	if total != 6 {
		t.Errorf("MinWCut: expected 6 total functions across all groups, got %d", total)
	}
}

// ── GreedyTP ──────────────────────────────────────────────────────────────────

func TestGreedyTP_MemoryConstraint(t *testing.T) {
	app := buildImageProcessingApp()
	result := (&algo.GreedyTP{}).Optimize(app)
	if result.Error != "" {
		t.Fatalf("GreedyTP returned error: %s", result.Error)
	}
	for i, g := range result.Groups {
		mem := domain.GroupMemory(g)
		if mem > app.MaxMemoryMB {
			t.Errorf("GreedyTP: group %d has %dMB, exceeds limit %dMB", i, mem, app.MaxMemoryMB)
		}
	}
}

func TestGreedyTP_LatencyConstraint(t *testing.T) {
	app := buildImageProcessingApp()
	result := (&algo.GreedyTP{}).Optimize(app)
	if result.Error != "" {
		t.Fatalf("GreedyTP returned error: %s", result.Error)
	}
	if result.Metrics.LatencyMS > float64(app.MaxLatencyMS) {
		t.Errorf("GreedyTP: latency %.0fms exceeds max %dms", result.Metrics.LatencyMS, app.MaxLatencyMS)
	}
}

func TestGreedyTP_AllFunctionsAssigned(t *testing.T) {
	app := buildImageProcessingApp()
	result := (&algo.GreedyTP{}).Optimize(app)
	total := 0
	for _, g := range result.Groups {
		total += len(g)
	}
	if total != 6 {
		t.Errorf("GreedyTP: expected 6 total functions, got %d", total)
	}
}

// ── CostlessCSP ───────────────────────────────────────────────────────────────

func TestCostlessCSP_MemoryConstraint(t *testing.T) {
	app := buildImageProcessingApp()
	result := (&algo.CostlessCSP{}).Optimize(app)
	if result.Error != "" {
		t.Fatalf("CostlessCSP returned error: %s", result.Error)
	}
	for i, g := range result.Groups {
		mem := domain.GroupMemory(g)
		if mem > app.MaxMemoryMB {
			t.Errorf("CostlessCSP: group %d has %dMB, exceeds limit %dMB", i, mem, app.MaxMemoryMB)
		}
	}
}

func TestCostlessCSP_LatencyConstraint(t *testing.T) {
	app := buildImageProcessingApp()
	result := (&algo.CostlessCSP{}).Optimize(app)
	if result.Error != "" {
		t.Fatalf("CostlessCSP returned error: %s", result.Error)
	}
	if result.Metrics.LatencyMS > float64(app.MaxLatencyMS) {
		t.Errorf("CostlessCSP: latency %.0fms exceeds max %dms", result.Metrics.LatencyMS, app.MaxLatencyMS)
	}
}

func TestCostlessCSP_AllFunctionsAssigned(t *testing.T) {
	app := buildImageProcessingApp()
	result := (&algo.CostlessCSP{}).Optimize(app)
	total := 0
	for _, g := range result.Groups {
		total += len(g)
	}
	if total != 6 {
		t.Errorf("CostlessCSP: expected 6 total functions, got %d", total)
	}
}

// ── Cost ordering: fused solutions should cost less than NoFusion ─────────────

func TestCostOrdering_FusionCheaperThanNoFusion(t *testing.T) {
	app := buildImageProcessingApp()
	noFusion := (&algo.NoFusion{}).Optimize(app)
	minWCut := (&algo.MinWCut{}).Optimize(app)
	greedyTP := (&algo.GreedyTP{}).Optimize(app)
	csp := (&algo.CostlessCSP{}).Optimize(app)

	for _, r := range []algo.AlgorithmResult{minWCut, greedyTP, csp} {
		if r.Error != "" {
			continue
		}
		if r.Metrics.TotalCostUSD >= noFusion.Metrics.TotalCostUSD {
			t.Errorf("%s: expected cost (%.8f) < NoFusion cost (%.8f)",
				r.Name, r.Metrics.TotalCostUSD, noFusion.Metrics.TotalCostUSD)
		}
	}
}
