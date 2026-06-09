package algo

import (
	"time"

	"github.com/Vaivaswat2244/OptiFuse_go/services/optimizer/internal/domain"
)

// ─────────────────────────────────────────────────────────────────────────────
// NoFusion
//
// Python: def no_fusion(app: Application) -> dict
// Every function is its own group. This is the baseline — no optimization,
// maximum invocation cost, maximum data transfer cost.
// ─────────────────────────────────────────────────────────────────────────────

type NoFusion struct{}

func (n *NoFusion) Name() string { return "NoFusion" }

func (n *NoFusion) Optimize(app *domain.Application) AlgorithmResult {
	start := time.Now()

	// Python: groups = [[func] for func in app.functions]
	groups := make([][]*domain.LambdaFunction, len(app.Functions))
	for i, f := range app.Functions {
		groups[i] = []*domain.LambdaFunction{f}
	}

	return AlgorithmResult{
		Name:        n.Name(),
		Groups:      groups,
		Metrics:     app.CalculateMetrics(groups),
		WallClockMs: float64(time.Since(start).Microseconds()) / 1000.0,
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Singleton
//
// Python: def singleton(app: Application) -> dict
// All functions in a single group, ordered by BFS from the root.
// This is the maximum fusion — one Lambda deployment containing everything.
// ─────────────────────────────────────────────────────────────────────────────

type Singleton struct{}

func (s *Singleton) Name() string { return "Singleton" }

func (s *Singleton) Optimize(app *domain.Application) AlgorithmResult {
	start := time.Now()

	// BFS from root to get topological order.
	// Python:
	//   q = [app.root_function]
	//   visited = {app.root_function.id}
	//   while head < len(q): ...
	root := app.RootFunction()
	queue := []*domain.LambdaFunction{root}
	visited := map[string]bool{root.ID: true}
	ordered := make([]*domain.LambdaFunction, 0, len(app.Functions))

	head := 0
	for head < len(queue) {
		node := queue[head]
		head++
		ordered = append(ordered, node)
		for _, child := range node.Children {
			if !visited[child.ID] {
				visited[child.ID] = true
				queue = append(queue, child)
			}
		}
	}

	// One group containing all functions in BFS order.
	groups := [][]*domain.LambdaFunction{ordered}

	return AlgorithmResult{
		Name:        s.Name(),
		Groups:      groups,
		Metrics:     app.CalculateMetrics(groups),
		WallClockMs: float64(time.Since(start).Microseconds()) / 1000.0,
	}
}
