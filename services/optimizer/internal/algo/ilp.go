package algo

import (
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/Vaivaswat2244/OptiFuse_go/services/optimizer/internal/algo/ilp"
	"github.com/Vaivaswat2244/OptiFuse_go/services/optimizer/internal/domain"
)

// ─────────────────────────────────────────────────────────────────────────────
// MtxILP (Matrix ILP — Optimal)
//
// Python: def mtx_ilp(app: Application) -> dict  (in optimal.py, uses PuLP/CBC)
//
// This is the only algorithm that guarantees an optimal solution.
// It formulates the fusion problem as a Mixed Integer Linear Program:
//
//   Minimize: sum over cut edges of data_transfer_cost(u→v) * is_cut[u,v]
//   Subject to:
//     - Every function is assigned to exactly one group (root)
//     - Root integrity: x[b,f] ≤ x[b,b]  (can only assign f to b if b is a root)
//     - Memory: sum(memory[f] * x[b,f]) ≤ max_memory * x[b,b]  for all b
//     - Cut definition: is_cut[u,v] ≥ |x[b,u] - x[b,v]|  for all b, (u,v)
//     - Latency: runtime_sum + sum(hop_delay * is_cut[u,v]) ≤ max_latency
//                where the sum is over critical path edges
//
// SOLVER STRATEGY:
// PuLP wraps CBC (COIN-B&B). In Go we have two options:
//   1. gonum/optimize — convex only, can't handle MIP
//   2. Shell out to glpk (glpsol) or cbc binary with an LP file
//
// We use option 2: the ilp package formats an MPS file and calls glpsol.
// If glpsol is not installed, the algorithm returns a descriptive error.
// This matches PuLP's behaviour when CBC is missing.
//
// For production, swap ilp.SolveWithGLPK for ilp.SolveWithCBC or a
// commercial solver (CPLEX, Gurobi) by implementing the ilp.Solver interface.
// ─────────────────────────────────────────────────────────────────────────────

type MtxILP struct{}

func (m *MtxILP) Name() string { return "MtxILP (Optimal)" }

func (m *MtxILP) Optimize(app *domain.Application) AlgorithmResult {
	start := time.Now()

	// Check solver availability before building the model.
	if err := checkSolverAvailable(); err != nil {
		return AlgorithmResult{
			Name:        m.Name(),
			Error:       fmt.Sprintf("ILP solver not available: %v — install glpk ('sudo apt install glpk-utils' or 'brew install glpk')", err),
			WallClockMs: float64(time.Since(start).Microseconds()) / 1000.0,
		}
	}

	// Build and solve the ILP model.
	// The ilp package handles MPS formatting and solver invocation.
	result, err := ilp.Solve(app)
	if err != nil {
		return AlgorithmResult{
			Name:        m.Name(),
			Error:       err.Error(),
			WallClockMs: float64(time.Since(start).Microseconds()) / 1000.0,
		}
	}

	if !result.Optimal {
		return AlgorithmResult{
			Name: m.Name(),
			Metrics: domain.Metrics{
				TotalCostUSD: domain.Inf,
				LatencyMS:    domain.Inf,
				Feasible:     false,
			},
			Error:       fmt.Sprintf("solver status: %s", result.Status),
			WallClockMs: float64(time.Since(start).Microseconds()) / 1000.0,
		}
	}

	return AlgorithmResult{
		Name:        m.Name(),
		Groups:      result.Groups,
		Metrics:     app.CalculateMetrics(result.Groups),
		WallClockMs: float64(time.Since(start).Microseconds()) / 1000.0,
	}
}

// checkSolverAvailable returns nil if glpsol is on PATH.
func checkSolverAvailable() error {
	_, err := exec.LookPath("glpsol")
	if err != nil {
		// Also try CBC
		_, err2 := exec.LookPath("cbc")
		if err2 != nil {
			return fmt.Errorf("neither glpsol nor cbc found in PATH")
		}
	}
	return nil
}

// SolverName returns the name of the first available ILP solver.
// Useful for logging / health checks.
func SolverName() string {
	for _, name := range []string{"glpsol", "cbc"} {
		if _, err := exec.LookPath(name); err == nil {
			return name
		}
	}
	return "none"
}

// ILPAvailable reports whether an ILP solver is installed.
func ILPAvailable() bool {
	return !strings.Contains(SolverName(), "none")
}
