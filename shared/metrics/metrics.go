// Package metrics exposes Prometheus instrumentation shared by every service:
// a /metrics endpoint and RED (rate, errors, duration) metrics for gRPC traffic
// in both directions.
//
// Service-specific metrics do not belong here. They are declared in the service
// that emits them, so a binary only exposes series it can actually populate —
// the parser advertising an always-zero optimizer metric is worse than useless,
// because a dashboard cannot distinguish "never ran" from "ran and was zero".
package metrics

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"google.golang.org/grpc"
	"google.golang.org/grpc/status"
)

// Labels deliberately exclude the service name: Prometheus attaches `job` and
// `instance` from the scrape config, so carrying our own copy would be
// redundant cardinality.
var (
	grpcServerRequests = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "grpc_server_requests_total",
		Help: "Total gRPC requests handled, by method and response code.",
	}, []string{"method", "code"})

	grpcServerDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "grpc_server_request_duration_seconds",
		Help:    "Time to handle a gRPC request, by method.",
		Buckets: prometheus.DefBuckets,
	}, []string{"method"})

	grpcClientRequests = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "grpc_client_requests_total",
		Help: "Total outbound gRPC requests, by method and response code.",
	}, []string{"method", "code"})

	grpcClientDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "grpc_client_request_duration_seconds",
		Help:    "Time for an outbound gRPC call to return, by method.",
		Buckets: prometheus.DefBuckets,
	}, []string{"method"})
)

// Port returns the port the metrics endpoint should listen on.
//
// Metrics are served separately from application traffic so they can be scraped
// inside the cluster without being reachable through the public ingress.
func Port() string {
	if p := os.Getenv("METRICS_PORT"); p != "" {
		return p
	}
	return "9090"
}

// Serve starts the metrics endpoint in the background and returns a function
// that shuts it down.
//
// A failure to bind is logged rather than fatal: losing observability should
// degrade the deployment, not take the service offline.
func Serve(port string, log *slog.Logger) func(context.Context) error {
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())

	srv := &http.Server{Addr: ":" + port, Handler: mux}

	go func() {
		log.Info("metrics endpoint listening", "port", port, "path", "/metrics")
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("metrics server stopped", "error", err)
		}
	}()

	return srv.Shutdown
}

// GRPCServerInterceptor records RED metrics for inbound gRPC calls.
func GRPCServerInterceptor() grpc.UnaryServerInterceptor {
	return func(
		ctx context.Context,
		req interface{},
		info *grpc.UnaryServerInfo,
		handler grpc.UnaryHandler,
	) (interface{}, error) {
		start := time.Now()
		resp, err := handler(ctx, req)

		grpcServerDuration.WithLabelValues(info.FullMethod).Observe(time.Since(start).Seconds())
		grpcServerRequests.WithLabelValues(info.FullMethod, status.Code(err).String()).Inc()

		return resp, err
	}
}

// GRPCClientInterceptor records RED metrics for outbound gRPC calls.
//
// Worth having separately from the server side: it attributes latency to the
// caller's view, which includes connection setup and queuing that the callee
// never sees.
func GRPCClientInterceptor() grpc.UnaryClientInterceptor {
	return func(
		ctx context.Context,
		method string,
		req, reply interface{},
		cc *grpc.ClientConn,
		invoker grpc.UnaryInvoker,
		opts ...grpc.CallOption,
	) error {
		start := time.Now()
		err := invoker(ctx, method, req, reply, cc, opts...)

		grpcClientDuration.WithLabelValues(method).Observe(time.Since(start).Seconds())
		grpcClientRequests.WithLabelValues(method, status.Code(err).String()).Inc()

		return err
	}
}
