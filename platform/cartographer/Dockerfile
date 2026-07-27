# Multi-stage build for the Cartographer service.
# CGO_ENABLED=1 is required because go-ladybug is a CGo wrapper.
# ponytail: this Dockerfile assumes go-ladybug is resolved from a module cache
# or vendored directory. Production builds must either vendor the module or
# pre-download liblbug.a (run: cd /tmp && cp -r $GOMODCACHE/... && bash download_lbug.sh).
FROM golang:1.25 AS builder
WORKDIR /workspace
ENV GOWORK=off

# Install CGo build dependencies (gcc + libstdc++-dev for the C++ CGo library).
RUN apt-get update && apt-get install -y gcc libstdc++-dev && rm -rf /var/lib/apt/lists/*

# Copy dependencies first for layer caching.
COPY gen/go.mod gen/go.sum gen/
COPY gen/flow/ gen/flow/
COPY platform/pkg/eventbus/ platform/pkg/eventbus/
COPY platform/cartographer/go.mod platform/cartographer/go.sum platform/cartographer/
RUN cd platform/cartographer && go mod download

# Copy source and build.
COPY platform/cartographer/ platform/cartographer/
RUN cd platform/cartographer && CGO_ENABLED=1 go build -o /cartographer ./cmd

FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y ca-certificates && rm -rf /var/lib/apt/lists/*
COPY --from=builder /cartographer /cartographer
USER 65532:65532
ENTRYPOINT ["/cartographer"]
