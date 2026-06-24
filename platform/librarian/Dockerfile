# Build the librarian binary (requires CGO for go-sqlite3)
#   docker build -t librarian:latest -f platform/librarian/Dockerfile .
FROM golang:1.25 AS builder
WORKDIR /workspace
ENV GOWORK=off

RUN apt-get update && apt-get install -y gcc libc6-dev libsqlite3-dev && rm -rf /var/lib/apt/lists/*

COPY gen/go.mod gen/go.sum gen/
COPY gen/flow/ gen/flow/
COPY platform/pkg/eventbus/ platform/pkg/eventbus/

COPY platform/librarian/go.mod platform/librarian/go.sum platform/librarian/
RUN cd platform/librarian && go mod download

COPY platform/librarian/ platform/librarian/

RUN cd platform/librarian && CGO_ENABLED=1 go build -o /librarian ./cmd

FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y ca-certificates && rm -rf /var/lib/apt/lists/*
COPY --from=builder /librarian /librarian
USER 65532:65532
ENTRYPOINT ["/librarian"]
