# NOT IMPLEMENTED — this service has no Go code yet; `docker build` on this file
# will fail at the build step because ./services/deployer/cmd/ does not exist.
# Kept because it records what the service will need at runtime (Node plus the
# Serverless CLI). See README.md in this directory for the design.

FROM golang:1.26.3-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN go build -o deployer ./services/deployer/cmd/

FROM alpine:3.21
RUN apk --no-cache add ca-certificates nodejs npm
RUN npm install -g serverless
WORKDIR /app
COPY --from=builder /app/deployer .
EXPOSE 50054
CMD ["./deployer"]