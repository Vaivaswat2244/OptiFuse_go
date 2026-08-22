package handlers

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// GitHub is the dominant term in end-to-end latency for a simulation: the whole
// pipeline typically runs in 300-700ms, and almost all of it is this call. It is
// also outside our control and rate limited, so it is the first thing to check
// when the API gets slow.
var githubDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
	Name:    "optifuse_github_api_duration_seconds",
	Help:    "Time for a GitHub API call to return, by operation.",
	Buckets: []float64{0.05, 0.1, 0.25, 0.5, 1, 2, 5, 10},
}, []string{"operation", "result"})
