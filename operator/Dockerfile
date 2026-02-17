# Build the manager binary
# NOTE: Build context must be the repository root (not operator/).
#   docker build -t flow-operator:latest -f operator/Dockerfile .
FROM golang:1.25 AS builder
ARG TARGETOS
ARG TARGETARCH

WORKDIR /workspace

# Copy workspace module manifests
COPY go.work go.work
COPY go.work.sum go.work.sum
COPY gen/go.mod gen/go.mod
COPY gen/go.sum gen/go.sum
COPY operator/go.mod operator/go.mod
COPY operator/go.sum operator/go.sum
# Stub out other workspace modules
COPY sdk/go/go.mod sdk/go/go.mod
COPY sdk/go/go.sum sdk/go/go.sum
COPY sidecar/go.mod sidecar/go.mod
COPY sidecar/go.sum sidecar/go.sum
COPY archivist/go.mod archivist/go.mod
COPY archivist/go.sum archivist/go.sum
COPY monitor/go.mod monitor/go.mod
COPY monitor/go.sum monitor/go.sum
COPY librarian/go.mod librarian/go.mod
COPY librarian/go.sum librarian/go.sum
COPY nodes/go.mod nodes/go.mod
COPY nodes/go.sum nodes/go.sum
COPY tools/haiku-watch/go.mod tools/haiku-watch/go.mod
COPY tools/haiku-watch/go.sum tools/haiku-watch/go.sum

RUN go mod download -x 2>&1 | tail -1

# Copy source
COPY gen/ gen/
COPY operator/ operator/

# Build
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} go build -a -o /manager ./operator/cmd/main.go

# Use distroless as minimal base image to package the manager binary
FROM gcr.io/distroless/static:nonroot
WORKDIR /
COPY --from=builder /manager .
USER 65532:65532

ENTRYPOINT ["/manager"]
