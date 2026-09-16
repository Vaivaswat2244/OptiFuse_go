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

	// DataTransferUSDPerGiB prices the bytes on this function's outbound edges.
	//
	// Copied onto every node from Application rather than read from it, because
	// MergeProfitUSD works on bare group slices with no Application in scope, and
	// a package-level constant would be shared mutable state in a server handling
	// concurrent requests.
	//
	// Defaults to 0. See Application.DataTransferUSDPerGiB for why.
	DataTransferUSDPerGiB float64

	// InvocationRate is how many times this function runs per invocation of the
	// whole application. The paper's r_f. The entry function is 1.0 by
	// definition; a function called twice per request is 2.0.
	//
	// Derived from telemetry as InvocationCount(f) / InvocationCount(entry).
	// Defaults to 1.0 when there is no telemetry to derive it from.
	InvocationRate float64

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

	// Cold start cost, written by the enricher from CloudWatch REPORT records.
	// Init is the runtime boot plus module loading plus top-level code, and is a
	// property of the code and its memory setting rather than of traffic, so it
	// is stable enough to measure once and reuse.
	//
	// Nothing reads these yet. The cost model gains a cold start term in a later
	// phase; collecting first means there is real data to build it against.
	AvgInitDurationMs float64
	P99InitDurationMs float64
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

// DataTransferCostUSD returns the data transfer cost for the edge to childID.
//
// The rate was hardcoded at $0.01/GiB, roughly AWS's cross-AZ and egress price.
// That is very likely wrong for the case OptiFuse actually optimises: a
// Lambda invoking another Lambda in the same region goes through the Lambda
// service endpoint, and AWS's pricing page frames transfer charges as applying
// to traffic "from outside the region the function executed".
//
// It matters because this term was the entire cost saving the tool reported.
// Priced at zero, fusing every function in the image-processing example goes
// from 95% cheaper to 21% more expensive, since all that remains is light
// branches re-tiering up to the group's memory. Fusion becomes a latency
// argument, not a cost one.
//
// Python: def get_data_transfer_cost(self, child_id) -> float
func (f *LambdaFunction) DataTransferCostUSD(childID string) float64 {
	if f.DataTransferUSDPerGiB == 0 {
		return 0
	}
	const gib = 1024 * 1024 * 1024
	bytes := float64(f.DataOutBytes[childID])
	return (bytes / gib) * f.DataTransferUSDPerGiB
}

