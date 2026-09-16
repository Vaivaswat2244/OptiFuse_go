package main

import (
	"context"
	"log/slog"
	"os"
	"time"

	pb "github.com/Vaivaswat2244/OptiFuse_go/proto"
	"github.com/Vaivaswat2244/OptiFuse_go/services/optimizer/internal/algo"
	"github.com/Vaivaswat2244/OptiFuse_go/services/optimizer/internal/domain"
	"github.com/Vaivaswat2244/OptiFuse_go/shared/grpcserver"
	"github.com/Vaivaswat2244/OptiFuse_go/shared/logger"
	"github.com/Vaivaswat2244/OptiFuse_go/shared/reqid"
)

var log *slog.Logger

// Set at build time via -ldflags "-X main.version=... -X main.commit=...".
// Logged at startup so it is always possible to tell which build a running pod
// is actually executing.
var (
	version = "dev"
	commit  = "unknown"
)

type server struct {
	pb.UnimplementedOptimizerServiceServer
}

func (s *server) Optimize(ctx context.Context, req *pb.OptimizeRequest) (*pb.OptimizeResponse, error) {
	// Shadows the package logger so every line below carries the caller's
	// request ID without touching the individual log calls.
	log := reqid.Logger(ctx, log)

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

	// Every algorithm runs first, then the frontier is computed across all of
	// them. Dominance is a property of the set, not of any single result, so it
	// cannot be decided while they are still being produced.
	domainResults := make([]algo.AlgorithmResult, 0, len(optimizers))
	noFusionCost := 0.0

	for _, o := range optimizers {
		algoStart := time.Now()
		r := o.Optimize(app)
		elapsed := time.Since(algoStart)
		r.WallClockMs = float64(elapsed.Microseconds()) / 1000.0
		if r.Error == "" {
			r.Tradeoff = app.CalculateTradeoff(r.Groups)
		}
		domainResults = append(domainResults, r)

		algorithmDuration.WithLabelValues(algorithmSlug(r.Name)).Observe(elapsed.Seconds())
		if r.Error != "" {
			algorithmFailures.WithLabelValues(algorithmSlug(r.Name), classifyFailure(r.Error)).Inc()
		}
		if r.Name == "NoFusion" && r.Error == "" {
			noFusionCost = r.Metrics.TotalCostUSD
		}

		log.Info("algorithm completed",
			"algorithm", r.Name,
			"feasible", r.Metrics.Feasible,
			"cost_usd", r.Metrics.TotalCostUSD,
			"latency_ms", r.Metrics.LatencyMS,
			"groups", len(r.Groups),
			"invocations_removed", r.Tradeoff.InvocationsRemoved,
			"execution_delta_usd", r.Tradeoff.ExecutionDeltaUSD,
			"hops_removed", r.Tradeoff.HopsRemoved,
			"wall_clock_ms", elapsed.Milliseconds(),
			"error", r.Error,
		)
	}

	domainResults = algo.MarkFrontier(domainResults)
	cheapest, fastest, unambiguous := algo.Extremes(domainResults)

	var results []*pb.AlgorithmResult
	var bestResult, cheapestPB, fastestPB *pb.AlgorithmResult

	for i := range domainResults {
		r := domainResults[i]

		var fusionGroups []*pb.FusionGroup
		for _, g := range r.Groups {
			ids := make([]string, len(g))
			for i, f := range g {
				ids[i] = f.ID
			}
			// Report exactly what the cost model priced. The composite's memory
			// is the largest member (one Lambda, one allocation) and its cost is
			// that allocation held for the members' summed runtime. Re-deriving
			// these here is how the UI ended up showing 3072 MB for six 512 MB
			// functions while the caption said "largest member".
			comp := &domain.CompositeFunction{Members: g}
			fusionGroups = append(fusionGroups, &pb.FusionGroup{
				FunctionIds:      ids,
				TotalMemoryMb:    int32(comp.MemoryMB()),
				TotalRuntimeMs:   int32(comp.RuntimeMs()),
				ExecutionCostUsd: comp.ExecutionCostUSD(),
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
			Dominated:    r.Dominated,
			DominatedBy:  r.DominatedBy,
			Tradeoff: &pb.Tradeoff{
				InvocationsRemoved: r.Tradeoff.InvocationsRemoved,
				RequestDeltaUsd:    r.Tradeoff.RequestDeltaUSD,
				ExecutionDeltaUsd:  r.Tradeoff.ExecutionDeltaUSD,
				HopsRemoved:        int32(r.Tradeoff.HopsRemoved),
			},
		}
		results = append(results, pbResult)

		if cheapest != nil && r.Name == cheapest.Name {
			cheapestPB = pbResult
			bestResult = pbResult
		}
		if fastest != nil && r.Name == fastest.Name {
			fastestPB = pbResult
		}
	}

	if bestResult != nil {
		optimizationsTotal.WithLabelValues("feasible").Inc()
		recommendations.WithLabelValues(algorithmSlug(bestResult.Name)).Inc()
		if noFusionCost > 0 {
			saving := (noFusionCost - bestResult.Metrics.TotalCostUsd) / noFusionCost
			if saving < 0 {
				saving = 0 // recommending something worse than NoFusion should read as zero, not negative
			}
			recommendationSavings.Observe(saving)
		}
	} else {
		optimizationsTotal.WithLabelValues("infeasible").Inc()
	}

	name := func(r *algo.AlgorithmResult) string {
		if r == nil {
			return "none"
		}
		return r.Name
	}
	onFrontier := 0
	for _, r := range domainResults {
		if r.Error == "" && r.Metrics.Feasible && !r.Dominated {
			onFrontier++
		}
	}

	log.Info("optimization complete",
		"total_algorithms", len(results),
		"on_frontier", onFrontier,
		"cheapest", name(cheapest),
		"fastest", name(fastest),
		"unambiguous", unambiguous,
	)

	return &pb.OptimizeResponse{
		Plan: &pb.OptimizationPlan{
			Results:     results,
			Recommended: bestResult,
			Cheapest:    cheapestPB,
			Fastest:     fastestPB,
			Unambiguous: unambiguous,
		},
	}, nil
}

func main() {
	log = logger.New("optimizer")
	log.Info("starting", "version", version, "commit", commit)

	port := os.Getenv("PORT")
	if port == "" {
		port = "50053"
	}

	srv := grpcserver.New(log)
	pb.RegisterOptimizerServiceServer(srv.GRPC(), &server{})

	if err := srv.Serve(port); err != nil {
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

			AvgInitDurationMs: node.AvgInitDurationMs,
			P99InitDurationMs: node.P99InitDurationMs,
			InvocationRate:    1.0, // refined below once the entry point is known
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

	// The transfer rate lives on every node because DataTransferCostUSD is a
	// method on LambdaFunction and MergeProfitUSD works on bare slices with no
	// Application in scope.
	for _, f := range fm {
		f.DataTransferUSDPerGiB = c.DataTransferUsdPerGib
	}

	// Derive each function's invocation rate relative to the entry point, which
	// runs exactly once per application invocation. A function invoked twice as
	// often as the entry has a rate of 2.
	//
	// This is the paper's r_f, and it decides the request charge, which is now
	// the only way fusion saves money. Without telemetry every rate stays at 1,
	// which under-counts fan-out but never invents savings.
	if entry := entryFunction(funcs); entry != nil && entry.InvocationCount > 0 {
		base := float64(entry.InvocationCount)
		for _, f := range funcs {
			if f.InvocationCount > 0 {
				f.InvocationRate = float64(f.InvocationCount) / base
			}
		}
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

// entryFunction returns the function the platform invokes to start the
// application, i.e. the one with no parent inside the graph.
func entryFunction(funcs []*domain.LambdaFunction) *domain.LambdaFunction {
	for _, f := range funcs {
		if f.Parent == nil {
			return f
		}
	}
	return nil
}
