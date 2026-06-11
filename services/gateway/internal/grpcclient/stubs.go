package grpcclient

import (
	"context"
	"fmt"

	"google.golang.org/grpc"
)

// ── Parser client ─────────────────────────────────────────────────────────────

type ParserClient struct {
	conn *grpc.ClientConn
}

func NewParserClient(conn *grpc.ClientConn) *ParserClient {
	return &ParserClient{conn: conn}
}

// Parse sends a serverless.yml to the parser service and returns a graph.
// This is a stub — will be replaced with generated proto client once
// we run protoc.
func (p *ParserClient) Parse(ctx context.Context, repoName string, yamlContent []byte) (map[string]any, []string, error) {
	// TODO: replace with generated gRPC call
	// parserpb.NewParserServiceClient(p.conn).Parse(ctx, &parserpb.ParseRequest{...})
	return nil, nil, fmt.Errorf("parser service not yet implemented — run make proto")
}

// ── Enricher client ───────────────────────────────────────────────────────────

type EnricherClient struct {
	conn *grpc.ClientConn
}

func NewEnricherClient(conn *grpc.ClientConn) *EnricherClient {
	return &EnricherClient{conn: conn}
}

// Enrich sends a graph to the enricher service and returns an enriched graph.
// Stub — will be replaced with generated proto client.
func (e *EnricherClient) Enrich(ctx context.Context, graph map[string]any, roleARN, externalID, serviceName, stage string) (map[string]any, error) {
	// TODO: replace with generated gRPC call
	return nil, fmt.Errorf("enricher service not yet implemented — run make proto")
}

// ── Optimizer client ──────────────────────────────────────────────────────────

type OptimizerClient struct {
	conn *grpc.ClientConn
}

func NewOptimizerClient(conn *grpc.ClientConn) *OptimizerClient {
	return &OptimizerClient{conn: conn}
}

// Optimize sends an enriched graph to the optimizer and returns the plan.
// Stub — will be replaced with generated proto client.
func (o *OptimizerClient) Optimize(ctx context.Context, graph map[string]any) (any, error) {
	// TODO: replace with generated gRPC call
	return nil, fmt.Errorf("optimizer service not yet implemented — run make proto")
}
