# Multi-target Dockerfile for Foundry Flow nodes.
#
# Build with --build-arg NODE=<node-dir> to select which node to build.
# The NODE arg is the directory name under nodes/ (e.g., forge, sort, null-node).
#
# Example:
#   docker build -t sort:latest --build-arg NODE=sort -f nodes/Dockerfile .
FROM golang:1.25 AS builder
ARG NODE

WORKDIR /workspace

# Copy workspace module manifests
COPY go.work go.work
COPY go.work.sum go.work.sum
COPY gen/go.mod gen/go.mod
COPY gen/go.sum gen/go.sum
COPY sdk/go/go.mod sdk/go/go.mod
COPY sdk/go/go.sum sdk/go/go.sum
COPY nodes/go.mod nodes/go.mod
COPY nodes/go.sum nodes/go.sum
COPY sidecar/go.mod sidecar/go.mod
COPY sidecar/go.sum sidecar/go.sum
COPY archivist/go.mod archivist/go.mod
COPY archivist/go.sum archivist/go.sum
COPY monitor/go.mod monitor/go.mod
COPY monitor/go.sum monitor/go.sum
COPY librarian/go.mod librarian/go.mod
COPY librarian/go.sum librarian/go.sum
COPY operator/go.mod operator/go.mod
COPY operator/go.sum operator/go.sum
COPY tools/haiku-watch/go.mod tools/haiku-watch/go.mod
COPY tools/haiku-watch/go.sum tools/haiku-watch/go.sum
COPY clerk/go.mod clerk/go.mod
COPY clerk/go.sum clerk/go.sum
COPY eventbus/go.mod eventbus/go.mod
COPY eventbus/go.sum eventbus/go.sum
COPY frictionledger/go.mod frictionledger/go.mod
COPY frictionledger/go.sum frictionledger/go.sum
COPY jury/go.mod jury/go.mod
COPY jury/go.sum jury/go.sum
COPY pkg/eventbus/go.mod pkg/eventbus/go.mod
COPY pkg/eventbus/go.sum pkg/eventbus/go.sum

RUN go mod download -x 2>&1 | tail -1

# Copy source for nodes + dependencies
COPY gen/ gen/
COPY sdk/go/ sdk/go/
COPY nodes/ nodes/

RUN CGO_ENABLED=0 go build -o /node ./nodes/${NODE}

FROM gcr.io/distroless/static:nonroot
COPY --from=builder /node /node
USER 65532:65532
ENTRYPOINT ["/node"]
