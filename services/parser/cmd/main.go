package main

import (
	"context"
	"log/slog"
	"os"

	pb "github.com/Vaivaswat2244/OptiFuse_go/proto"
	parser "github.com/Vaivaswat2244/OptiFuse_go/services/parser/internal"
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
	pb.UnimplementedParserServiceServer
}

func (s *server) Parse(ctx context.Context, req *pb.ParseRequest) (*pb.ParseResponse, error) {
	// Shadows the package logger so every line below carries the caller's
	// request ID without touching the individual log calls.
	log := reqid.Logger(ctx, log)

	log.Info("parse request received", "repo", req.RepoName)

	parsed, err := parser.Parse(req.RepoName, req.YamlContent)
	if err != nil {
		log.Error("parse failed", "repo", req.RepoName, "error", err)
		return nil, err
	}

	log.Info("parse complete",
		"repo", req.RepoName,
		"service", parsed.ServiceName,
		"stage", parsed.Stage,
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
			// Estimates from custom.optifuse.functions. The enricher overwrites
			// these per-function when real CloudWatch data exists.
			AvgDurationMs:   f.AvgDurationMs,
			InvocationCount: f.InvocationCount,
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
		Warnings:    parsed.Warnings,
		ServiceName: parsed.ServiceName,
		Stage:       parsed.Stage,
	}, nil
}

func main() {
	log = logger.New("parser")
	log.Info("starting", "version", version, "commit", commit)

	port := os.Getenv("PORT")
	if port == "" {
		port = "50051"
	}

	srv := grpcserver.New(log)
	pb.RegisterParserServiceServer(srv.GRPC(), &server{})

	if err := srv.Serve(port); err != nil {
		log.Error("server error", "error", err)
		os.Exit(1)
	}
}
