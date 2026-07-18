package main

import (
	"context"
	"log"
	"net"
	"os"

	"google.golang.org/grpc"

	pb "github.com/Vaivaswat2244/OptiFuse_go/proto"
	parser "github.com/Vaivaswat2244/OptiFuse_go/services/parser/internal"
)

type server struct {
	pb.UnimplementedParserServiceServer
}

func (s *server) Parse(ctx context.Context, req *pb.ParseRequest) (*pb.ParseResponse, error) {
	log.Printf("🔥 Parser ACTIVATED: Received simulation request!")
	parsed, err := parser.Parse(req.RepoName, req.YamlContent)
	if err != nil {
		return nil, err
	}

	// Convert ParsedGraph → proto Graph
	nodes := make(map[string]*pb.FunctionNode, len(parsed.Functions))
	for _, f := range parsed.Functions {
		nodes[f.ID] = &pb.FunctionNode{
			Id:           f.ID,
			Name:         f.Name,
			MemoryMb:     int32(f.MemoryMB),
			TimeoutSec:   int32(f.TimeoutSec),
			Handler:      f.Handler,
			Runtime:      f.Runtime,
			Environment:  f.Environment,
			DataOutBytes: f.DataOutBytes,
			LoadFactor:   1.0,
		}
	}

	// Build edge list from DataOutBytes on each node
	var edges []*pb.Edge
	for _, f := range parsed.Functions {
		for childID, bytes := range f.DataOutBytes {
			const gib = 1024 * 1024 * 1024
			costUSD := (float64(bytes) / gib) * 0.01
			edges = append(edges, &pb.Edge{
				From:      f.ID,
				To:        childID,
				DataBytes: bytes,
				CostUsd:   costUSD,
			})
		}
	}

	graph := &pb.Graph{
		Name:         parsed.Name,
		Nodes:        nodes,
		Edges:        edges,
		CriticalPath: parsed.CriticalPath,
		Constraints: &pb.Constraints{
			MaxMemoryMb:  int32(parsed.MaxMemoryMB),
			MaxLatencyMs: int32(parsed.MaxLatencyMS),	
			NetworkHopMs: int32(parsed.NetworkHopMS),
		},
	}

	return &pb.ParseResponse{
		Graph:    graph,
		Warnings: parsed.Warnings,
	}, nil
}

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "50051"
	}

	lis, err := net.Listen("tcp", ":"+port)
	if err != nil {
		log.Fatalf("failed to listen on port %s: %v", port, err)
	}

	s := grpc.NewServer()
	pb.RegisterParserServiceServer(s, &server{})

	log.Printf("✓ parser service listening on :%s", port)
	if err := s.Serve(lis); err != nil {
		log.Fatalf("failed to serve: %v", err)
	}
}
