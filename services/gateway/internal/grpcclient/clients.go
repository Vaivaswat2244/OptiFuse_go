package grpcclient

import (
	"fmt"
	"os"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// Clients holds gRPC connections to all internal services.
// The gateway is the only thing that talks to these — frontend never sees them.
type Clients struct {
	Parser    *ParserClient
	Enricher  *EnricherClient
	Optimizer *OptimizerClient
}

// New creates gRPC connections to all services using addresses from env vars.
// Addresses are injected by docker-compose / Kubernetes configmap.
//
// PARSER_ADDR=parser:50051
// ENRICHER_ADDR=enricher:50052
// OPTIMIZER_ADDR=optimizer:50053
func New() (*Clients, error) {
	parserConn, err := dial(mustEnv("PARSER_ADDR"))
	if err != nil {
		return nil, fmt.Errorf("connect to parser: %w", err)
	}

	enricherConn, err := dial(mustEnv("ENRICHER_ADDR"))
	if err != nil {
		return nil, fmt.Errorf("connect to enricher: %w", err)
	}

	optimizerConn, err := dial(mustEnv("OPTIMIZER_ADDR"))
	if err != nil {
		return nil, fmt.Errorf("connect to optimizer: %w", err)
	}

	return &Clients{
		Parser:    NewParserClient(parserConn),
		Enricher:  NewEnricherClient(enricherConn),
		Optimizer: NewOptimizerClient(optimizerConn),
	}, nil
}

func dial(addr string) (*grpc.ClientConn, error) {
	// insecure is fine on a private Kubernetes network.
	// Add TLS here when exposing services across clusters.
	return grpc.NewClient(addr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
}

func mustEnv(key string) string {
	v := os.Getenv(key)
	if v == "" {
		panic(fmt.Sprintf("required env var %s is not set", key))
	}
	return v
}
