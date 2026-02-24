# Build the monitor binary (no CGO required)
#   docker build -t monitor:latest -f platform/monitor/Dockerfile .
FROM golang:1.25 AS builder
WORKDIR /workspace
ENV GOWORK=off

COPY gen/go.mod gen/go.sum gen/
COPY gen/flow/ gen/flow/

COPY platform/monitor/go.mod platform/monitor/go.sum platform/monitor/
RUN cd platform/monitor && go mod download

COPY platform/monitor/ platform/monitor/

RUN cd platform/monitor && CGO_ENABLED=0 go build -o /monitor ./cmd

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=builder /monitor /monitor
USER 65532:65532
ENTRYPOINT ["/monitor"]
