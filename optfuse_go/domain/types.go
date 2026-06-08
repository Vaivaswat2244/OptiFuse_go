package domain
// Package domain contains the core data structures for OptiFuse.
//
// This is a direct translation of simulation/core/structures.py.
// Every field name maps to its Python counterpart — differences are noted inline.
//
// RULE: No algorithm, handler, or AWS code imports this package directly from
// outside the optimizer service. Over the wire, everything travels as protobuf
// (proto/graph.proto, proto/optimizer.proto). These types live *inside* the
// optimizer service and are converted to/from proto at the service boundary.
package domain

import "math"

// ─────────────────────────────────────────────────────────────────────────────
// LambdaFunction
//
// Python: @dataclass class LambdaFunction
// ─────────────────────────────────────────────────────────────────────────────

// LambdaFunction represents a single serverless function node in the call graph.
type LambdaFunction struct {
	// ── Identity ────────────────────────────────────────────────────────────
	ID   string // Python: id
	Name string // Python: name

	// ── Static config (from serverless.yml) ─────────────────────────────────
	MemoryMB   int // Python: memory — in MB
	TimeoutSec int // Python: baseline_runtime was stored in ms; we keep seconds here
	            //          and compute ms on demand via BaselineRuntimeMs()

	// LoadFactor multiplies the baseline runtime to simulate load.
	// Default 1.0. Python: load_factor
	LoadFactor float64

	// DataOutBytes maps child function ID → bytes transferred on that edge.
	// Python: data_out_edges: dict[str, int]
	DataOutBytes map[string]int64

	// ── Graph structure ──────────────────────────────────────────────────────
	// Parent is nil for the root function.
	// Python stored parent as a pointer on the child; we do the same.
	Parent   *LambdaFunction
	Children []*LambdaFunction

	// ── Telemetry (filled by enricher; zero value = not enriched) ────────────
	// These replace the simulated baseline_runtime/memory when real data is available.
	// Python: enrich_with_live_data() mutated these fields directly.
	AvgDurationMs   float64 // CloudWatch @duration avg
	AvgMemoryUsedMB float64 // CloudWatch @maxMemoryUsed avg / 1024 / 1024
	InvocationCount int64
	ErrorRate       float64
	P99LatencyMs    float64
	ColdStartRate   float64
}

// BaselineRuntimeMs returns the runtime in milliseconds, adjusted for load.
// Python: @property def runtime(self) -> int: return int(self.baseline_runtime * self.load_factor)
//
// If telemetry is available (AvgDurationMs > 0), it takes precedence over
// the YAML-derived timeout estimate.
func (f *LambdaFunction) RuntimeMs() int {
	base := float64(f.TimeoutSec * 1000)
	if f.AvgDurationMs > 0 {
		base = f.AvgDurationMs
	}
	lf := f.LoadFactor
	if lf == 0 {
		lf = 1.0
	}
	return int(base * lf)
}

// AddChild establishes the parent→child relationship and records the data edge.
// Python: def add_child(self, child, data_bytes)
func (f *LambdaFunction) AddChild(child *LambdaFunction, dataBytes int64) {
	f.Children = append(f.Children, child)
	child.Parent = f
	if f.DataOutBytes == nil {
		f.DataOutBytes = make(map[string]int64)
	}
	f.DataOutBytes[child.ID] = dataBytes
}

// DataTransferCostUSD returns the AWS data transfer cost for the edge to childID.
// Python: def get_data_transfer_cost(self, child_id) -> float:
//
//	return (bytes_transferred / GiB) * $0.01
func (f *LambdaFunction) DataTransferCostUSD(childID string) float64 {
	bytes := float64(f.DataOutBytes[childID])
	const gib = 1024 * 1024 * 1024
	const pricePerGiB = 0.01
	return (bytes / gib) * pricePerGiB
}

