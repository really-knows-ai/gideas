# Multi-stage build for the Cartographer service.
# CGO_ENABLED=1 is required because go-ladybug is a CGo wrapper.
FROM golang:1.26-bookworm AS builder
WORKDIR /workspace
ENV GOWORK=off

# curl is used by go-ladybug's native-library downloader.
RUN apt-get update && apt-get install -y curl g++ && rm -rf /var/lib/apt/lists/*

# Copy dependencies first for layer caching.
COPY gen/go.mod gen/go.sum gen/
COPY gen/flow/ gen/flow/
COPY platform/pkg/eventbus/ platform/pkg/eventbus/
COPY platform/cartographer/go.mod platform/cartographer/go.sum platform/cartographer/
COPY tools/setup-ladybug.sh tools/ladybug-verified-curl tools/
RUN LADYBUG_WORK_MODULES="gen platform/pkg/eventbus platform/cartographer" tools/setup-ladybug.sh && \
    moddir="$(GOWORK=/workspace/.cache/ladybug/go.work go list -m -f '{{.Dir}}' github.com/LadybugDB/go-ladybug)" && \
    mkdir /ladybug-lib && cp -a "$moddir"/lib/liblbug.so* /ladybug-lib/

# Copy source and build.
COPY platform/cartographer/ platform/cartographer/
RUN cd platform/cartographer && GOWORK=/workspace/.cache/ladybug/go.work CGO_ENABLED=1 go build -o /cartographer ./cmd

FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y ca-certificates libstdc++6 && rm -rf /var/lib/apt/lists/*
COPY --from=builder /cartographer /cartographer
COPY --from=builder /ladybug-lib/ /usr/local/lib/
ENV LD_LIBRARY_PATH=/usr/local/lib
USER 65532:65532
ENTRYPOINT ["/cartographer"]
