MODULE := github.com/Vaivaswat2244/OptiFuse_go
SERVICES := gateway parser enricher optimizer deployer
GO := go
PROTOC := protoc

.PHONY: all proto build test lint docker-up docker-down clean help

all: proto build

## proto: Generate Go code from all .proto files
proto:
	@echo "→ Generating protobuf Go code..."
	$(PROTOC) --proto_path=. \
		--go_out=. --go_opt=paths=source_relative \
		--go-grpc_out=. --go-grpc_opt=paths=source_relative \
		proto/graph.proto proto/optimizer.proto proto/services.proto
	@echo "✓ Proto generation complete"

## build: Build all service binaries into bin/
build:
	@mkdir -p bin
	@for svc in $(SERVICES); do \
		echo "→ Building $$svc..."; \
		$(GO) build -o bin/$$svc ./services/$$svc/cmd/; \
	done

## test: Run all tests
test:
	$(GO) test ./... -v -count=1

## test-parser: Run parser service tests only
test-parser:
	$(GO) test ./services/parser/... -v

## test-optimizer: Run optimizer service tests only
test-optimizer:
	$(GO) test ./services/optimizer/... -v

## tidy: Run go mod tidy
tidy:
	$(GO) mod tidy

## docker-up: Start all services with docker-compose
docker-up:
	docker compose up --build

## docker-down: Stop all services
docker-down:
	docker compose down

## clean: Remove built binaries
clean:
	rm -rf bin/

help:
	@grep -E '^##' Makefile | sed 's/## //'
