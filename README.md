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
- **Docker** (Docker Desktop or equivalent)
- **kubectl**
- **A Kubernetes cluster** (Docker Desktop Kubernetes, Kind, or any conformant cluster)
- **[Buf CLI](https://buf.build/docs/installation)** (only if regenerating proto code)
- **[grpcurl](https://github.com/fullstorydev/grpcurl)** (for demo scripts)
- **[Ollama](https://ollama.com)** running locally on `localhost:11434` with these models pulled:
  ```bash
  ollama pull gemma4:31b-cloud
  ollama pull deepseek-v4-flash:cloud
  ollama pull kimi-k2.5:cloud
  ```
  And signed in to ollama.com:
  ```bash
  ollama login
  ```

## Getting Started

### 1. Install CRDs

```bash
make -C platform/operator install
kubectl apply -f platform/operator/config/crd/bases/
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

# Haiku demo nodes
for node in forge haiku-quench sort appraisal appraiser refine embassy; do
  docker build -t "$node:latest" --build-arg NODE="$node" -f nodes/Dockerfile .
done
```

If using Kind, load the images into the cluster:

```bash
for img in flow-operator flow-sidecar flow-archivist flow-librarian \
            flow-eventbus flow-frictionledger flow-monitor \
            forge haiku-quench sort appraisal appraiser refine embassy; do
  kind load docker-image "$img:latest" --name <cluster-name>
done
```

### 3. Deploy the Operator

```bash
# Pass your Ollama API key for in-cluster model pulling.
# Get one from https://ollama.com/settings/api-keys
export OLLAMA_API_KEY="your-key-here"

make -C platform/operator deploy IMG=flow-operator:latest
```

### 4. Deploy the Haiku demo

The Haiku demo runs a full Foundry Cycle — Forge, Sort, Quench, Appraisal, Appraiser, Refine — producing a syllable-validated, security-reviewed, and governance-stamped haiku.

```bash
kubectl apply -f nodes/haiku-manifests/flow.yaml
kubectl apply -f nodes/haiku-manifests/configmaps.yaml
```

The operator automatically creates all system services (Archivist, Librarian, EventBus, FrictionLedger, Monitor, Embassy, Ollama) and node deployments from the `FoundryFlow` and `FoundryNode` resources.

#### Phase 1 — Structural rules only

Seed a workitem with a safe prompt to see the flow complete under structural governance (3-line haiku, 5-7-5 syllables):

```bash
kubectl port-forward svc/flow-archivist 50054:50054 &
kubectl port-forward svc/flow-librarian 50058:50058 &

./tools/demo/new-haiku "write me a haiku about a quiet room"
```

Watch it run:

```bash
bash ./tools/haiku-watch/watch.sh haiku-<id>
```

The cycle completes when all stamps (`linter`, `appraise-security`, `approval`) are on the haiku.

#### Phase 2 — Add content laws, pick a fight

Now apply content governance laws that prohibit weather and atmospheric references:

```bash
kubectl apply -f nodes/haiku-manifests/laws.yaml
```

Then seed a workitem deliberately designed to violate them:

```bash
./tools/demo/new-haiku "A raging storm over the north sea"
```

The Forge generates a haiku — likely full of weather imagery. The Appraisal node checks the `no-weather` and `no-atmosphere` laws, finds violations, and the Refine node must strip out every meteorological and atmospheric reference. Watch the friction build as the cycle wrestles the haiku into compliance.

## The Haiku Flow

```
forge          # Generate haiku (LLM: gemma4:31b-cloud)
  │
sort           # Check stamps and feedback
  │
quench         # Validate syllable count (5-7-5), stamp linter, raise feedback
  │
sort           # Detect unaddressed feedback → route to refine
  │
refine         # Triage feedback (LLM: deepseek-v4-flash:cloud), action fix, revise haiku
  │
sort           # Re-check, route to quench for re-validation
  │
quench         # Re-validate, stamp linter if valid
  │
sort           # Missing appraise-security stamp → route to appraisal
  │
appraisal      # Evaluate actioned feedback, accept/reject fix, stamp appraise-security
  │
sort           # All stamps present, no unaddressed feedback → stamp approval
  │
COMPLETE       # Exit contract satisfied
```

Each `sort` visit checks the exit contract (`linter`, `appraise-security`, `approval`). Stamps and feedback determine the next node. The sort node stamps `approval` when all governance conditions are met.

## Troubleshooting

**Workitems stuck in Pending:** Check the operator logs (`kubectl logs -n operator-system deployment/operator-controller-manager`). The operator must be running and have all CRDs installed.

**LLM calls failing:** Ensure Ollama is running locally (`curl http://localhost:11434/api/tags`). The node containers reach the host at `host.docker.internal:11434`. Models must be pulled and you must be signed in (`ollama login`).

**Image cache issues (Docker Desktop):** Docker Desktop's containerd image store caches manifests by digest. If you rebuild an image with the same tag, pods may still use the old image. Use unique tags (`image:fix2`, `image:fix-v2`, etc.) to bypass the cache.

**Operator CrashLoopBackOff:** Verify all CRDs are installed (`kubectl get crd | grep flow.gideas.io`). Missing CRDs (especially `lawgroups`) will cause the operator to fail on startup.

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
