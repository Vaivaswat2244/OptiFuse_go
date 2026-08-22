package main

import (
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	httpRequests = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "http_server_requests_total",
		Help: "Total HTTP requests handled, by method, route and status.",
	}, []string{"method", "route", "status"})

	httpDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name: "http_server_request_duration_seconds",
		Help: "Time to handle an HTTP request, by method and route.",
		// /api/simulate/live/ runs the whole pipeline including a GitHub round
		// trip and a CloudWatch query, so it lives in seconds while every other
		// route is in milliseconds. The default buckets top out at 10s, which
		// would hide a slow enrichment entirely.
		Buckets: []float64{0.005, 0.025, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60},
	}, []string{"method", "route"})
)

// metricsMiddleware records RED metrics per route.
//
// The label is gin's route *pattern* (c.FullPath), not the request path: using
// the raw path would create a distinct time series per repository name, which
// grows without bound and is the classic way to take Prometheus down. Requests
// that match no route are bucketed together for the same reason — otherwise any
// scanner probing random URLs mints series at will.
func metricsMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()

		route := c.FullPath()
		if route == "" {
			route = "unmatched"
		}

		httpDuration.WithLabelValues(c.Request.Method, route).Observe(time.Since(start).Seconds())
		httpRequests.WithLabelValues(
			c.Request.Method, route, strconv.Itoa(c.Writer.Status()),
		).Inc()
	}
}
