// Package grpcserver provides the common startup path for OptiFuse's gRPC
// services: the interceptor chain, health reporting, and graceful shutdown.
//
// The three gRPC services previously each carried their own copy of this
// boilerplate and had already drifted apart — only the optimizer installed a
// panic-recovery interceptor, so a panic in the parser or enricher took the
// whole container down. Keeping it in one place means a fix lands everywhere.
package grpcserver

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os/signal"
	"runtime/debug"
	"syscall"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/reflection"
	"google.golang.org/grpc/status"

	"github.com/Vaivaswat2244/OptiFuse_go/shared/metrics"
	"github.com/Vaivaswat2244/OptiFuse_go/shared/reqid"
)

const (
	// defaultDrainDelay is how long to keep serving after SIGTERM before
	// shutting down.
	//
	// Kubernetes removes a pod from its Service endpoints *concurrently* with
	// sending SIGTERM, not before it, and endpoint propagation to every kube-proxy
	// takes a moment. Exiting immediately therefore drops requests that were
	// routed to us microseconds earlier. Reporting NOT_SERVING and then waiting
	// lets that propagation finish.
	defaultDrainDelay = 2 * time.Second

	// defaultShutdownTimeout bounds the wait for in-flight RPCs to finish. Kept
	// under Kubernetes' default terminationGracePeriodSeconds of 30 so we exit
	// on our own terms rather than being SIGKILLed mid-request.
	defaultShutdownTimeout = 25 * time.Second
)

// Server wraps a *grpc.Server with health reporting and lifecycle management.
type Server struct {
	grpc   *grpc.Server
	health *health.Server
	log    *slog.Logger

	DrainDelay      time.Duration
	ShutdownTimeout time.Duration
}

// New builds a gRPC server with the standard interceptor chain and a registered
// health service.
//
// Chain order is reqid → metrics → extra → recovery, outermost first.
//
// reqid runs first so every later interceptor and the handler itself can see the
// request ID. Recovery must be innermost so that metrics observes the Internal
// error it produces: were the order reversed, a panicking handler would unwind
// straight through the metrics interceptor and the request would vanish from the
// counters entirely — exactly the request you most want to see on a dashboard.
func New(log *slog.Logger, extra ...grpc.UnaryServerInterceptor) *Server {
	chain := []grpc.UnaryServerInterceptor{
		reqid.ServerInterceptor(),
		metrics.GRPCServerInterceptor(),
	}
	chain = append(chain, extra...)
	chain = append(chain, recoveryInterceptor(log))

	s := grpc.NewServer(grpc.ChainUnaryInterceptor(chain...))

	h := health.NewServer()
	healthpb.RegisterHealthServer(s, h)

	// Reflection lets grpcurl introspect the service without a local copy of the
	// protos, which is how these are debugged in a cluster. These services are
	// never publicly routable — only the gateway is.
	reflection.Register(s)

	return &Server{
		grpc:            s,
		health:          h,
		log:             log,
		DrainDelay:      defaultDrainDelay,
		ShutdownTimeout: defaultShutdownTimeout,
	}
}

// GRPC returns the underlying server so the caller can register its service.
func (s *Server) GRPC() *grpc.Server { return s.grpc }

// Health returns the health server, for services that need to report
// NOT_SERVING while a dependency is unavailable.
func (s *Server) Health() *health.Server { return s.health }

// Serve listens on port and blocks until SIGTERM or SIGINT, then drains.
//
// The empty service name "" is the conventional overall-server status that
// Kubernetes' built-in gRPC probe checks.
func (s *Server) Serve(port string) error {
	lis, err := net.Listen("tcp", ":"+port)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", port, err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	// Started here rather than in serve() so tests driving the lifecycle directly
	// do not contend for the metrics port.
	stopMetrics := metrics.Serve(metrics.Port(), s.log)
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := stopMetrics(shutdownCtx); err != nil {
			s.log.Warn("metrics server shutdown", "error", err)
		}
	}()

	return s.serve(ctx, lis, port)
}

// serve is Serve without the signal wiring, so tests can drive the lifecycle by
// cancelling a context rather than delivering real signals to the test process.
func (s *Server) serve(ctx context.Context, lis net.Listener, port string) error {
	errCh := make(chan error, 1)
	go func() {
		s.health.SetServingStatus("", healthpb.HealthCheckResponse_SERVING)
		s.log.Info("grpc server listening", "port", port)
		if err := s.grpc.Serve(lis); err != nil {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return fmt.Errorf("serve: %w", err)
	case <-ctx.Done():
		s.shutdown()
		return nil
	}
}

func (s *Server) shutdown() {
	s.log.Info("shutdown signal received, draining",
		"drain_delay", s.DrainDelay, "timeout", s.ShutdownTimeout)

	// Fail readiness first so load balancers stop sending new work, then wait
	// for that to propagate before refusing connections.
	s.health.SetServingStatus("", healthpb.HealthCheckResponse_NOT_SERVING)
	time.Sleep(s.DrainDelay)

	done := make(chan struct{})
	go func() {
		s.grpc.GracefulStop()
		close(done)
	}()

	select {
	case <-done:
		s.log.Info("graceful shutdown complete")
	case <-time.After(s.ShutdownTimeout):
		// A request is wedged. Better to drop it than be SIGKILLed with no log line.
		s.log.Warn("graceful shutdown timed out, forcing stop")
		s.grpc.Stop()
	}
}

// recoveryInterceptor turns a panic in any handler into a gRPC error instead of
// letting it kill the process. Graphs are user-supplied — a malformed topology
// must not take the service down for everyone else.
func recoveryInterceptor(log *slog.Logger) grpc.UnaryServerInterceptor {
	return func(
		ctx context.Context,
		req interface{},
		info *grpc.UnaryServerInfo,
		handler grpc.UnaryHandler,
	) (resp interface{}, err error) {
		defer func() {
			if r := recover(); r != nil {
				log.Error("panic recovered in handler",
					"method", info.FullMethod,
					"panic", r,
					"stack", string(debug.Stack()),
				)
				err = status.Errorf(codes.Internal, "internal error processing %s", info.FullMethod)
			}
		}()
		return handler(ctx, req)
	}
}
