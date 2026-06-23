# Foundry Flow

A governed workflow runtime on Kubernetes. Work progresses through adversarial cycles of creation, validation, review, and refinement — each step producing auditable artefacts with cryptographic proof of every governance checkpoint. Structured feedback drives iterative refinement until exit contracts are satisfied, and friction makes the real-time cost of governance visible at every layer.

## Repository Structure

| Directory | Description |
|-----------|-------------|
| `specs/` | [Technical specification](specs/README.md) — the authoritative source of truth |
| `proto/` | Protocol Buffer definitions (the wire contract) |
| `gen/` | Generated Go code from proto definitions |
| `platform/operator/` | Control plane — Kubebuilder controller managing Flows, Workitems, and CRDs |
| `platform/sidecar/` | Data plane — in-pod proxy handling identity, capability enforcement, and service routing |
| `platform/archivist/` | System service — content-addressable artefact storage (SQLite) |
| `platform/librarian/` | System service — law and governance store (SQLite) |
| `platform/eventbus/` | System service — Event Bus |
| `platform/frictionledger/` | System service — Friction Ledger |
| `platform/federation/` | System service — Cross-Flow Federation controller |
| `platform/monitor/` | System service — Flow Monitor for friction and telemetry |
| `sdk/go/` | Go SDK for node developers |
| `nodes/` | Standard node implementations |
| `charts/` | Helm charts for deployment |
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
make -C platform/operator install
```

### 2. Build container images

From the repo root:

```bash
# Operator
docker build -t flow-operator:latest -f platform/operator/Dockerfile .

# Sidecar
docker build -t flow-sidecar:latest -f platform/sidecar/Dockerfile .

# System services
docker build -t flow-archivist:latest      -f platform/archivist/Dockerfile .
docker build -t flow-librarian:latest      -f platform/librarian/Dockerfile .
docker build -t flow-eventbus:latest       -f platform/eventbus/Dockerfile .
docker build -t flow-frictionledger:latest -f platform/frictionledger/Dockerfile .
docker build -t flow-monitor:latest        -f platform/monitor/Dockerfile .

# Haiku demo nodes (one image per node)
for node in forge haiku-quench sort appraise refine; do
  docker build -t "$node:latest" --build-arg NODE="$node" -f nodes/Dockerfile .
done
```

If using Kind, load the images into the cluster:

```bash
for img in flow-operator flow-sidecar flow-archivist flow-librarian \
           flow-eventbus flow-frictionledger flow-monitor \
           forge haiku-quench sort appraise refine; do
  kind load docker-image "$img:latest" --name <cluster-name>
done
```

### 3. Deploy the Operator

```bash
make -C platform/operator deploy
```

### 4. Deploy system services

```bash
kubectl apply -f platform/archivist/deployment.yaml
kubectl apply -f platform/librarian/deployment.yaml
kubectl apply -f platform/eventbus/deployment.yaml
kubectl apply -f platform/frictionledger/deployment.yaml
kubectl apply -f platform/monitor/deployment.yaml
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