// ExecutionCostUSD returns the AWS Lambda execution cost for a single invocation.
// Python: def get_execution_cost(self) -> float:
//
//	gb_seconds = (memory / 1024) * (runtime / 1000)
//	return 0.00001667 * gb_seconds
func (f *LambdaFunction) ExecutionCostUSD() float64 {
	gbSeconds := (float64(f.MemoryMB) / 1024.0) * (float64(f.RuntimeMs()) / 1000.0)
	return 0.00001667 * gbSeconds
}

// ─────────────────────────────────────────────────────────────────────────────
// CompositeFunction
//
// Python: @dataclass class CompositeFunction
// Represents a fused group of functions — the output unit of every algorithm.
// ─────────────────────────────────────────────────────────────────────────────

// CompositeFunction is a fused deployment group.
// Member order is preserved (first member is the canonical ID of the group).
type CompositeFunction struct {
	Members []*LambdaFunction // Python: member_functions
}

// ID returns the canonical identifier for this group (the first member's ID).
// Python: @property def id(self) -> str: return self.member_functions[0].id
func (c *CompositeFunction) ID() string {
	if len(c.Members) == 0 {
		return ""
	}
	return c.Members[0].ID
}

// MemoryMB returns the sum of all member memories.
// Python: @property def memory(self) -> int
func (c *CompositeFunction) MemoryMB() int {
	total := 0
	for _, f := range c.Members {
		total += f.MemoryMB
	}
	return total
}

// RuntimeMs returns the sum of all member runtimes (sequential execution).
// Python: @property def runtime(self) -> int
func (c *CompositeFunction) RuntimeMs() int {
	total := 0
	for _, f := range c.Members {
		total += f.RuntimeMs()
	}
	return total
}

// ExecutionCostUSD calculates the cost for a single invocation of the fused group.
// Python: def get_execution_cost(self) -> float
func (c *CompositeFunction) ExecutionCostUSD() float64 {
	gbSeconds := (float64(c.MemoryMB()) / 1024.0) * (float64(c.RuntimeMs()) / 1000.0)
	return 0.00001667 * gbSeconds
}

// ─────────────────────────────────────────────────────────────────────────────
// Application
//
// Python: @dataclass class Application
// The complete graph + constraints that every algorithm receives as input.
// ─────────────────────────────────────────────────────────────────────────────

// Application encapsulates the full serverless application model.
type Application struct {
	Name      string
	Functions []*LambdaFunction

	// CriticalPathIDs is the ordered list of function IDs on the critical path.
	// Python: critical_path_ids — provided by user in serverless.yml custom block.
	CriticalPathIDs []string

	// Constraints
	MaxMemoryMB    int     // Python: max_memory
	MaxLatencyMS   int     // Python: max_latency
	NetworkHopMS   int     // Python: network_hop_delay — added per cross-group call on critical path
}

// FunctionsMap returns a map of function ID → *LambdaFunction for O(1) lookup.
// Python: @property def functions_map(self) -> dict[str, LambdaFunction]
// Note: Python recomputed this on every access. In Go we compute on demand;
// callers that need it frequently should cache the result themselves.
func (a *Application) FunctionsMap() map[string]*LambdaFunction {
	m := make(map[string]*LambdaFunction, len(a.Functions))
	for _, f := range a.Functions {
		m[f.ID] = f
	}
	return m
}

// RootFunction returns the function with no parent.
// Python: @property def root_function(self) -> LambdaFunction
// Panics if no root exists — the parser guarantees exactly one root.
func (a *Application) RootFunction() *LambdaFunction {
	for _, f := range a.Functions {
		if f.Parent == nil {
			return f
		}
	}
	panic("application has no root function — invalid graph")
}

// CriticalPath returns the ordered slice of LambdaFunction pointers on the critical path.
// Python: @property def critical_path_functions(self) -> list[LambdaFunction]
func (a *Application) CriticalPath() []*LambdaFunction {
	fm := a.FunctionsMap()
	path := make([]*LambdaFunction, 0, len(a.CriticalPathIDs))
	for _, id := range a.CriticalPathIDs {
		if f, ok := fm[id]; ok {
			path = append(path, f)
		}
	}
	return path
}

