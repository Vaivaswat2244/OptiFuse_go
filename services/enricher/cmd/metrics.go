package main

import (
	"strings"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	// A function that CloudWatch returned nothing for keeps whatever estimate the
	// serverless.yml supplied, so a run can silently mix measured durations with
	// hand-written guesses and still look entirely successful. This ratio is the
	// only signal that has happened.
	enrichmentFunctions = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "optifuse_enrichment_functions_total",
		Help: "Functions seen during enrichment, by whether telemetry was found.",
	}, []string{"result"})

	enrichmentRequests = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "optifuse_enrichment_requests_total",
		Help: "Enrichment requests, by outcome.",
	}, []string{"result"})

	// Logs Insights is a poll-until-complete API, so this is dominated by query
	// scheduling rather than data volume and is the slowest hop in the pipeline.
	enrichmentDuration = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "optifuse_enrichment_duration_seconds",
		Help:    "Time to assume the customer role and complete the CloudWatch query.",
		Buckets: []float64{0.5, 1, 2, 5, 10, 20, 30, 60},
	})
)

// classifyEnrichmentError maps an AWS error onto a bounded label set.
//
// Raw AWS messages carry request IDs and ARNs that differ every call, so using
// them as label values would create unbounded cardinality. These four buckets
// also happen to be the four distinct fixes: the role ARN is wrong, the trust
// policy does not admit us, we have no base credentials, or the log groups do
// not exist.
func classifyEnrichmentError(msg string) string {
	m := strings.ToLower(msg)
	switch {
	case strings.Contains(m, "not authorized to perform: sts:assumerole"):
		return "assume_role_denied"
	case strings.Contains(m, "no ec2 imds role found") || strings.Contains(m, "failed to refresh cached credentials"):
		return "no_base_credentials"
	case strings.Contains(m, "externalid") || strings.Contains(m, "external id"):
		return "external_id_mismatch"
	case strings.Contains(m, "resourcenotfound") || strings.Contains(m, "log group"):
		return "log_group_not_found"
	case strings.Contains(m, "context deadline exceeded") || strings.Contains(m, "timeout"):
		return "timeout"
	default:
		return "other"
	}
}
