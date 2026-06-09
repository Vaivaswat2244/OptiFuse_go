package algo

import (
	"container/heap"
	"time"

	"github.com/Vaivaswat2244/OptiFuse_go/services/optimizer/internal/domain"
)

// ─────────────────────────────────────────────────────────────────────────────
// CostlessCSP (Constrained Shortest Path on critical path)
//
// Python: def costless_csp(app: Application) -> dict
//
// Strategy: label-setting algorithm on the critical path.
// Each label tracks (cost, latency, current_group_memory, partitioning).
// At each step we can either MERGE the next node into the current group,
// or CUT and start a new group (paying a network hop).
// We keep only Pareto-optimal labels (no label dominates another on both
// cost and latency). At the end, pick the minimum-cost feasible label.
// Functions NOT on the critical path are assigned to singleton groups.
//
// This is the most theoretically principled polynomial-time algorithm.
// It guarantees optimality on the critical path under memory constraints.
// ─────────────────────────────────────────────────────────────────────────────

type CostlessCSP struct{}

func (c *CostlessCSP) Name() string { return "Costless (CSP)" }

// cspLabel corresponds to the CSPLabel dataclass in Python.
// Python:
//
//	@dataclass class CSPLabel:
//	    cost: float; latency: int; current_group_mem: int
//	    partitioning: Tuple[Tuple[LambdaFunction, ...], ...]
type cspLabel struct {
	cost            float64
	latency         int
	currentGroupMem int
	// partitioning is a slice of groups; each group is a slice of function IDs.
	// We store IDs rather than pointers to keep labels comparable and copyable.
	// Python stored the actual function objects; we resolve IDs back to pointers
	// at the end when building the final groups.
	partitioning [][]string // [group_index][member_index] = function ID
}

// dominates returns true if l dominates other on both cost and latency.
// Python: any(l.cost <= new_label.cost and l.latency <= new_label.latency for l in labels[v.id])
func (l *cspLabel) dominates(other *cspLabel) bool {
	return l.cost <= other.cost && l.latency <= other.latency
}

// clonePartitioning deep-copies the partitioning slice so mutations don't
// affect the original label. Python used immutable tuples; Go slices are mutable.
func (l *cspLabel) clonePartitioning() [][]string {
	cp := make([][]string, len(l.partitioning))
	for i, g := range l.partitioning {
		gcp := make([]string, len(g))
		copy(gcp, g)
		cp[i] = gcp
	}
	return cp
}

// ── Priority queue implementation ────────────────────────────────────────────

type pqItem struct {
	cost   float64
	nodeID string
	label  *cspLabel
	index  int
}

type labelPQ []*pqItem

func (pq labelPQ) Len() int           { return len(pq) }
func (pq labelPQ) Less(i, j int) bool { return pq[i].cost < pq[j].cost }
func (pq labelPQ) Swap(i, j int)      { pq[i], pq[j] = pq[j], pq[i]; pq[i].index = i; pq[j].index = j }
func (pq *labelPQ) Push(x interface{}) {
	item := x.(*pqItem)
	item.index = len(*pq)
	*pq = append(*pq, item)
}
func (pq *labelPQ) Pop() interface{} {
	old := *pq
	n := len(old)
	item := old[n-1]
	*pq = old[:n-1]
	return item
}

// ─────────────────────────────────────────────────────────────────────────────