// ExecutionCostUSD returns the AWS Lambda execution cost for a single invocation.
// Python: def get_execution_cost(self) -> float:
//
//	gb_seconds = (memory / 1024) * (runtime / 1000)
//	return 0.00001667 * gb_seconds
func (f *LambdaFunction) ExecutionCostUSD() float64 {
	gbSeconds := (float64(f.MemoryMB) / 1024.0) * (float64(f.RuntimeMs()) / 1000.0)
	return ExecutionPriceUSDPerGBSecond * gbSeconds
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

// MemoryMB returns the memory the fused function would be provisioned with.
//
// Lambda allocates memory per function, not per code path, so a fused function
// has one setting and it has to be large enough for its heaviest member. This
// used to sum the members, which is not a thing Lambda can do.
//
// Summing overstated fused cost, so the solver under-fused rather than
// over-recommending. Taking the max is correct, and it also produces the
// per-branch overbilling effect for free: unfused the group costs
// Σ(memᵢ × rtᵢ), fused it costs max × Σrtᵢ, and the difference is exactly
// Σ((max − memᵢ) × rtᵢ) — every light branch now billing at the heavy branch's
// rate for its own duration. Folding a 128MB handler in with a 1769MB one makes
// the light path cost ~13x what it did, which is why the merge gate in
// MergeIsProfitable exists.
//
// Python: @property def memory(self) -> int
func (c *CompositeFunction) MemoryMB() int {
	max := 0
	for _, f := range c.Members {
		if f.MemoryMB > max {
			max = f.MemoryMB
		}
	}
	return max
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
	return ExecutionPriceUSDPerGBSecond * gbSeconds
}

// RequestPriceUSD is AWS Lambda's per-invocation charge: $0.20 per million.
//
// This is now the only mechanism by which fusion saves money. Merging two
// functions removes one platform invocation and therefore one request charge.
// The transfer term it replaces was never real: AWS confirmed that
// Lambda-to-Lambda invocation inside one region is not billed as data transfer,
// because the payload never leaves the Lambda service's internal network.
const RequestPriceUSD = 0.0000002

// ExecutionPriceUSDPerGBSecond is AWS Lambda's on-demand execution charge for
// x86. Every place that prices execution — the two cost methods here and the
// ILP objective — reads this one constant so they cannot drift apart.
const ExecutionPriceUSDPerGBSecond = 0.00001667

// Rate is how often the function runs per application invocation, relative to
// the entry point. Zero means no telemetry, and a function with no telemetry
// is assumed to run once, which never invents a saving.
func (f *LambdaFunction) Rate() float64 {
	if f.InvocationRate == 0 {
		return 1.0
	}
	return f.InvocationRate
}

// BlockInvocationRate is how many times a fused block is invoked by the
// platform per application invocation.
//
// Only the block's entry counts. A member whose caller is inside the same block
// is reached by an in-process call, which the platform never sees and never
// charges for. That is precisely what fusion buys.
func BlockInvocationRate(group []*LambdaFunction) float64 {
	inGroup := make(map[string]bool, len(group))
	for _, f := range group {
		inGroup[f.ID] = true
	}

	rate := 0.0
	for _, f := range group {
		if f.Parent == nil || !inGroup[f.Parent.ID] {
			rate += f.Rate()
		}
	}
	return rate
}

// RequestCostUSD is what the platform charges to invoke this block.
func RequestCostUSD(group []*LambdaFunction) float64 {
	return RequestPriceUSD * BlockInvocationRate(group)
}

// Tradeoff explains a partition against the do-nothing baseline, so a reader can
// see the mechanism rather than just a total.
//
// Fusion has exactly two cost effects and they pull in opposite directions:
// it removes platform invocations (saving request charges) and it raises every
// member to its block's memory (adding execution charges). Reporting only the
// net hides which one dominated, and that is the thing a user needs in order to
// judge whether the trade suits them.
type Tradeoff struct {
	// InvocationsRemoved is how many platform invocations per application call
	// become in-process calls.
	InvocationsRemoved float64

	// RequestDeltaUSD is negative when fusion saves request charges.
	RequestDeltaUSD float64

	// ExecutionDeltaUSD is positive when members run at a higher memory than
	// they would alone.
	ExecutionDeltaUSD float64

	// HopsRemoved counts cut edges eliminated from the critical path.
	HopsRemoved int
}

// CalculateTradeoff compares a partition against running every function
// separately, which is the only baseline a user can act on without changing
// anything.
func (a *Application) CalculateTradeoff(groups [][]*LambdaFunction) Tradeoff {
	baselineInvocations := 0.0
	baselineExecution := 0.0
	for _, f := range a.Functions {
		single := []*LambdaFunction{f}
		baselineInvocations += BlockInvocationRate(single)
		baselineExecution += groupExecutionCostUSD(single)
	}

	invocations := 0.0
	execution := 0.0
	for _, g := range groups {
		invocations += BlockInvocationRate(g)
		execution += groupExecutionCostUSD(g)
	}

	return Tradeoff{
		InvocationsRemoved: baselineInvocations - invocations,
		RequestDeltaUSD:    RequestPriceUSD * (invocations - baselineInvocations),
		ExecutionDeltaUSD:  execution - baselineExecution,
		HopsRemoved:        a.criticalPathCuts(nil) - a.criticalPathCuts(groups),
	}
}

// criticalPathCuts counts edges on the critical path whose endpoints land in
// different blocks. A nil partition means every function is its own block, i.e.
// every critical-path edge is cut.
func (a *Application) criticalPathCuts(groups [][]*LambdaFunction) int {
	critPath := a.CriticalPath()
	if len(critPath) < 2 {
		return 0
	}
	if groups == nil {
		return len(critPath) - 1
	}

	groupOf := FuncToGroupIndex(groups)
	cuts := 0
	for i := 0; i < len(critPath)-1; i++ {
		if groupOf[critPath[i].ID] != groupOf[critPath[i+1].ID] {
			cuts++
		}
	}
	return cuts
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
	MaxMemoryMB  int // Python: max_memory
	MaxLatencyMS int // Python: max_latency
	NetworkHopMS int // Python: network_hop_delay — added per cross-group call on critical path

	// DataTransferUSDPerGiB prices bytes on cut edges. Defaults to 0.
	//
	// Zero is the honest default for the common case, which is functions calling
	// each other inside one region: AWS does not appear to bill that as data
	// transfer, and asserting a charge we cannot evidence inflated every saving
	// the tool has ever reported.
	//
	// Set it in custom.optifuse.constraints when the calls genuinely cross a
	// region or leave AWS, where EC2 transfer rates do apply.
	DataTransferUSDPerGiB float64
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

// RootFunction returns the function with no parent, or nil if there isn't one.
// Python: @property def root_function(self) -> LambdaFunction
//
// A rootless graph is reachable from a hand-written custom.optifuse.topology
// that forms a cycle, so this returns nil rather than panicking — callers decide
// how to degrade. Where more than one function is parentless, the first in
// Application.Functions order wins (critical path first, see graphToApp).
func (a *Application) RootFunction() *LambdaFunction {
	for _, f := range a.Functions {
		if f.Parent == nil {
			return f
		}
	}
	return nil
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

	// ── Total cost ────────────────────────────────────────────────────────────
	// Per invocation of the whole application:
	//
	//   Σ over blocks:  request charge + execution charge
	//   plus data transfer on cut edges, which is normally zero
	//
	// The request charge is what fusion actually saves. Unfused, the platform
	// invokes every function and bills for each; fused, it only invokes each
	// block's entry and the internal calls become in-process. The execution
	// charge moves the other way, because every member of a block runs at the
	// block's memory rather than its own.
	//
	// That trade is the whole model now. The transfer term used to dominate it
	// and was not a real charge.
	totalCost := 0.0
	for _, c := range composites {
		totalCost += c.ExecutionCostUSD()
		totalCost += RequestCostUSD(c.Members)
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
	if len(critPath) > 0 {
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
	}

	latOK := len(critPath) == 0 || latency <= float64(a.MaxLatencyMS)

	// ── Feasibility ──────────────────────────────────────────────────────────
	memOK := true
	for _, c := range composites {
		if c.MemoryMB() > a.MaxMemoryMB {
			memOK = false
			break
		}
	}

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

// GroupMemory returns the memory a fused group would be provisioned with, which
// is the largest of its members rather than their total. See
// CompositeFunction.MemoryMB for why.
//
// Note what this does to maxMemoryMB as a limit: under the old sum, six 512MB
// functions came to 3072 and a 1024 cap forbade fusing more than two. Under max
// they come to 512 and the cap forbids nothing. The cap is now what it always
// should have been — a check that the fused function fits Lambda's limits — and
// MergeIsProfitable is what actually decides whether a fusion is worth doing.
func GroupMemory(group []*LambdaFunction) int {
	max := 0
	for _, f := range group {
		if f.MemoryMB > max {
			max = f.MemoryMB
		}
	}
	return max
}

// groupExecutionCostUSD is the per-invocation execution cost of running these
// functions as one fused Lambda.
func groupExecutionCostUSD(group []*LambdaFunction) float64 {
	return (&CompositeFunction{Members: group}).ExecutionCostUSD()
}

// MergeProfitUSD returns what fusing two groups saves per invocation: the
// platform invocation that disappears, plus any data transfer that stops
// crossing a billed boundary, minus the extra execution cost from re-tiering
// every member to the combined group's memory.
//
// Positive means the merge pays for itself. Negative means it costs more than it
// saves, which is possible whenever the groups are sized differently, and is
// invisible to a memory ceiling: a 1024MB branch and five 128MB ones total 1664
// memory-units of duration unfused, but 6144 fused, while the transfer saved may
// be a rounding error.
func MergeProfitUSD(a, b []*LambdaFunction) float64 {
	merged := make([]*LambdaFunction, 0, len(a)+len(b))
	merged = append(merged, a...)
	merged = append(merged, b...)

	executionAdded := groupExecutionCostUSD(merged) -
		groupExecutionCostUSD(a) - groupExecutionCostUSD(b)

	// Fusing removes a platform invocation: whichever block was being called by
	// the other stops being invoked externally.
	requestSaved := RequestCostUSD(a) + RequestCostUSD(b) - RequestCostUSD(merged)

	// Edges in both directions stop crossing the network once the endpoints
	// share a group.
	transferSaved := 0.0
	for _, f := range a {
		for _, g := range b {
			transferSaved += f.DataTransferCostUSD(g.ID)
			transferSaved += g.DataTransferCostUSD(f.ID)
		}
	}

	return transferSaved + requestSaved - executionAdded
}

// MergeIsProfitable reports whether fusing two groups is worth doing.
//
// This is a gate on the search, not a term in the objective, and that
// distinction is the whole point. Every solver here minimises data-transfer cost
// on cut edges; total cost only ranks the finished candidates. So pricing the
// memory penalty into the cost model would change nothing, because a partition
// no solver ever proposes cannot be selected by re-ranking. Refusing the merge
// removes the bad partition from the search space instead.
func MergeIsProfitable(a, b []*LambdaFunction) bool {
	return MergeProfitUSD(a, b) > 0
}

// MergeIsHarmful reports whether fusing two groups would cost strictly more than
// it saves.
//
// Deliberately weaker than !MergeIsProfitable, which also rejects a merge worth
// exactly zero. Two equally-sized functions with no edge between them gain
// nothing from fusing but lose nothing either, and forbidding that outright
// would stop three functions in a chain from ever sharing a group: the two ends
// have no direct edge, so their pairwise profit is zero.
//
// The greedy solvers ask the stronger question because every merge they consider
// is driven by an edge. The ILP asks this one, because it reasons about pairs
// rather than about edges.
func MergeIsHarmful(a, b []*LambdaFunction) bool {
	return MergeProfitUSD(a, b) < 0
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
