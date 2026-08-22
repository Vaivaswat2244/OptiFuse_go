package reqid

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

func TestContextRoundTrip(t *testing.T) {
	ctx := NewContext(context.Background(), "abc123")
	if got := FromContext(ctx); got != "abc123" {
		t.Errorf("FromContext = %q, want %q", got, "abc123")
	}
}

func TestFromContext_EmptyWhenUnset(t *testing.T) {
	if got := FromContext(context.Background()); got != "" {
		t.Errorf("FromContext on a bare context = %q, want empty", got)
	}
}

func TestGenerate_IsUniqueAndHex(t *testing.T) {
	seen := make(map[string]bool, 100)
	for i := 0; i < 100; i++ {
		id := Generate()
		if len(id) != 16 {
			t.Fatalf("id %q has length %d, want 16", id, len(id))
		}
		if strings.Trim(id, "0123456789abcdef") != "" {
			t.Fatalf("id %q is not hex", id)
		}
		if seen[id] {
			t.Fatalf("Generate produced a duplicate: %q", id)
		}
		seen[id] = true
	}
}

// An ID supplied by the caller must survive, otherwise a trace is broken at
// every hop and the gateway's ID never reaches the optimizer.
func TestServerInterceptor_PreservesInboundID(t *testing.T) {
	md := metadata.Pairs(HeaderKey, "from-caller")
	ctx := metadata.NewIncomingContext(context.Background(), md)

	var seen string
	_, err := ServerInterceptor()(ctx, nil, &grpc.UnaryServerInfo{},
		func(ctx context.Context, _ interface{}) (interface{}, error) {
			seen = FromContext(ctx)
			return nil, nil
		})
	if err != nil {
		t.Fatalf("interceptor returned error: %v", err)
	}
	if seen != "from-caller" {
		t.Errorf("handler saw %q, want the inbound id %q", seen, "from-caller")
	}
}

// A service called directly — by grpcurl, or a future second caller — should
// still produce correlatable logs rather than untagged ones.
func TestServerInterceptor_GeneratesWhenAbsent(t *testing.T) {
	var seen string
	_, err := ServerInterceptor()(context.Background(), nil, &grpc.UnaryServerInfo{},
		func(ctx context.Context, _ interface{}) (interface{}, error) {
			seen = FromContext(ctx)
			return nil, nil
		})
	if err != nil {
		t.Fatalf("interceptor returned error: %v", err)
	}
	if seen == "" {
		t.Error("handler saw no request id; one should have been generated")
	}
}

func TestClientInterceptor_InjectsIntoOutgoingMetadata(t *testing.T) {
	ctx := NewContext(context.Background(), "trace-me")

	var got string
	err := ClientInterceptor()(ctx, "/svc/Method", nil, nil, nil,
		func(ctx context.Context, _ string, _, _ interface{}, _ *grpc.ClientConn, _ ...grpc.CallOption) error {
			md, ok := metadata.FromOutgoingContext(ctx)
			if !ok {
				t.Fatal("no outgoing metadata was attached")
			}
			if v := md.Get(HeaderKey); len(v) > 0 {
				got = v[0]
			}
			return nil
		})
	if err != nil {
		t.Fatalf("interceptor returned error: %v", err)
	}
	if got != "trace-me" {
		t.Errorf("outgoing metadata carried %q, want %q", got, "trace-me")
	}
}

// The full path a real request takes: gateway context -> client interceptor ->
// wire -> server interceptor -> handler context. If this breaks, IDs stop
// matching across services and the whole feature is silently useless.
func TestPropagation_ClientToServer(t *testing.T) {
	clientCtx := NewContext(context.Background(), "end-to-end")

	var handlerSaw string
	err := ClientInterceptor()(clientCtx, "/svc/Method", nil, nil, nil,
		func(outCtx context.Context, _ string, _, _ interface{}, _ *grpc.ClientConn, _ ...grpc.CallOption) error {
			// Outgoing metadata on the caller becomes incoming metadata on the callee.
			md, _ := metadata.FromOutgoingContext(outCtx)
			serverCtx := metadata.NewIncomingContext(context.Background(), md)

			_, err := ServerInterceptor()(serverCtx, nil, &grpc.UnaryServerInfo{},
				func(ctx context.Context, _ interface{}) (interface{}, error) {
					handlerSaw = FromContext(ctx)
					return nil, nil
				})
			return err
		})
	if err != nil {
		t.Fatalf("propagation failed: %v", err)
	}
	if handlerSaw != "end-to-end" {
		t.Errorf("downstream handler saw %q, want %q", handlerSaw, "end-to-end")
	}
}

func TestLogger_TagsWithRequestID(t *testing.T) {
	var buf strings.Builder
	base := slog.New(slog.NewTextHandler(&buf, nil))

	Logger(NewContext(context.Background(), "tagged"), base).Info("hello")

	if !strings.Contains(buf.String(), LogKey+"=tagged") {
		t.Errorf("log line missing request id: %s", buf.String())
	}
}

func TestLogger_UnchangedWithoutID(t *testing.T) {
	base := slog.New(slog.NewTextHandler(io.Discard, nil))
	if Logger(context.Background(), base) != base {
		t.Error("expected the base logger back untouched when no id is set")
	}
}