func (c *CostlessCSP) Optimize(app *domain.Application) AlgorithmResult {
	start := time.Now()

	chain := app.CriticalPath()
	if len(chain) == 0 {
		return AlgorithmResult{
			Name:  c.Name(),
			Error: "critical path is empty",
		}
	}

	fm := app.FunctionsMap()

	// labels[nodeID] holds the current Pareto-frontier of labels at that node.
	labels := make(map[string][]*cspLabel, len(chain))

	// Seed the priority queue with the initial label at chain[0].
	// Python: initial_label = CSPLabel(cost=0, latency=start_node.runtime,
	//             current_group_mem=start_node.memory, partitioning=((start_node,),))
	startNode := chain[0]
	initialLabel := &cspLabel{
		cost:            0,
		latency:         startNode.RuntimeMs(),
		currentGroupMem: startNode.MemoryMB,
		partitioning:    [][]string{{startNode.ID}},
	}
	labels[startNode.ID] = []*cspLabel{initialLabel}

	pq := &labelPQ{}
	heap.Init(pq)
	heap.Push(pq, &pqItem{cost: 0, nodeID: startNode.ID, label: initialLabel})

	// Build a position map for O(1) chain index lookup.
	// Python: chain.index(u) — linear scan; we avoid that.
	chainIdx := make(map[string]int, len(chain))
	for i, f := range chain {
		chainIdx[f.ID] = i
	}

	// ── Main label-setting loop ───────────────────────────────────────────────
	for pq.Len() > 0 {
		item := heap.Pop(pq).(*pqItem)
		uID := item.nodeID
		uLabel := item.label

		uIndex, ok := chainIdx[uID]
		if !ok || uIndex+1 >= len(chain) {
			continue
		}
		v := chain[uIndex+1]
		u := fm[uID]

		// ── Option A: MERGE v into current group ─────────────────────────────
		// Python: if u_label.current_group_mem + v.memory <= app.max_memory:
		if uLabel.currentGroupMem+v.MemoryMB <= app.MaxMemoryMB {
			newPart := uLabel.clonePartitioning()
			// Append v to the last group.
			// Python: new_part_merge[-1] += (v,)
			last := len(newPart) - 1
			newPart[last] = append(newPart[last], v.ID)

			mergeLabel := &cspLabel{
				cost:            uLabel.cost,
				latency:         uLabel.latency + v.RuntimeMs(),
				currentGroupMem: uLabel.currentGroupMem + v.MemoryMB,
				partitioning:    newPart,
			}

			if !isDominated(labels[v.ID], mergeLabel) {
				labels[v.ID] = pruneDominated(labels[v.ID], mergeLabel)
				labels[v.ID] = append(labels[v.ID], mergeLabel)
				heap.Push(pq, &pqItem{cost: mergeLabel.cost, nodeID: v.ID, label: mergeLabel})
			}
		}

		// ── Option B: CUT — start a new group for v ───────────────────────────
		// Python: new_label_cut = CSPLabel(cost=u_label.cost + u.get_data_transfer_cost(v.id), ...)
		cutCost := uLabel.cost + u.DataTransferCostUSD(v.ID)
		newPart := uLabel.clonePartitioning()
		newPart = append(newPart, []string{v.ID}) // new group starting with v

		cutLabel := &cspLabel{
			cost:            cutCost,
			latency:         uLabel.latency + v.RuntimeMs() + app.NetworkHopMS,
			currentGroupMem: v.MemoryMB,
			partitioning:    newPart,
		}

		if !isDominated(labels[v.ID], cutLabel) {
			labels[v.ID] = pruneDominated(labels[v.ID], cutLabel)
			labels[v.ID] = append(labels[v.ID], cutLabel)
			heap.Push(pq, &pqItem{cost: cutCost, nodeID: v.ID, label: cutLabel})
		}
	}

	// ── Select best feasible label at chain end ───────────────────────────────
	// Python: best_label = min([l for l in labels[chain[-1].id]
	//                           if l.latency <= app.max_latency], key=lambda l: l.cost, default=None)
	endID := chain[len(chain)-1].ID
	var bestLabel *cspLabel
	for _, l := range labels[endID] {
		if l.latency <= app.MaxLatencyMS {
			if bestLabel == nil || l.cost < bestLabel.cost {
				bestLabel = l
			}
		}
	}

	if bestLabel == nil {
		return AlgorithmResult{
			Name:        c.Name(),
			Error:       "infeasible on critical path — no partition satisfies latency constraint",
			WallClockMs: float64(time.Since(start).Microseconds()) / 1000.0,
		}
	}

	// ── Build final groups ────────────────────────────────────────────────────
	// Convert ID-based partitioning back to *LambdaFunction slices.
	// Python: final_groups = [list(g) for g in best_label.partitioning]
	finalGroups := make([][]*domain.LambdaFunction, len(bestLabel.partitioning))
	assignedIDs := make(map[string]bool)
	for i, group := range bestLabel.partitioning {
		funcs := make([]*domain.LambdaFunction, 0, len(group))
		for _, id := range group {
			if f, ok := fm[id]; ok {
				funcs = append(funcs, f)
				assignedIDs[id] = true
			}
		}
		finalGroups[i] = funcs
	}

	// Assign functions NOT on the critical path to singleton groups.
	// Python: for func in app.functions: if func not in assigned_funcs: final_groups.append([func])
	for _, f := range app.Functions {
		if !assignedIDs[f.ID] {
			finalGroups = append(finalGroups, []*domain.LambdaFunction{f})
		}
	}

	return AlgorithmResult{
		Name:        c.Name(),
		Groups:      finalGroups,
		Metrics:     app.CalculateMetrics(finalGroups),
		WallClockMs: float64(time.Since(start).Microseconds()) / 1000.0,
	}
}

// isDominated returns true if candidate is dominated by any label in the frontier.
func isDominated(frontier []*cspLabel, candidate *cspLabel) bool {
	for _, l := range frontier {
		if l.dominates(candidate) {
			return true
		}
	}
	return false
}

// pruneDominated removes any label from frontier that is dominated by newLabel.
func pruneDominated(frontier []*cspLabel, newLabel *cspLabel) []*cspLabel {
	result := frontier[:0]
	for _, l := range frontier {
		if !newLabel.dominates(l) {
			result = append(result, l)
		}
	}
	return result
}
