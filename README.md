# Foundry Flow

A governed workflow runtime on Kubernetes. Work progresses through adversarial cycles of creation, validation, review, and refinement — each step producing auditable artefacts with cryptographic proof of every governance checkpoint. Structured feedback drives iterative refinement until exit contracts are satisfied, and friction makes the real-time cost of governance visible at every layer.

## Repository Structure

| Directory | Description |
|-----------|-------------|
| `specs/` | [Technical specification](specs/README.md) — the authoritative source of truth |
| `proto/` | Protocol Buffer definitions (the wire contract) |
| `operator/` | Control plane — Kubebuilder controller managing Flows, Workitems, and CRDs |
| `sidecar/` | Data plane — in-pod proxy handling identity, capability enforcement, and service routing |
| `sdk/go/` | Go SDK for node developers |
| `nodes/` | Standard node implementations and the Haiku demo cycle |
| `archivist/` | System service — content-addressable artefact storage (SQLite) |
| `librarian/` | System service — law and governance store (SQLite) |
| `monitor/` | System service — Flow Monitor for friction and telemetry (SQLite) |
| `tools/` | Spec linter, demo scripts, and the `haiku-watch` CLI |

## Prerequisites

- **Go 1.25+**
- **Docker**
- **kubectl**
- **A Kubernetes cluster** (Kind, Docker Desktop, or any conformant cluster)
- **[Buf CLI](https://buf.build/docs/installation)** (only if regenerating proto code)
- **[grpcurl](https://github.com/fullstorydev/grpcurl)** (only for the demo scripts)

## Getting Started

### 1. Install CRDs

```bash
make -C operator install
```

### 2. Build container images

From the repo root:

```bash
# Operator
docker build -t flow-operator:latest -f operator/Dockerfile .

# Sidecar
docker build -t flow-sidecar:latest -f sidecar/Dockerfile .

# System services
docker build -t flow-archivist:latest -f archivist/Dockerfile .
docker build -t flow-librarian:latest -f librarian/Dockerfile .
docker build -t flow-monitor:latest   -f monitor/Dockerfile .

# Haiku demo nodes (one image per node)
for node in forge haiku-quench sort haiku-appraise haiku-refine; do
  docker build -t "$node:latest" --build-arg NODE="$node" -f nodes/Dockerfile .
done
```

If using Kind, load the images into the cluster:

```bash
for img in flow-operator flow-sidecar flow-archivist flow-librarian flow-monitor \
           forge haiku-quench sort haiku-appraise haiku-refine; do
  kind load docker-image "$img:latest" --name <cluster-name>
done
```

### 3. Deploy the Operator

```bash
make -C operator deploy
```

### 4. Deploy system services

```bash
kubectl apply -f archivist/deployment.yaml
kubectl apply -f librarian/deployment.yaml
kubectl apply -f monitor/deployment.yaml
```

### 5. Deploy the Haiku demo

The Haiku demo runs a full Foundry Cycle — Forge, Quench, Appraise, Sort, Refine — producing governed haiku artefacts.

```bash
kubectl apply -f nodes/haiku-manifests/flow.yaml
kubectl apply -f nodes/haiku-manifests/configmaps.yaml
kubectl apply -f nodes/haiku-manifests/deployments.yaml
```

### 6. Seed a workitem

Port-forward the Archivist and Librarian, then use the demo scripts:

```bash
kubectl port-forward svc/flow-archivist 50054:50054 &
kubectl port-forward svc/flow-librarian 50056:50056 &

# Optionally add a governance law
./tools/demo/add-law "The haiku must evoke a season"

# Create a new haiku workitem
./tools/demo/new-haiku "write me a haiku about autumn leaves"
```

### 7. Watch it

```bash
go run ./tools/haiku-watch --workitem haiku-<id> --archivist localhost:50054
```

## Development

```bash
make build          # Build all binaries (sidecar, nodes, system services)
make test           # Run all unit tests
make test-all       # Including operator tests
make proto          # Regenerate Go code from proto definitions
make fmt            # Format
make vet            # Vet
make tidy           # go mod tidy across all workspace modules
```

See `make help` for the full target list.

## Specification

The full technical specification lives in [`specs/`](specs/README.md). Start with [Concepts](specs/01-concepts/00-overview.md), then [Architecture](specs/01-concepts/01-architecture.md).
