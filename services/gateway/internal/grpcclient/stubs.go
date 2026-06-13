package grpcclient

import (
	"context"
	"fmt"

	"google.golang.org/grpc"

	pb "github.com/Vaivaswat2244/OptiFuse_go/proto"
)

// ── Parser client ─────────────────────────────────────────────────────────────

type ParserClient struct {
	client pb.ParserServiceClient
}

func NewParserClient(conn *grpc.ClientConn) *ParserClient {
	return &ParserClient{client: pb.NewParserServiceClient(conn)}
}

// Parse sends serverless.yml bytes to the parser service.
// Returns the parsed Graph proto and any warnings.
func (p *ParserClient) Parse(ctx context.Context, repoName string, yamlContent []byte) (*pb.Graph, []string, error) {
	resp, err := p.client.Parse(ctx, &pb.ParseRequest{
		RepoName:    repoName,
		YamlContent: yamlContent,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("parser.Parse: %w", err)
	}
	return resp.Graph, resp.Warnings, nil
}

// ── Enricher client ───────────────────────────────────────────────────────────

type EnricherClient struct {
	client pb.EnricherServiceClient
}

func NewEnricherClient(conn *grpc.ClientConn) *EnricherClient {
	return &EnricherClient{client: pb.NewEnricherServiceClient(conn)}
}

// Enrich sends a Graph to the enricher service which fills in CloudWatch telemetry.
func (e *EnricherClient) Enrich(ctx context.Context, graph *pb.Graph, roleARN, externalID, serviceName, stage string) (*pb.Graph, error) {
	resp, err := e.client.Enrich(ctx, &pb.EnrichRequest{
		Graph:       graph,
		RoleArn:     roleARN,
		ExternalId:  externalID,
		ServiceName: serviceName,
		Stage:       stage,
	})
	if err != nil {
		return nil, fmt.Errorf("enricher.Enrich: %w", err)
	}
	return resp.Graph, nil
}

// ── Optimizer client ──────────────────────────────────────────────────────────

type OptimizerClient struct {
	client pb.OptimizerServiceClient
}

func NewOptimizerClient(conn *grpc.ClientConn) *OptimizerClient {
	return &OptimizerClient{client: pb.NewOptimizerServiceClient(conn)}
}

// Optimize runs all 6 fusion algorithms on the enriched graph.
func (o *OptimizerClient) Optimize(ctx context.Context, graph *pb.Graph) (*pb.OptimizationPlan, error) {
	resp, err := o.client.Optimize(ctx, &pb.OptimizeRequest{
		Graph: graph,
	})
	if err != nil {
		return nil, fmt.Errorf("optimizer.Optimize: %w", err)
	}
	return resp.Plan, nil
}
