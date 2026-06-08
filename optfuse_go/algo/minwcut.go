package algo

import (
	"sort"
	"time"

	"github.com/Vaivaswat2244/OptiFuse_go/services/optimizer/internal/domain"
)

// ─────────────────────────────────────────────────────────────────────────────
// MinWCut (Minimum Weight Cut Heuristic)
//
// Python: def min_w_cut_heuristic(app: Application) -> dict
//
// Strategy: start with every function in its own group (same as NoFusion).
// Then greedily merge parent→child pairs in descending order of data transfer
// cost — i.e., merge the most expensive edges first — as long as the combined
// memory stays within app.MaxMemoryMB.
//
// This reduces data transfer cost without ever considering latency, which makes
// it fast but potentially infeasible on tight latency constraints.
// ─────────────────────────────────────────────────────────────────────────────

type MinWCut struct{}

func (m *MinWCut) Name() string { return "MinWCut Heuristic" }

func (m *MinWCut) Optimize(app *domain.Application) AlgorithmResult {
	start := time.Now()

	// Start: each function in its own singleton group.
	// Python: groups = [[f] for f in app.functions]
	groups := make([][]*domain.LambdaFunction, len(app.Functions))
	for i, f := range app.Functions {
		groups[i] = []*domain.LambdaFunction{f}
	}

	// Collect all (cost, parent, child) merge candidates.
	// Python: merge_candidates = []
	//         for f in app.functions:
	//             for child in f.children:
	//                 merge_candidates.append((f.get_data_transfer_cost(child.id), f, child))
	type candidate struct {
		cost   float64
		parent *domain.LambdaFunction
		child  *domain.LambdaFunction
	}
	var candidates []candidate
	for _, f := range app.Functions {
		for _, child := range f.Children {
			candidates = append(candidates, candidate{
				cost:   f.DataTransferCostUSD(child.ID),
				parent: f,
				child:  child,
			})
		}
	}

	// Sort descending by cost — merge most expensive edges first.
	// Python: merge_candidates.sort(key=lambda x: x[0], reverse=True)
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].cost > candidates[j].cost
	})

	// Greedy merge loop.
	for _, c := range candidates {
		// Recompute the group index map on each iteration.
		// Python: temp_group_map = {f.id: i for i, g in enumerate(groups) for f in g}
		idx := domain.FuncToGroupIndex(groups)

		parentIdx, parentOK := idx[c.parent.ID]
		childIdx, childOK := idx[c.child.ID]

		// Skip if either function isn't found, or they're already in the same group.
		if !parentOK || !childOK || parentIdx == childIdx {
			continue
		}

		// Check memory constraint before merging.
		// Python: if sum(f.memory for f in parent_group) + sum(...child_group) <= app.max_memory
		if domain.GroupMemory(groups[parentIdx])+domain.GroupMemory(groups[childIdx]) <= app.MaxMemoryMB {
			// Merge child group into parent group, then remove child group slot.
			// Python: groups[parent_idx].extend(child_group); groups.pop(child_idx)
			groups[parentIdx] = append(groups[parentIdx], groups[childIdx]...)
			groups = domain.RemoveIndex(groups, childIdx)
		}
	}

	return AlgorithmResult{
		Name:        m.Name(),
		Groups:      groups,
		Metrics:     app.CalculateMetrics(groups),
		WallClockMs: float64(time.Since(start).Microseconds()) / 1000.0,
	}
}
