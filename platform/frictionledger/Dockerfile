# Build the frictionledger binary (requires CGO for go-sqlite3)
#   docker build -t frictionledger:latest -f platform/frictionledger/Dockerfile .
FROM golang:1.25 AS builder
WORKDIR /workspace
ENV GOWORK=off

RUN apt-get update && apt-get install -y gcc libc6-dev && rm -rf /var/lib/apt/lists/*

COPY gen/go.mod gen/go.sum gen/
COPY gen/flow/ gen/flow/

COPY platform/frictionledger/go.mod platform/frictionledger/go.sum platform/frictionledger/
RUN cd platform/frictionledger && go mod download

COPY platform/frictionledger/ platform/frictionledger/

RUN cd platform/frictionledger && CGO_ENABLED=1 go build -o /frictionledger ./cmd

FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y ca-certificates && rm -rf /var/lib/apt/lists/*
COPY --from=builder /frictionledger /frictionledger
USER 65532:65532
ENTRYPOINT ["/frictionledger"]
