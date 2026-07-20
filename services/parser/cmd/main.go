package main

import (
	"context"
	"log/slog"
	"net"
	"os"

	"google.golang.org/grpc"

	pb "github.com/Vaivaswat2244/OptiFuse_go/proto"
	parser "github.com/Vaivaswat2244/OptiFuse_go/services/parser/internal"
	"github.com/Vaivaswat2244/OptiFuse_go/shared/logger"
)

var log *slog.Logger

type server struct {
	pb.UnimplementedParserServiceServer
}

func (s *server) Parse(ctx context.Context, req *pb.ParseRequest) (*pb.ParseResponse, error) {
	log.Info("parse request received", "repo", req.RepoName)

	parsed, err := parser.Parse(req.RepoName, req.YamlContent)
	if err != nil {
		log.Error("parse failed", "repo", req.RepoName, "error", err)
		return nil, err
	}

	log.Info("parse complete",
		"repo", req.RepoName,
		"functions", len(parsed.Functions),
		"critical_path", parsed.CriticalPath,
		"max_memory_mb", parsed.MaxMemoryMB,
		"max_latency_ms", parsed.MaxLatencyMS,
		"network_hop_ms", parsed.NetworkHopMS,
		"warnings", parsed.Warnings,
	)

	for _, f := range parsed.Functions {
		log.Debug("parsed function",
			"id", f.ID,
			"memory_mb", f.MemoryMB,
			"timeout_sec", f.TimeoutSec,
			"children", len(f.DataOutBytes),
		)
	}

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

	log.Info("graph built", "nodes", len(nodes), "edges", len(edges))

	return &pb.ParseResponse{
		Graph: &pb.Graph{
			Name:         parsed.Name,
			Nodes:        nodes,
			Edges:        edges,
			CriticalPath: parsed.CriticalPath,
			Constraints: &pb.Constraints{
				MaxMemoryMb:  int32(parsed.MaxMemoryMB),
				MaxLatencyMs: int32(parsed.MaxLatencyMS),
				NetworkHopMs: int32(parsed.NetworkHopMS),
			},
		},
		Warnings: parsed.Warnings,
	}, nil
}

func main() {
	log = logger.New("parser")

	port := os.Getenv("PORT")
	if port == "" {
		port = "50051"
	}

	lis, err := net.Listen("tcp", ":"+port)
	if err != nil {
		log.Error("failed to listen", "port", port, "error", err)
		os.Exit(1)
	}

	s := grpc.NewServer()
	pb.RegisterParserServiceServer(s, &server{})

	log.Info("parser service started", "port", port)
	if err := s.Serve(lis); err != nil {
		log.Error("server error", "error", err)
		os.Exit(1)
	}
}
