package main

import (
	"context"
	"log"
	"net"
	"os"

	"google.golang.org/grpc"

	pb "github.com/Vaivaswat2244/OptiFuse_go/proto"
	enricher "github.com/Vaivaswat2244/OptiFuse_go/services/enricher/internal"
)

type server struct {
	pb.UnimplementedEnricherServiceServer
	enricher *enricher.Enricher
}

func (s *server) Enrich(ctx context.Context, req *pb.EnrichRequest) (*pb.EnrichResponse, error) {
	log.Printf("🔥 Enricher ACTIVATED: Received simulation request!")
	graph, enrichedIDs, missingIDs, err := s.enricher.Enrich(
		ctx,
		req.Graph,
		req.RoleArn,
		req.ExternalId,
		req.ServiceName,
		req.Stage,
	)
	if err != nil {
		return nil, err
	}

	return &pb.EnrichResponse{
		Graph:       graph,
		EnrichedIds: enrichedIDs,
		MissingIds:  missingIDs,
	}, nil
}

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "50052"
	}

	lis, err := net.Listen("tcp", ":"+port)
	if err != nil {
		log.Fatalf("failed to listen on port %s: %v", port, err)
	}

	s := grpc.NewServer()
	pb.RegisterEnricherServiceServer(s, &server{
		enricher: &enricher.Enricher{},
	})

	log.Printf("✓ enricher service listening on :%s", port)
	if err := s.Serve(lis); err != nil {
		log.Fatalf("failed to serve: %v", err)
	}
}
