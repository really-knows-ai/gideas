# Build the manager binary
# NOTE: Build context must be the repository root (not platform/operator/).
#   docker build -t flow-operator:latest -f platform/operator/Dockerfile .
FROM golang:1.25 AS builder
ARG TARGETOS
ARG TARGETARCH

WORKDIR /workspace
ENV GOWORK=off

# Copy dependency modules
COPY gen/go.mod gen/go.sum gen/
COPY gen/flow/ gen/flow/
COPY platform/pkg/eventbus/ platform/pkg/eventbus/
COPY platform/pkg/randid/ platform/pkg/randid/
COPY sdk/go/ sdk/go/

# Copy operator module and download deps
COPY platform/operator/go.mod platform/operator/go.sum platform/operator/
RUN cd platform/operator && go mod download

# Copy operator source
COPY platform/operator/ platform/operator/

RUN cd platform/operator && CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} go build -a -o /manager ./cmd/main.go

FROM gcr.io/distroless/static:nonroot
WORKDIR /
COPY --from=builder /manager .
USER 65532:65532
ENTRYPOINT ["/manager"]
