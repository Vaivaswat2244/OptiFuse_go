package main

import (
	"context"
	"log/slog"
	"net"
	"os"

	"google.golang.org/grpc"

	pb "github.com/Vaivaswat2244/OptiFuse_go/proto"
	enricher "github.com/Vaivaswat2244/OptiFuse_go/services/enricher/internal"
	"github.com/Vaivaswat2244/OptiFuse_go/shared/logger"
)

var log *slog.Logger

type server struct {
	pb.UnimplementedEnricherServiceServer
	enricher *enricher.Enricher
}

func (s *server) Enrich(ctx context.Context, req *pb.EnrichRequest) (*pb.EnrichResponse, error) {
	log.Info("enrich request received",
		"service", req.ServiceName,
		"stage", req.Stage,
		"functions", len(req.Graph.Nodes),
		"role_arn", req.RoleArn,
	)

	graph, enrichedIDs, missingIDs, err := s.enricher.Enrich(
		ctx,
		req.Graph,
		req.RoleArn,
		req.ExternalId,
		req.ServiceName,
		req.Stage,
	)
	if err != nil {
		log.Error("enrichment failed",
			"service", req.ServiceName,
			"error", err,
		)
		return nil, err
	}

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
				"avg_memory_mb", node.AvgMemoryUsedMb,
				"invocations", node.InvocationCount,
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

	port := os.Getenv("PORT")
	if port == "" {
		port = "50052"
	}

	lis, err := net.Listen("tcp", ":"+port)
	if err != nil {
		log.Error("failed to listen", "port", port, "error", err)
		os.Exit(1)
	}

	s := grpc.NewServer()
	pb.RegisterEnricherServiceServer(s, &server{
		enricher: &enricher.Enricher{},
	})

	log.Info("enricher service started", "port", port)
	if err := s.Serve(lis); err != nil {
		log.Error("server error", "error", err)
		os.Exit(1)
	}
}
