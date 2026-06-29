# Build the sidecar binary
#   docker build -t sidecar:latest -f platform/sidecar/Dockerfile .
FROM golang:1.25 AS builder
WORKDIR /workspace
ENV GOWORK=off

COPY gen/go.mod gen/go.sum gen/
COPY gen/flow/ gen/flow/
COPY platform/pkg/eventbus/ platform/pkg/eventbus/
COPY platform/pkg/randid/ platform/pkg/randid/
COPY sdk/go/ sdk/go/

COPY platform/sidecar/go.mod platform/sidecar/go.sum platform/sidecar/
RUN cd platform/sidecar && go mod download

COPY platform/sidecar/ platform/sidecar/

RUN cd platform/sidecar && CGO_ENABLED=0 go build -o /sidecar ./cmd

FROM gcr.io/distroless/static:nonroot
COPY --from=builder /sidecar /sidecar
USER 65532:65532
ENTRYPOINT ["/sidecar"]
