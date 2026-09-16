package algo

// Pareto analysis over the candidate partitions.
//
// OptiFuse used to answer "which algorithm won", picking the cheapest feasible
// result. That question turned out to be the wrong one. Fusion trades execution
// cost against invocation count and latency, and which side of that trade a user
// wants depends on things the tool cannot see: whether they are optimising a
// bill or a p99, how much operational coupling they will accept, whether the
// workload is latency-sensitive at all.
//
// So the output is the set of options that are not strictly worse than some
// other option, and the mechanism behind each, leaving the choice where it
// belongs. NoFusion and Singleton stop being baselines to beat and become the
// endpoints that bound the space.

// Dominates reports whether a is at least as good as b on both cost and
// latency, and strictly better on at least one.
//
// A dominated candidate is never worth choosing: something else is cheaper or
// faster and no worse on the other axis. Saying so is more useful than ranking,
// because "doing nothing is strictly worse than this" is a stronger claim than
// "this scored highest".
func Dominates(a, b AlgorithmResult) bool {
	if !a.Metrics.Feasible || !b.Metrics.Feasible {
		return false
	}
	noWorse := a.Metrics.TotalCostUSD <= b.Metrics.TotalCostUSD &&
		a.Metrics.LatencyMS <= b.Metrics.LatencyMS
	strictlyBetter := a.Metrics.TotalCostUSD < b.Metrics.TotalCostUSD ||
		a.Metrics.LatencyMS < b.Metrics.LatencyMS
	return noWorse && strictlyBetter
}

// MarkFrontier flags every feasible result that some other result dominates.
//
// Ties matter here. Two algorithms often produce the identical partition, and
// neither dominates the other, so both stay on the frontier rather than one
// arbitrarily shadowing the other.
func MarkFrontier(results []AlgorithmResult) []AlgorithmResult {
	for i := range results {
		results[i].Dominated = false
		results[i].DominatedBy = ""

		if !results[i].Metrics.Feasible || results[i].Error != "" {
			continue
		}
		for j := range results {
			if i == j || results[j].Error != "" {
				continue
			}
			if Dominates(results[j], results[i]) {
				results[i].Dominated = true
				results[i].DominatedBy = results[j].Name
				break
			}
		}
	}
	return results
}

// Extremes returns the cheapest and fastest feasible candidates, and whether a
// single option is best on both.
//
// When one candidate is both cheapest and fastest there is no trade to present
// and the tool can simply say so. When they differ, the gap between them is the
// decision the user has to make.
func Extremes(results []AlgorithmResult) (cheapest, fastest *AlgorithmResult, unambiguous bool) {
	for i := range results {
		r := &results[i]
		if !r.Metrics.Feasible || r.Error != "" {
			continue
		}
		if cheapest == nil || r.Metrics.TotalCostUSD < cheapest.Metrics.TotalCostUSD {
			cheapest = r
		}
		if fastest == nil || r.Metrics.LatencyMS < fastest.Metrics.LatencyMS {
			fastest = r
		}
	}
	if cheapest == nil || fastest == nil {
		return cheapest, fastest, false
	}
	unambiguous = cheapest.Metrics.TotalCostUSD == fastest.Metrics.TotalCostUSD &&
		cheapest.Metrics.LatencyMS == fastest.Metrics.LatencyMS
	return cheapest, fastest, unambiguous
}
