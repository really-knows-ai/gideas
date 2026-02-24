# Build the frictionledger binary (requires CGO for go-sqlite3)
FROM golang:1.25 AS builder
WORKDIR /workspace

RUN apt-get update && apt-get install -y gcc libc6-dev && rm -rf /var/lib/apt/lists/*

# Copy workspace module manifests
COPY go.work go.work
COPY go.work.sum go.work.sum
COPY gen/go.mod gen/go.mod
COPY gen/go.sum gen/go.sum
COPY frictionledger/go.mod frictionledger/go.mod
COPY frictionledger/go.sum frictionledger/go.sum
COPY eventbus/go.mod eventbus/go.mod
COPY eventbus/go.sum eventbus/go.sum
COPY sdk/go/go.mod sdk/go/go.mod
COPY sdk/go/go.sum sdk/go/go.sum
COPY sidecar/go.mod sidecar/go.mod
COPY sidecar/go.sum sidecar/go.sum
COPY archivist/go.mod archivist/go.mod
COPY archivist/go.sum archivist/go.sum
COPY librarian/go.mod librarian/go.mod
COPY librarian/go.sum librarian/go.sum
COPY monitor/go.mod monitor/go.mod
COPY monitor/go.sum monitor/go.sum
COPY nodes/go.mod nodes/go.mod
COPY nodes/go.sum nodes/go.sum
COPY operator/go.mod operator/go.mod
COPY operator/go.sum operator/go.sum
COPY clerk/go.mod clerk/go.mod
COPY clerk/go.sum clerk/go.sum
COPY jury/go.mod jury/go.mod
COPY jury/go.sum jury/go.sum
COPY tools/haiku-watch/go.mod tools/haiku-watch/go.mod
COPY tools/haiku-watch/go.sum tools/haiku-watch/go.sum
COPY pkg/eventbus/go.mod pkg/eventbus/go.mod
COPY pkg/eventbus/go.sum pkg/eventbus/go.sum

RUN go mod download -x 2>&1 | tail -1

COPY gen/ gen/
COPY frictionledger/ frictionledger/

RUN CGO_ENABLED=1 go build -o /frictionledger ./frictionledger/cmd

FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y ca-certificates && rm -rf /var/lib/apt/lists/*
COPY --from=builder /frictionledger /frictionledger
USER 65532:65532
ENTRYPOINT ["/frictionledger"]
