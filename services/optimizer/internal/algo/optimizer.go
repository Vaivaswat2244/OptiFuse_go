// Package algo defines the Optimizer interface and the AlgorithmResult type.
// Every algorithm (no_fusion, singleton, min_w_cut, greedy_tp, costless_csp, mtx_ilp)
// lives in its own file in this package and implements this interface.
//
// # Known model limitations
//
// These are inherited from the Python implementation and are deliberate for now.
// Read them before trusting a result or debugging a "why didn't it fuse?" report.
//
// 1. The solvers and the ranking disagree on what they are optimizing.
//
// MinWCut, GreedyTP, CostlessCSP and MtxILP all minimize *data-transfer cost on
// cut edges only* (see csp.go's label cost and ilp/solver.go's objective).
// Nothing in their objective accounts for execution cost. But the recommendation
// in optimizer/cmd/main.go ranks candidates by Metrics.TotalCostUSD, which is
// execution + transfer. So MtxILP is "optimal" for a different objective than
// the one it is scored on, and the heuristics push toward maximum fusion while
// the ranker may then reject it.
//
// 2. Fusion is structurally penalized by the cost model.
//
// CompositeFunction.MemoryMB sums member memory and RuntimeMs sums member
// runtimes, so a fused group costs (Σmem)x(Σruntime) where the same functions
// unfused cost Σ(mem x runtime). The cross terms mean fusion always looks more
// expensive on execution, and only wins when data-transfer savings outweigh
// them. A real fused Lambda has a single memory allocation (about the max of its
// members), not the sum. The practical effect: the larger the runtimes, the more
// fusion is penalized — which is why missing telemetry (runtime falling back to
// TimeoutSec, default 30s) makes everything collapse to NoFusion.
//
// 3. Fusion's real benefits are not modeled at all.
//
// No per-request charge ($0.20/1M invocations), no cold-start savings, and
// nothing is weighted by traffic volume. InvocationCount and AvgMemoryUsedMB are
// fetched by the enricher, carried through the proto, mapped onto
// domain.LambdaFunction — and then read by no cost function. ErrorRate,
// P99LatencyMs and ColdStartRate are never populated at all.
//
// Fixing 1–3 means changing what the numbers mean, so it is tracked separately
// from making the pipeline produce a graph in the first place.
package algo

import (
	"github.com/Vaivaswat2244/OptiFuse_go/services/optimizer/internal/domain"
)

// Optimizer is the contract every algorithm must satisfy.
// Python equivalent: every function in heuristic.py / optimal.py takes
// app: Application and returns a dict. Here we make that contract explicit.
type Optimizer interface {
	// Name returns the human-readable algorithm name, e.g. "Greedy TP (GrTP)".
	Name() string

	// Optimize runs the algorithm and returns the result.
	// It must never panic — all errors are returned in AlgorithmResult.Error.
	Optimize(app *domain.Application) AlgorithmResult
}

// AlgorithmResult is the structured output of every algorithm.
// Python equivalent: the dict returned by each algo function, e.g.:
//
//	{'name': 'NoFusion', 'groups': groups, 'cost': ..., 'latency': ...,
//	 'feasible': ..., 'runtime': ...}
type AlgorithmResult struct {
	Name    string
	Groups  [][]*domain.LambdaFunction // the proposed partition
	Metrics domain.Metrics
	// WallClockMs is how long the algorithm itself took.
	// Python: (time.time() - start_time) * 1000
	WallClockMs float64
	// Error is non-empty if the algorithm failed or found no feasible solution.
	Error string

	// Dominated is true when another candidate is no worse on both cost and
	// latency and strictly better on one, i.e. this option is never worth
	// choosing. Set by MarkFrontier, not by the algorithms themselves.
	Dominated   bool
	DominatedBy string

	// Tradeoff explains this partition against running everything separately.
	Tradeoff domain.Tradeoff
}
