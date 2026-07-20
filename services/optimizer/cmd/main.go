package main

import (
	"context"
	"log/slog"
	"net"
	"os"
	"time"

	"google.golang.org/grpc"

	pb "github.com/Vaivaswat2244/OptiFuse_go/proto"
	"github.com/Vaivaswat2244/OptiFuse_go/services/optimizer/internal/algo"
	"github.com/Vaivaswat2244/OptiFuse_go/services/optimizer/internal/domain"
	"github.com/Vaivaswat2244/OptiFuse_go/shared/logger"
)

var log *slog.Logger

type server struct {
	pb.UnimplementedOptimizerServiceServer
}

func (s *server) Optimize(ctx context.Context, req *pb.OptimizeRequest) (*pb.OptimizeResponse, error) {
	app, err := graphToApp(req.Graph)
	if err != nil {
		log.Error("failed to convert graph to app", "error", err)
		return nil, err
	}

	log.Info("optimization started",
		"app", app.Name,
		"functions", len(app.Functions),
		"max_memory_mb", app.MaxMemoryMB,
		"max_latency_ms", app.MaxLatencyMS,
		"critical_path", app.CriticalPathIDs,
	)

	for _, f := range app.Functions {
		log.Debug("function node",
			"id", f.ID,
			"memory_mb", f.MemoryMB,
			"runtime_ms", f.RuntimeMs(),
			"children", len(f.Children),
		)
	}

	optimizers := []algo.Optimizer{
		&algo.NoFusion{},
		&algo.Singleton{},
		&algo.MinWCut{},
		&algo.GreedyTP{},
		&algo.CostlessCSP{},
		&algo.MtxILP{},
	}

	var results []*pb.AlgorithmResult
	var bestResult *pb.AlgorithmResult

	for _, o := range optimizers {
		algoStart := time.Now()
		r := o.Optimize(app)
		elapsed := time.Since(algoStart)

		log.Info("algorithm completed",
			"algorithm", r.Name,
			"feasible", r.Metrics.Feasible,
			"cost_usd", r.Metrics.TotalCostUSD,
			"latency_ms", r.Metrics.LatencyMS,
			"groups", len(r.Groups),
			"wall_clock_ms", elapsed.Milliseconds(),
			"error", r.Error,
		)

		var fusionGroups []*pb.FusionGroup
		for _, g := range r.Groups {
			ids := make([]string, len(g))
			totalMem := 0
			totalRuntime := 0
			for i, f := range g {
				ids[i] = f.ID
				totalMem += f.MemoryMB
				totalRuntime += f.RuntimeMs()
			}
			gbSec := (float64(totalMem) / 1024.0) * (float64(totalRuntime) / 1000.0)
			fusionGroups = append(fusionGroups, &pb.FusionGroup{
				FunctionIds:      ids,
				TotalMemoryMb:    int32(totalMem),
				TotalRuntimeMs:   int32(totalRuntime),
				ExecutionCostUsd: 0.00001667 * gbSec,
			})
		}

		pbResult := &pb.AlgorithmResult{
			Name:   r.Name,
			Groups: fusionGroups,
			Metrics: &pb.Metrics{
				TotalCostUsd: r.Metrics.TotalCostUSD,
				LatencyMs:    r.Metrics.LatencyMS,
				Feasible:     r.Metrics.Feasible,
				RuntimeMs:    r.WallClockMs,
			},
			Error:        r.Error != "",
			ErrorMessage: r.Error,
		}
		results = append(results, pbResult)

		if r.Metrics.Feasible && r.Error == "" {
			if bestResult == nil || r.Metrics.TotalCostUSD < bestResult.Metrics.TotalCostUsd {
				bestResult = pbResult
			}
		}
	}

	log.Info("optimization complete",
		"total_algorithms", len(results),
		"best", func() string {
			if bestResult != nil {
				return bestResult.Name
			}
			return "none"
		}(),
	)

	return &pb.OptimizeResponse{
		Plan: &pb.OptimizationPlan{
			Results:     results,
			Recommended: bestResult,
		},
	}, nil
}

func main() {
	log = logger.New("optimizer")

	port := os.Getenv("PORT")
	if port == "" {
		port = "50053"
	}

	lis, err := net.Listen("tcp", ":"+port)
	if err != nil {
		log.Error("failed to listen", "port", port, "error", err)
		os.Exit(1)
	}

	s := grpc.NewServer()
	pb.RegisterOptimizerServiceServer(s, &server{})

	log.Info("optimizer service started", "port", port)
	if err := s.Serve(lis); err != nil {
		log.Error("server error", "error", err)
		os.Exit(1)
	}
}

// suppress unused import if time is not used elsewhere
var _ = time.Now

// graphToApp converts a proto Graph into a domain.Application
// that the algorithms can work with.
func graphToApp(g *pb.Graph) (*domain.Application, error) {
	// First pass: create all LambdaFunction nodes
	fm := make(map[string]*domain.LambdaFunction, len(g.Nodes))
	for id, node := range g.Nodes {
		lf := &domain.LambdaFunction{
			ID:              id,
			Name:            node.Name,
			MemoryMB:        int(node.MemoryMb),
			TimeoutSec:      int(node.TimeoutSec),
			LoadFactor:      node.LoadFactor,
			DataOutBytes:    make(map[string]int64),
			AvgDurationMs:   node.AvgDurationMs,
			AvgMemoryUsedMB: node.AvgMemoryUsedMb,
			InvocationCount: node.InvocationCount,
			ErrorRate:       node.ErrorRate,
			P99LatencyMs:    node.P99LatencyMs,
			ColdStartRate:   node.ColdStartRate,
		}
		fm[id] = lf
	}

	// Second pass: wire edges
	for _, edge := range g.Edges {
		if parent, ok := fm[edge.From]; ok {
			if child, ok := fm[edge.To]; ok {
				parent.AddChild(child, edge.DataBytes)
			}
		}
	}

	// Build ordered function slice: critical path first, then rest
	cpSet := make(map[string]bool, len(g.CriticalPath))
	for _, id := range g.CriticalPath {
		cpSet[id] = true
	}
	funcs := make([]*domain.LambdaFunction, 0, len(fm))
	for _, id := range g.CriticalPath {
		if f, ok := fm[id]; ok {
			funcs = append(funcs, f)
		}
	}
	for id, f := range fm {
		if !cpSet[id] {
			funcs = append(funcs, f)
		}
	}

	var c *pb.Constraints
	if g.Constraints != nil {
		c = g.Constraints
	} else {
		c = &pb.Constraints{MaxMemoryMb: 1024, MaxLatencyMs: 30000, NetworkHopMs: 20}
	}

	return &domain.Application{
		Name:            g.Name,
		Functions:       funcs,
		CriticalPathIDs: g.CriticalPath,
		MaxMemoryMB:     int(c.MaxMemoryMb),
		MaxLatencyMS:    int(c.MaxLatencyMs),
		NetworkHopMS:    int(c.NetworkHopMs),
	}, nil
}
