// Package reqid propagates a request ID across service boundaries so the log
// lines produced by one user request can be found together.
//
// Without it the only way to correlate a gateway line with the parser and
// optimizer lines it caused is by timestamp, which stops working the moment two
// requests overlap — and overlapping is the normal case, not the exception.
//
// This is also the seam OpenTelemetry would slot into later: the propagation
// points are the same, so adopting tracing becomes a swap rather than a rewrite.
package reqid

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

// HeaderKey is the HTTP header and gRPC metadata key carrying the ID.
// gRPC metadata keys are case-insensitive and normalised to lower case.
const HeaderKey = "x-request-id"

// LogKey is the structured-log attribute name.
const LogKey = "request_id"

// contextKey is unexported so no other package can write to this slot by
// accident — a collision here would silently mix up request identities.
type contextKey struct{}

// Generate returns a new random request ID.
//
// 8 bytes of randomness, not a UUID: this only has to be unique among requests
// in flight across a handful of services, and avoiding a UUID dependency keeps
// the module graph smaller. crypto/rand cannot fail in practice on Linux, and
// an all-zero ID on the impossible error path is still better than panicking in
// a logging helper.
func Generate() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// NewContext returns ctx carrying id.
func NewContext(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, contextKey{}, id)
}

// FromContext returns the request ID, or "" if none is set.
func FromContext(ctx context.Context) string {
	id, _ := ctx.Value(contextKey{}).(string)
	return id
}

// Logger returns base tagged with the context's request ID, or base unchanged
// when there is none — so callers never have to check.
func Logger(ctx context.Context, base *slog.Logger) *slog.Logger {
	if id := FromContext(ctx); id != "" {
		return base.With(LogKey, id)
	}
	return base
}

// ServerInterceptor lifts the request ID out of incoming gRPC metadata into the
// context.
//
// It generates one when the caller supplied none, so a service invoked directly
// — by grpcurl, or a future second caller — still produces correlatable logs
// rather than untagged ones.
func ServerInterceptor() grpc.UnaryServerInterceptor {
	return func(
		ctx context.Context,
		req interface{},
		info *grpc.UnaryServerInfo,
		handler grpc.UnaryHandler,
	) (interface{}, error) {
		id := ""
		if md, ok := metadata.FromIncomingContext(ctx); ok {
			if values := md.Get(HeaderKey); len(values) > 0 {
				id = values[0]
			}
		}
		if id == "" {
			id = Generate()
		}
		return handler(NewContext(ctx, id), req)
	}
}

// ClientInterceptor writes the context's request ID into outgoing gRPC metadata.
func ClientInterceptor() grpc.UnaryClientInterceptor {
	return func(
		ctx context.Context,
		method string,
		req, reply interface{},
		cc *grpc.ClientConn,
		invoker grpc.UnaryInvoker,
		opts ...grpc.CallOption,
	) error {
		if id := FromContext(ctx); id != "" {
			ctx = metadata.AppendToOutgoingContext(ctx, HeaderKey, id)
		}
		return invoker(ctx, method, req, reply, cc, opts...)
	}
}
