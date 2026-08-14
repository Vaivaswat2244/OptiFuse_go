package algo_test

import (
	"sort"
	"strings"
	"testing"

	"github.com/Vaivaswat2244/OptiFuse_go/services/optimizer/internal/algo"
	"github.com/Vaivaswat2244/OptiFuse_go/services/optimizer/internal/domain"
)

// canonicalPartition renders a partition as a stable string so two runs can be
// compared regardless of group or member ordering.
func canonicalPartition(groups [][]*domain.LambdaFunction) string {
	rendered := make([]string, 0, len(groups))
	for _, g := range groups {
		ids := make([]string, len(g))
		for i, f := range g {
			ids[i] = f.ID
		}
		sort.Strings(ids)
		rendered = append(rendered, strings.Join(ids, "+"))
	}
	sort.Strings(rendered)
	return strings.Join(rendered, ",")
}

// buildEcommerceApp mirrors the optifuse_lambda_test repo: a fulfilment chain
// with one parallel branch, every function at the 512MB default.
//
//	orderPlaced → processPayment
//	orderPlaced → updateInventory → prepareShipping → notifyCustomer → logCompletion
//
// The uniform memory matters: a 1024MB cap allows at most two functions per
// group, which forces a real partitioning decision.
func buildEcommerceApp() *domain.Application {
	newFn := func(id string, durationMs float64) *domain.LambdaFunction {
		return &domain.LambdaFunction{
			ID: id, Name: id, MemoryMB: 512, TimeoutSec: 1, LoadFactor: 1.0,
			AvgDurationMs: durationMs, DataOutBytes: make(map[string]int64),
		}
	}
	orderPlaced := newFn("orderPlaced", 80)
	processPayment := newFn("processPayment", 250)
	updateInventory := newFn("updateInventory", 120)
	prepareShipping := newFn("prepareShipping", 150)
	notifyCustomer := newFn("notifyCustomer", 200)
	logCompletion := newFn("logCompletion", 60)

	orderPlaced.AddChild(processPayment, 262144)
	orderPlaced.AddChild(updateInventory, 262144)
	updateInventory.AddChild(prepareShipping, 524288)
	prepareShipping.AddChild(notifyCustomer, 786432)
	notifyCustomer.AddChild(logCompletion, 1048576)

	return &domain.Application{
		Name: "Ecommerce",
		Functions: []*domain.LambdaFunction{
			orderPlaced, processPayment, updateInventory,
			prepareShipping, notifyCustomer, logCompletion,
		},
		CriticalPathIDs: []string{
			"orderPlaced", "updateInventory", "prepareShipping",
			"notifyCustomer", "logCompletion",
		},
		MaxMemoryMB:  1024,
		MaxLatencyMS: 700,
		NetworkHopMS: 10,
	}
}

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

// A fusion algorithm that loses to doing nothing is worse than useless — it is
// actively misleading. GreedyTP used to return 3.433e-5 on the ecommerce app
// where NoFusion costs 3.402e-5, because it refused to merge critical-path edges
// whenever the latency budget was already satisfied.
func TestGreedyTP_NeverWorseThanNoFusion(t *testing.T) {
	for name, app := range map[string]*domain.Application{
		"image-processing": buildImageProcessingApp(),
		"ecommerce":        buildEcommerceApp(),
	} {
		t.Run(name, func(t *testing.T) {
			noFusion := (&algo.NoFusion{}).Optimize(app)
			greedy := (&algo.GreedyTP{}).Optimize(app)
			if greedy.Error != "" {
				t.Fatalf("GreedyTP returned error: %s", greedy.Error)
			}
			if greedy.Metrics.TotalCostUSD > noFusion.Metrics.TotalCostUSD {
				t.Errorf("GreedyTP cost %.6e exceeds NoFusion %.6e — fusing made it worse",
					greedy.Metrics.TotalCostUSD, noFusion.Metrics.TotalCostUSD)
			}
		})
	}
}

// The critical path carries the heaviest edges in both test applications, so a
// partition that never merges along it is leaving most of the saving behind.
func TestGreedyTP_MergesAlongCriticalPath(t *testing.T) {
	app := buildImageProcessingApp()
	result := (&algo.GreedyTP{}).Optimize(app)
	if result.Error != "" {
		t.Fatalf("GreedyTP returned error: %s", result.Error)
	}

	groupOf := domain.FuncToGroupIndex(result.Groups)
	critPath := app.CriticalPath()
	internalised := 0
	for i := 0; i < len(critPath)-1; i++ {
		if groupOf[critPath[i].ID] == groupOf[critPath[i+1].ID] {
			internalised++
		}
	}
	if internalised == 0 {
		t.Errorf("GreedyTP cut every critical-path edge: %s", canonicalPartition(result.Groups))
	}
}

// Equal-cost edges are common (parallel branches often carry identical
// payloads). With an unstable sort the same input could yield different
// recommendations run to run.
func TestGreedyTP_IsDeterministic(t *testing.T) {
	want := canonicalPartition((&algo.GreedyTP{}).Optimize(buildImageProcessingApp()).Groups)
	for i := 0; i < 25; i++ {
		got := canonicalPartition((&algo.GreedyTP{}).Optimize(buildImageProcessingApp()).Groups)
		if got != want {
			t.Fatalf("run %d produced %q, first run produced %q", i+1, got, want)
		}
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
