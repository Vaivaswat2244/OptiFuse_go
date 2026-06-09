// Package algo defines the Optimizer interface and the AlgorithmResult type.
// Every algorithm (no_fusion, singleton, min_w_cut, greedy_tp, costless_csp, mtx_ilp)
// lives in its own file in this package and implements this interface.
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
}
