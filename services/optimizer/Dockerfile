# syntax=docker/dockerfile:1

FROM golang:1.26.3-alpine AS builder
WORKDIR /app

# Dependencies first: this layer is only invalidated when go.mod/go.sum change.
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

# Only what is needed to compile. A .dockerignore keeps .env and friends out of
# the context entirely; copying explicitly means an unrelated file can never
# invalidate the layer below.
COPY proto/ proto/
COPY services/ services/
COPY shared/ shared/

ARG VERSION=dev
ARG COMMIT=unknown

# CGO_ENABLED=0 produces a static binary. It currently works without this only
# because both stages are Alpine; a Debian or distroless runtime would fail on a
# dynamically linked binary, and static is the prerequisite for moving to scratch.
# -s -w strips DWARF and the symbol table.
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux go build \
      -ldflags="-s -w -X main.version=${VERSION} -X main.commit=${COMMIT}" \
      -o optimizer ./services/optimizer/cmd/

FROM alpine:3.21
RUN apk --no-cache add ca-certificates glpk
# glpk provides glpsol, the MIP solver the MtxILP algorithm shells out to.

# Runs unprivileged: Kubernetes' restricted Pod Security Standard forbids root,
# and this is what lets the manifests set runAsNonRoot: true.
RUN adduser -D -u 10001 optifuse

WORKDIR /app
COPY --from=builder /app/optimizer .
USER optifuse

EXPOSE 50053
EXPOSE 9090
CMD ["./optimizer"]
