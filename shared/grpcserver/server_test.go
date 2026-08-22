package grpcserver

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"
)

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// startServer runs a server on an ephemeral port and returns a health client
// plus a function that triggers shutdown and waits for it to finish.
func startServer(t *testing.T) (healthpb.HealthClient, func()) {
	t.Helper()

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	srv := New(quietLogger())
	srv.DrainDelay = 10 * time.Millisecond
	srv.ShutdownTimeout = 2 * time.Second

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.serve(ctx, lis, "test") }()

	conn, err := grpc.NewClient(lis.Addr().String(),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	// Idempotent: both the test body and t.Cleanup may call this, and `done`
	// only ever carries one value, so a second naive receive would block forever.
	var once sync.Once
	stop := func() {
		once.Do(func() {
			cancel()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Error("server did not shut down within 5s")
			}
		})
	}
	t.Cleanup(stop)

	return healthpb.NewHealthClient(conn), stop
}

// Kubernetes' built-in gRPC probe checks the empty service name, so that is the
// status that has to be SERVING for a pod to receive traffic.
func TestServe_ReportsServing(t *testing.T) {
	client, _ := startServer(t)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	resp, err := client.Check(ctx, &healthpb.HealthCheckRequest{})
	if err != nil {
		t.Fatalf("health check failed: %v", err)
	}
	if resp.Status != healthpb.HealthCheckResponse_SERVING {
		t.Errorf("status = %v, want SERVING", resp.Status)
	}
}

// The ordering that matters on shutdown: report NOT_SERVING *first* and keep
// answering for a moment, so load balancers stop routing before the port closes.
// Flipping these produces exactly the dropped requests this package exists to
// prevent.
func TestShutdown_ReportsNotServingBeforeClosing(t *testing.T) {
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	srv := New(quietLogger())
	srv.DrainDelay = 500 * time.Millisecond // long enough to observe the window
	srv.ShutdownTimeout = 2 * time.Second

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.serve(ctx, lis, "test") }()

	conn, err := grpc.NewClient(lis.Addr().String(),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	client := healthpb.NewHealthClient(conn)

	checkCtx, checkCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer checkCancel()
	if _, err := client.Check(checkCtx, &healthpb.HealthCheckRequest{}); err != nil {
		t.Fatalf("expected SERVING before shutdown: %v", err)
	}

	cancel() // stand in for SIGTERM

	// Mid-drain the server must still answer, but say NOT_SERVING.
	time.Sleep(150 * time.Millisecond)
	resp, err := client.Check(checkCtx, &healthpb.HealthCheckRequest{})
	if err != nil {
		t.Fatalf("server stopped answering during drain, so the drain window is useless: %v", err)
	}
	if resp.Status != healthpb.HealthCheckResponse_NOT_SERVING {
		t.Errorf("during drain status = %v, want NOT_SERVING", resp.Status)
	}

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("server did not shut down")
	}
}

func TestServe_ReturnsAfterContextCancelled(t *testing.T) {
	_, stop := startServer(t)
	stop() // fails the test if shutdown does not complete
}

// A panic must become an error for the caller rather than killing the process:
// graphs are user-supplied, so one malformed topology would otherwise take the
// service down for everyone.
func TestRecoveryInterceptor_ConvertsPanicToError(t *testing.T) {
	interceptor := recoveryInterceptor(quietLogger())

	_, err := interceptor(
		context.Background(),
		nil,
		&grpc.UnaryServerInfo{FullMethod: "/test.Service/Boom"},
		func(context.Context, interface{}) (interface{}, error) {
			panic("topology has a cycle")
		},
	)

	if err == nil {
		t.Fatal("expected an error, got nil — the panic escaped")
	}
	if status.Code(err) != codes.Internal {
		t.Errorf("code = %v, want Internal", status.Code(err))
	}
}

func TestRecoveryInterceptor_PassesThroughNormalErrors(t *testing.T) {
	interceptor := recoveryInterceptor(quietLogger())
	sentinel := errors.New("ordinary failure")

	_, err := interceptor(
		context.Background(),
		nil,
		&grpc.UnaryServerInfo{FullMethod: "/test.Service/Fine"},
		func(context.Context, interface{}) (interface{}, error) {
			return nil, sentinel
		},
	)

	if !errors.Is(err, sentinel) {
		t.Errorf("err = %v, want the handler's own error unchanged", err)
	}
}