// ─────────────────────────────────────────────────────────────────────────────
// Metrics
//
// Python: calculate_metrics() in simulation/algorithms/metrics.py
// Kept here as a domain method on Application so all algorithm files
// can call app.CalculateMetrics(groups) without importing a separate package.
// ─────────────────────────────────────────────────────────────────────────────

// Metrics holds the evaluation output for a given partition.
type Metrics struct {
	TotalCostUSD float64
	LatencyMS    float64
	Feasible     bool
}

// CalculateMetrics evaluates a proposed partition (list of groups) against
// the application's constraints.
//
// Python: calculate_metrics(groups_of_funcs, app) in metrics.py
func (a *Application) CalculateMetrics(groups [][]*LambdaFunction) Metrics {
	// Build composite groups
	composites := make([]*CompositeFunction, len(groups))
	for i, g := range groups {
		composites[i] = &CompositeFunction{Members: g}
	}

	// Map each function ID → its composite group
	funcToGroup := make(map[string]*CompositeFunction)
	for _, c := range composites {
		for _, f := range c.Members {
			funcToGroup[f.ID] = c
		}
	}

	// ── Total cost: execution cost of all composites + data transfer on cut edges ──
	totalCost := 0.0
	for _, c := range composites {
		totalCost += c.ExecutionCostUSD()
	}
	for _, f := range a.Functions {
		parentGroup := funcToGroup[f.ID]
		for _, child := range f.Children {
			childGroup := funcToGroup[child.ID]
			// Only charge data transfer if the edge is a CUT (different groups)
			if parentGroup != nil && childGroup != nil && parentGroup.ID() != childGroup.ID() {
				totalCost += f.DataTransferCostUSD(child.ID)
			}
		}
	}

	// ── Latency: sum of runtimes on critical path + network hop per cut edge ──
	latency := 0.0
	critPath := a.CriticalPath()
	for _, f := range critPath {
		latency += float64(f.RuntimeMs())
	}
	for i := 0; i < len(critPath)-1; i++ {
		parent := critPath[i]
		child := critPath[i+1]
		pg := funcToGroup[parent.ID]
		cg := funcToGroup[child.ID]
		if pg != nil && cg != nil && pg.ID() != cg.ID() {
			latency += float64(a.NetworkHopMS)
		}
	}

	// ── Feasibility ──────────────────────────────────────────────────────────
	memOK := true
	for _, c := range composites {
		if c.MemoryMB() > a.MaxMemoryMB {
			memOK = false
			break
		}
	}
	latOK := latency <= float64(a.MaxLatencyMS)

	return Metrics{
		TotalCostUSD: totalCost,
		LatencyMS:    latency,
		Feasible:     memOK && latOK,
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Helpers used by multiple algorithms
// ─────────────────────────────────────────────────────────────────────────────

// FuncToGroupIndex builds a map of function ID → index into the groups slice.
// Used by min_w_cut and greedy_tp to find which group a function currently
// belongs to without scanning the whole slice each time.
//
// Python: temp_group_map = {f.id: i for i, g in enumerate(groups) for f in g}
func FuncToGroupIndex(groups [][]*LambdaFunction) map[string]int {
	m := make(map[string]int)
	for i, g := range groups {
		for _, f := range g {
			m[f.ID] = i
		}
	}
	return m
}

// GroupMemory returns the total memory of a group in MB.
// Python: sum(f.memory for f in group)
func GroupMemory(group []*LambdaFunction) int {
	total := 0
	for _, f := range group {
		total += f.MemoryMB
	}
	return total
}

// RemoveIndex removes element at idx from a slice without preserving order.
// Used when merging groups: we extend one and pop the other.
// Python: groups.pop(child_idx)
func RemoveIndex[T any](s []T, idx int) []T {
	s[idx] = s[len(s)-1]
	return s[:len(s)-1]
}

// Inf is a convenience constant for infeasible algorithm results.
const Inf = math.MaxFloat64