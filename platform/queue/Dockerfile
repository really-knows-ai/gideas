# Build the queue-service binary (no CGO needed — stateless, no SQLite)
#   docker build -t flow-queue-service:latest -f platform/queue/Dockerfile .
FROM golang:1.25 AS builder
WORKDIR /workspace
ENV GOWORK=off

COPY gen/go.mod gen/go.sum gen/
COPY gen/flow/ gen/flow/
COPY sdk/go/ sdk/go/

COPY platform/queue/go.mod platform/queue/go.sum platform/queue/
RUN cd platform/queue && go mod download

COPY platform/queue/ platform/queue/

RUN cd platform/queue && CGO_ENABLED=0 go build -o /queue-service ./cmd

FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y ca-certificates && rm -rf /var/lib/apt/lists/*
COPY --from=builder /queue-service /queue-service
USER 65532:65532
ENTRYPOINT ["/queue-service"]
