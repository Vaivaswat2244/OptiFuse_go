package main

import (
	"strings"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	// Buckets span milliseconds to minutes on purpose. Five of the six algorithms
	// finish in under a millisecond on realistic graphs, while MtxILP is
	// exponential and runs under a 60s solver deadline — the default buckets,
	// which top out at 10s, would put every interesting MtxILP run in +Inf.
	algorithmDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name: "optifuse_algorithm_duration_seconds",
		Help: "Wall-clock time for a single fusion algorithm to run.",
		Buckets: []float64{
			0.0005, 0.001, 0.005, 0.01, 0.05, 0.1, 0.5, 1, 5, 15, 30, 60, 120,
		},
	}, []string{"algorithm"})

	// MtxILP failed on every single run for months without anyone noticing,
	// because a failing algorithm is indistinguishable from a losing one in the
	// UI — the other five still produce answers. This counter is the alert.
	algorithmFailures = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "optifuse_algorithm_failures_total",
		Help: "Algorithm runs that returned an error, by classified reason.",
	}, []string{"algorithm", "reason"})

	// Which algorithm actually wins in production. If one never wins it is
	// costing CPU for nothing; if the exact solver rarely beats the heuristics,
	// that is worth knowing before optimising it further.
	recommendations = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "optifuse_recommendation_total",
		Help: "Optimizations by the algorithm whose plan was recommended.",
	}, []string{"algorithm"})

	// Zero means fusion saved nothing over deploying every function separately.
	// A distribution clustered near zero means the product is not earning its keep.
	recommendationSavings = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "optifuse_recommendation_savings_ratio",
		Help:    "Fractional cost saving of the recommended plan against NoFusion.",
		Buckets: []float64{0, 0.05, 0.1, 0.2, 0.3, 0.4, 0.5, 0.6, 0.75, 0.9, 1},
	})

	optimizationsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "optifuse_optimizations_total",
		Help: "Optimization requests, by whether any feasible plan was found.",
	}, []string{"result"})
)

// algorithmSlugs maps an algorithm's display name to the stable identifier used
// as a metric label.
//
// The display names are UI strings — "MtxILP (Optimal)", "Greedy TP (GrTP)" —
// and using them directly would make every dashboard query and alert rule break
// the day someone retitles a column. These slugs are the same identifiers the
// Algorithm enum in proto/optimizer.proto already uses.
var algorithmSlugs = map[string]string{
	"NoFusion":          "no_fusion",
	"Singleton":         "singleton",
	"MinWCut Heuristic": "min_w_cut",
	"Greedy TP (GrTP)":  "greedy_tp",
	"Costless (CSP)":    "costless_csp",
	"MtxILP (Optimal)":  "mtx_ilp",
}

// algorithmSlug returns the metric label for an algorithm. An unknown name
// falls back to "unknown" rather than being passed through, so adding an
// algorithm without registering it here cannot mint unbounded label values.
func algorithmSlug(name string) string {
	if slug, ok := algorithmSlugs[name]; ok {
		return slug
	}
	return "unknown"
}

// classifyFailure maps a free-text algorithm error onto a bounded label set.
//
// The raw strings must never become label values: the MtxILP solver error
// embeds a temp file path that differs every run, so using it directly would
// create an unbounded number of time series and eventually take Prometheus down.
func classifyFailure(msg string) string {
	m := strings.ToLower(msg)
	switch {
	case strings.Contains(m, "critical path is empty"):
		return "no_critical_path"
	case strings.Contains(m, "no root function"):
		return "no_root_function"
	case strings.Contains(m, "solver not available"):
		return "solver_missing"
	case strings.Contains(m, "glpsol") || strings.Contains(m, "cbc") || strings.Contains(m, "solver status"):
		return "solver_error"
	case strings.Contains(m, "infeasible") || strings.Contains(m, "exceeds max"):
		return "infeasible"
	case strings.Contains(m, "no feasible partitioning"):
		return "no_feasible_partition"
	default:
		return "other"
	}
}
