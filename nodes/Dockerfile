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
ENV GOWORK=off

# Copy dependency modules
COPY gen/go.mod gen/go.sum gen/
COPY gen/flow/ gen/flow/
COPY sdk/go/ sdk/go/

# Copy nodes module and download deps
COPY nodes/go.mod nodes/go.sum nodes/
RUN cd nodes && go mod download

# Copy nodes source
COPY nodes/ nodes/

RUN cd nodes && CGO_ENABLED=0 go build -o /node ./${NODE}

FROM gcr.io/distroless/static:nonroot
COPY --from=builder /node /node
USER 65532:65532
ENTRYPOINT ["/node"]
