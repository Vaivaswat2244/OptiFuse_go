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