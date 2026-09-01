package main

import (
	"context"
	"log/slog"
	"os"
	"time"

	pb "github.com/Vaivaswat2244/OptiFuse_go/proto"
	enricher "github.com/Vaivaswat2244/OptiFuse_go/services/enricher/internal"
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
	pb.UnimplementedEnricherServiceServer
	enricher *enricher.Enricher
}

func (s *server) Enrich(ctx context.Context, req *pb.EnrichRequest) (*pb.EnrichResponse, error) {
	// Shadows the package logger so every line below carries the caller's
	// request ID without touching the individual log calls.
	log := reqid.Logger(ctx, log)

	log.Info("enrich request received",
		"service", req.ServiceName,
		"stage", req.Stage,
		"functions", len(req.Graph.Nodes),
		"role_arn", req.RoleArn,
	)

	start := time.Now()
	graph, enrichedIDs, missingIDs, err := s.enricher.Enrich(
		ctx,
		req.Graph,
		req.RoleArn,
		req.ExternalId,
		req.ServiceName,
		req.Stage,
	)
	enrichmentDuration.Observe(time.Since(start).Seconds())

	if err != nil {
		enrichmentRequests.WithLabelValues(classifyEnrichmentError(err.Error())).Inc()
		log.Error("enrichment failed",
			"service", req.ServiceName,
			"error", err,
		)
		return nil, err
	}

	enrichmentRequests.WithLabelValues("success").Inc()
	enrichmentFunctions.WithLabelValues("enriched").Add(float64(len(enrichedIDs)))
	enrichmentFunctions.WithLabelValues("missing").Add(float64(len(missingIDs)))

	log.Info("enrichment complete",
		"service", req.ServiceName,
		"enriched", enrichedIDs,
		"missing", missingIDs,
	)

	for id, node := range graph.Nodes {
		if node.AvgDurationMs > 0 {
			log.Debug("enriched function",
				"id", id,
				"avg_duration_ms", node.AvgDurationMs,
				"p99_duration_ms", node.P99LatencyMs,
				"avg_memory_mb", node.AvgMemoryUsedMb,
				"invocations", node.InvocationCount,
				"cold_start_rate", node.ColdStartRate,
				"avg_init_ms", node.AvgInitDurationMs,
				"p99_init_ms", node.P99InitDurationMs,
			)
		}
	}

	return &pb.EnrichResponse{
		Graph:       graph,
		EnrichedIds: enrichedIDs,
		MissingIds:  missingIDs,
	}, nil
}

func main() {
	log = logger.New("enricher")
	log.Info("starting", "version", version, "commit", commit)

	port := os.Getenv("PORT")
	if port == "" {
		port = "50052"
	}

	srv := grpcserver.New(log)
	pb.RegisterEnricherServiceServer(srv.GRPC(), &server{
		enricher: &enricher.Enricher{},
	})

	if err := srv.Serve(port); err != nil {
		log.Error("server error", "error", err)
		os.Exit(1)
	}
}
