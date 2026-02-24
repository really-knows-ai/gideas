# Build the eventbus binary (requires CGO for go-sqlite3)
#   docker build -t eventbus:latest -f platform/eventbus/Dockerfile .
FROM golang:1.25 AS builder
WORKDIR /workspace
ENV GOWORK=off

RUN apt-get update && apt-get install -y gcc libc6-dev && rm -rf /var/lib/apt/lists/*

COPY gen/go.mod gen/go.sum gen/
COPY gen/flow/ gen/flow/

COPY platform/eventbus/go.mod platform/eventbus/go.sum platform/eventbus/
RUN cd platform/eventbus && go mod download

COPY platform/eventbus/ platform/eventbus/

RUN cd platform/eventbus && CGO_ENABLED=1 go build -o /eventbus ./cmd

FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y ca-certificates && rm -rf /var/lib/apt/lists/*
COPY --from=builder /eventbus /eventbus
USER 65532:65532
ENTRYPOINT ["/eventbus"]
