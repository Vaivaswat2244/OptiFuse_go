MODULE := github.com/Vaivaswat2244/OptiFuse_go
# deployer is intentionally absent: it is a designed but unimplemented service
# (proto contract only — see services/deployer/README.md). Listing it here made
# `make build` fail with "services/deployer/cmd: directory not found".
SERVICES := gateway parser enricher optimizer
GO := go
PROTOC := protoc

.PHONY: all proto build test lint docker-up docker-down images push clean help

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

# Images are tagged with the short commit SHA, never "latest", so the
# deployed tag always names exactly one commit. The source label links each
# GHCR package to this repository.
REGISTRY ?= ghcr.io/vaivaswat2244
TAG      ?= $(shell git rev-parse --short HEAD)
IMAGES   := gateway parser enricher optimizer

## images: Build all service images tagged with the current commit
images:
	@for svc in $(IMAGES); do \
		echo "building $(REGISTRY)/optifuse-$$svc:$(TAG)"; \
		docker build -q -f services/$$svc/Dockerfile \
			--build-arg VERSION=$(TAG) --build-arg COMMIT=$(TAG) \
			--label org.opencontainers.image.source=https://github.com/Vaivaswat2244/OptiFuse_go \
			-t $(REGISTRY)/optifuse-$$svc:$(TAG) . || exit 1; \
	done

## push: Push the images built by `make images`
push:
	@for svc in $(IMAGES); do docker push -q $(REGISTRY)/optifuse-$$svc:$(TAG) || exit 1; done

## docker-down: Stop all services
docker-down:
	docker compose down

## clean: Remove built binaries
clean:
	rm -rf bin/

help:
	@grep -E '^##' Makefile | sed 's/## //'
