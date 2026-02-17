# Multi-target Dockerfile for haiku nodes and null-node.
#
# Build with --build-arg NODE=<node-dir> to select which node to build.
# The NODE arg is the directory name under nodes/ (e.g., haiku-forge, haiku-sort).
#
# Example:
#   docker build -t haiku-forge:latest --build-arg NODE=haiku-forge -f nodes/Dockerfile .
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
