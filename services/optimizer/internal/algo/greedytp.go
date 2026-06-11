package algo

import (
	"fmt"
	"sort"
	"time"

	"github.com/Vaivaswat2244/OptiFuse_go/services/optimizer/internal/domain"
)

// ─────────────────────────────────────────────────────────────────────────────
// GreedyTP (Greedy Tree Partitioning)
//
// Python: def greedy_tree_partitioning(app: Application) -> dict
//
// Strategy:
//   1. Find the minimum number of cuts k on the critical path such that
//      latency ≤ app.MaxLatencyMS.
//   2. Use those k cuts as "barrier" nodes — each barrier starts a new group.
//   3. BFS from each barrier assigns non-barrier nodes to their nearest barrier.
//   4. Then apply the same greedy merge as MinWCut (but only on non-cut edges)
//      to further reduce data transfer cost within the latency constraint.
//
// This is the most sophisticated heuristic and usually produces the best
// cost/latency trade-off among the polynomial-time algorithms.
// ─────────────────────────────────────────────────────────────────────────────

type GreedyTP struct{}

func (g *GreedyTP) Name() string { return "Greedy TP (GrTP)" }

// edge is a directed pair of function pointers used as a map key.
type edge struct {
	from, to string // function IDs
}

func (g *GreedyTP) Optimize(app *domain.Application) AlgorithmResult {
	start := time.Now()

	critPath := app.CriticalPath()
	if len(critPath) == 0 {
		return AlgorithmResult{
			Name:  g.Name(),
			Error: "critical path is empty — check criticalPath in serverless.yml",
		}
	}

	// ── Step 1: Find minimum k cuts on critical path ─────────────────────────
	// Python: base_latency = sum(f.runtime for f in critical_path)
	baseLatency := 0
	for _, f := range critPath {
		baseLatency += f.RuntimeMs()
	}

	if baseLatency > app.MaxLatencyMS {
		return AlgorithmResult{
			Name: g.Name(),
			Metrics: domain.Metrics{
				LatencyMS: float64(baseLatency),
				Feasible:  false,
			},
			Error:       fmt.Sprintf("base latency %dms exceeds max %dms — infeasible even with singleton", baseLatency, app.MaxLatencyMS),
			WallClockMs: float64(time.Since(start).Microseconds()) / 1000.0,
		}
	}

	// Build the list of edges on the critical path.
	// Python: critical_path_edges = list(zip(critical_path[:-1], critical_path[1:]))
	cpEdges := make([]edge, len(critPath)-1)
	for i := 0; i < len(critPath)-1; i++ {
		cpEdges[i] = edge{critPath[i].ID, critPath[i+1].ID}
	}

	// Try k=0,1,2,... cuts until latency fits.
	// Python: for k in range(len(critical_path_edges) + 1):
	//             for merge_combination in itertools.combinations(critical_path_edges, k):
	//
	// Go: we replicate combinations with a recursive helper.
	var initialCuts map[edge]bool
	found := false

	for k := 0; k <= len(cpEdges); k++ {
		// Iterate over all combinations of k edges to MERGE (i.e., NOT cut).
		// The cuts are the remaining edges.
		for _, mergedEdges := range combinations(cpEdges, k) {
			mergedSet := make(map[edge]bool, len(mergedEdges))
			for _, e := range mergedEdges {
				mergedSet[e] = true
			}

			numExternal := len(cpEdges) - len(mergedEdges)
			currentLatency := baseLatency + numExternal*app.NetworkHopMS

			if currentLatency <= app.MaxLatencyMS {
				// The cut set is all cp edges NOT in mergedSet.
				initialCuts = make(map[edge]bool)
				for _, e := range cpEdges {
					if !mergedSet[e] {
						initialCuts[e] = true
					}
				}
				found = true
				break
			}
		}
		if found {
			break
		}
	}

	if !found {
		return AlgorithmResult{
			Name:        g.Name(),
			Error:       "no feasible partitioning found on critical path",
			WallClockMs: float64(time.Since(start).Microseconds()) / 1000.0,
		}
	}

	// ── Step 2: Build initial groups via BFS from barrier nodes ──────────────
	// Barrier nodes: root + the "to" node of every initial cut.
	// Python: initial_barrier_nodes = {app.root_function} | {child for _, child in initial_cuts}
	barrierIDs := map[string]bool{app.RootFunction().ID: true}
	for e := range initialCuts {
		barrierIDs[e.to] = true
	}

	fm := app.FunctionsMap()

	// groups_dict maps barrier function ID → list of functions in that group.
	groupsDict := make(map[string][]*domain.LambdaFunction)
	// nodeToBarrier maps each function ID → its barrier function.
	nodeToBarrier := make(map[string]string)

	for id := range barrierIDs {
		groupsDict[id] = []*domain.LambdaFunction{fm[id]}
		nodeToBarrier[id] = id
	}

	// BFS to assign every non-barrier node to the nearest barrier.
	// Python: q = list(initial_barrier_nodes) ... BFS ...
	bfsQueue := make([]*domain.LambdaFunction, 0)
	for id := range barrierIDs {
		bfsQueue = append(bfsQueue, fm[id])
	}
	bfsHead := 0
	for bfsHead < len(bfsQueue) {
		current := bfsQueue[bfsHead]
		bfsHead++
		barrier := nodeToBarrier[current.ID]
		for _, child := range current.Children {
			if _, assigned := nodeToBarrier[child.ID]; !assigned {
				nodeToBarrier[child.ID] = barrier
				groupsDict[barrier] = append(groupsDict[barrier], child)
				bfsQueue = append(bfsQueue, child)
			}
		}
	}

	// Convert map to slice.
	groups := make([][]*domain.LambdaFunction, 0, len(groupsDict))
	for _, g := range groupsDict {
		groups = append(groups, g)
	}

	safeGroups := make([][]*domain.LambdaFunction, 0, len(groups))
	for _, g := range groups {
		if domain.GroupMemory(g) <= app.MaxMemoryMB {
			safeGroups = append(safeGroups, g)
			continue
		}
		// Greedily keep functions until memory would be exceeded, eject the rest.
		var kept []*domain.LambdaFunction
		usedMem := 0
		var ejected []*domain.LambdaFunction
		for _, f := range g {
			if usedMem+f.MemoryMB <= app.MaxMemoryMB {
				kept = append(kept, f)
				usedMem += f.MemoryMB
			} else {
				ejected = append(ejected, f)
			}
		}
		safeGroups = append(safeGroups, kept)
		for _, f := range ejected {
			safeGroups = append(safeGroups, []*domain.LambdaFunction{f})
		}
	}
	groups = safeGroups

	// ── Step 3: Secondary greedy merge (only non-cut edges) ──────────────────
	// Python: merge_candidates = [(cost, f, child) for non-cut edges]
	//         sort desc, then merge if memory fits
	type candidate struct {
		cost   float64
		parent *domain.LambdaFunction
		child  *domain.LambdaFunction
	}
	var candidates []candidate
	for _, f := range app.Functions {
		for _, child := range f.Children {
			e := edge{f.ID, child.ID}
			if !initialCuts[e] {
				candidates = append(candidates, candidate{
					cost:   f.DataTransferCostUSD(child.ID),
					parent: f,
					child:  child,
				})
			}
		}
	}
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].cost > candidates[j].cost
	})

	for _, c := range candidates {
		idx := domain.FuncToGroupIndex(groups)
		parentIdx, pOK := idx[c.parent.ID]
		childIdx, cOK := idx[c.child.ID]
		if !pOK || !cOK || parentIdx == childIdx {
			continue
		}
		if domain.GroupMemory(groups[parentIdx])+domain.GroupMemory(groups[childIdx]) <= app.MaxMemoryMB {
			groups[parentIdx] = append(groups[parentIdx], groups[childIdx]...)
			groups = domain.RemoveIndex(groups, childIdx)
		}
	}

	return AlgorithmResult{
		Name:        g.Name(),
		Groups:      groups,
		Metrics:     app.CalculateMetrics(groups),
		WallClockMs: float64(time.Since(start).Microseconds()) / 1000.0,
	}
}

// combinations returns all k-element subsets of edges.
// Go equivalent of itertools.combinations(edges, k).
func combinations(edges []edge, k int) [][]edge {
	if k == 0 {
		return [][]edge{{}}
	}
	if k > len(edges) {
		return nil
	}
	var result [][]edge
	var helper func(start int, current []edge)
	helper = func(start int, current []edge) {
		if len(current) == k {
			cp := make([]edge, k)
			copy(cp, current)
			result = append(result, cp)
			return
		}
		for i := start; i < len(edges); i++ {
			helper(i+1, append(current, edges[i]))
		}
	}
	helper(0, nil)
	return result
}
