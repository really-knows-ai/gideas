# Build the clerk binary (no CGO required)
#   docker build -t clerk:latest -f platform/clerk/Dockerfile .
FROM golang:1.25 AS builder
WORKDIR /workspace
ENV GOWORK=off

COPY gen/go.mod gen/go.sum gen/
COPY gen/flow/ gen/flow/

COPY platform/clerk/go.mod platform/clerk/go.sum platform/clerk/
RUN cd platform/clerk && go mod download

COPY platform/clerk/ platform/clerk/

RUN cd platform/clerk && CGO_ENABLED=0 go build -o /clerk ./cmd

FROM gcr.io/distroless/static:nonroot
COPY --from=builder /clerk /clerk
USER 65532:65532
ENTRYPOINT ["/clerk"]
