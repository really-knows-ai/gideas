# Build the archivist binary (requires CGO for go-sqlite3)
#   docker build -t flow-archivist:latest -f platform/archivist/Dockerfile .
FROM golang:1.25 AS builder
WORKDIR /workspace
ENV GOWORK=off

RUN apt-get update && apt-get install -y gcc libc6-dev && rm -rf /var/lib/apt/lists/*

COPY gen/go.mod gen/go.sum gen/
COPY gen/flow/ gen/flow/
COPY platform/pkg/eventbus/ platform/pkg/eventbus/

COPY platform/archivist/go.mod platform/archivist/go.sum platform/archivist/
RUN cd platform/archivist && go mod download

COPY platform/archivist/ platform/archivist/

RUN cd platform/archivist && CGO_ENABLED=1 go build -o /archivist ./cmd

FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y ca-certificates && rm -rf /var/lib/apt/lists/*
COPY --from=builder /archivist /archivist
USER 65532:65532
ENTRYPOINT ["/archivist"]
