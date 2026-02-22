# Build the clerk binary (no CGO needed — stateless service, no SQLite)
FROM golang:1.25 AS builder
WORKDIR /workspace

# Copy workspace module manifests
COPY go.work go.work
COPY go.work.sum go.work.sum
COPY gen/go.mod gen/go.mod
COPY gen/go.sum gen/go.sum
COPY clerk/go.mod clerk/go.mod
COPY clerk/go.sum clerk/go.sum
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
COPY jury/go.mod jury/go.mod
COPY jury/go.sum jury/go.sum
COPY nodes/go.mod nodes/go.mod
COPY nodes/go.sum nodes/go.sum
COPY operator/go.mod operator/go.mod
COPY operator/go.sum operator/go.sum
COPY tools/haiku-watch/go.mod tools/haiku-watch/go.mod
COPY tools/haiku-watch/go.sum tools/haiku-watch/go.sum

RUN go mod download -x 2>&1 | tail -1

# Copy source
COPY gen/ gen/
COPY clerk/ clerk/

RUN CGO_ENABLED=0 go build -o /clerk ./clerk/cmd

# Distroless static image (no libc needed — pure Go binary)
FROM gcr.io/distroless/static
COPY --from=builder /clerk /clerk
USER 65532:65532
ENTRYPOINT ["/clerk"]
