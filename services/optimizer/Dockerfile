FROM golang:1.26.3-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN go build -o optimizer ./services/optimizer/cmd/

FROM alpine:3.21
RUN apk --no-cache add ca-certificates glpk
WORKDIR /app
COPY --from=builder /app/optimizer .
EXPOSE 50053
CMD ["./optimizer"]
