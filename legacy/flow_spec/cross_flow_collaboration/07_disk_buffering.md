# Cross-Flow Collaboration: Disk Buffering for Large Bundles

## 1. The Problem: Memory Cliff

The naive implementation loads entire bundles into RAM before transmission. This imposes a hard ceiling (~500MB) before OOM crashes occur. Multi-gigabyte transfers are impossible.

## 2. The Solution: Ephemeral Disk Buffering

Use streaming I/O with disk as an intermediate buffer:

- **Export:** Stream artefacts from Archivist → Disk → Stream to Network
- **Import:** Stream from Network → Disk → Stream to Archivist

**Memory usage becomes O(1) regardless of bundle size.**

## 3. The Embassy Node Pattern

Nodes configured for cross-flow I/O are called **Embassy Nodes**. They have a dedicated scratch volume called the **Diplomatic Pouch**.

**Volume Configuration:**
- **Type:** Kubernetes `emptyDir` (ephemeral, pod-local)
- **Mount Path:** `/var/run/foundry/pouch`
- **Shared:** Mounted in BOTH the Node container and the Sidecar container

```yaml
apiVersion: flow.gideas.io/v1
kind: FoundryNode
metadata:
  name: export-embassy
spec:
  image: "registry/nodes/exporter:v1.0"
  roles: ["exporter"]
  capabilities:
    - "READ:artefact/*"
    - "EXPORT"
  isTerminal: true
  terminalContract: "exported"
  
  # Embassy configuration
  volumes:
    - name: diplomatic-pouch
      emptyDir:
        sizeLimit: "10Gi"  # Adjust based on expected bundle size
  
  volumeMounts:
    - name: diplomatic-pouch
      mountPath: /var/run/foundry/pouch
```

The Operator injects the same volume mount into the Sidecar container, enabling zero-copy handoff.

## 4. Streaming Data Flow

```
Export Flow:
┌─────────────────────────────────────────────────────────────────────────┐
│                           Export Node Pod                               │
│  ┌─────────────┐      ┌─────────────┐      ┌─────────────────────────┐  │
│  │   Sidecar   │      │  Pouch Vol  │      │     Node Container      │  │
│  │             │      │ (emptyDir)  │      │                         │  │
│  │  Archivist  │─────▶│  bundle.fb  │─────▶│  HTTP Chunked Transfer  │──┼──▶ Network
│  │  (Stream)   │      │             │      │  (Stream from disk)     │  │
│  └─────────────┘      └─────────────┘      └─────────────────────────┘  │
└─────────────────────────────────────────────────────────────────────────┘

Import Flow:
┌─────────────────────────────────────────────────────────────────────────┐
│                          Ingress Node Pod                               │
│  ┌─────────────────────────┐      ┌─────────────┐      ┌─────────────┐  │
│  │     Node Container      │      │  Pouch Vol  │      │   Sidecar   │  │
│  │                         │      │ (emptyDir)  │      │             │  │
│  │  HTTP Handler           │─────▶│  bundle.fb  │─────▶│  Archivist  │  │
│  │  (Stream to disk)       │      │             │      │  (Stream)   │  │
│  └─────────────────────────┘      └─────────────┘      └─────────────┘  │
└─────────────────────────────────────────────────────────────────────────┘
       ▲
       │
    Network
```

## 5. Export Execution (Disk-Buffered)

When a node calls `Export()`:

1. **Stream Fetch:** Sidecar opens streaming RPC to Archivist for each artefact
2. **Build to Disk:** Sidecar writes artefact bytes directly to `/var/run/foundry/pouch/bundle.fb` as a tar stream
3. **Sign:** Sidecar computes hash/signature over the file on disk (streaming read)
4. **Transmit:** Node opens HTTP connection with `Transfer-Encoding: chunked` and streams file to target
5. **Cleanup:** On success, Sidecar deletes the bundle file and marks Workitem as Completed

## 6. Import Execution (Disk-Buffered)

When an Ingress Node receives a bundle:

1. **Receive Stream:** Node's HTTP handler streams request body to `/var/run/foundry/pouch/incoming.fb`
2. **Handoff:** Node calls `sdk.Import(ctx, "/var/run/foundry/pouch/incoming.fb")` (path, not bytes)
3. **Validate:** Sidecar reads file, validates signature (streaming hash computation)
4. **Unpack & Store:** Sidecar extracts artefacts and streams them to local Archivist
5. **Cleanup:** Sidecar deletes the bundle file after successful import

## 7. Path-Based Import API

To enable zero-copy handoff, the Import RPC accepts a file path instead of a byte array:

```protobuf
message ImportRequest {
    oneof source {
        bytes bundle = 1;              // Small bundles (< 100MB) - in-memory
        string bundle_path = 3;        // Large bundles - disk path
    }
    ImportOptions options = 2;
}
```

The SDK automatically chooses the appropriate mode based on bundle size.

## 8. Streaming Archivist RPCs

To support disk buffering, the Archivist proxy RPCs become streaming:

```protobuf
// Streaming fetch - returns chunks
rpc FetchArtefactStream(FetchArtefactRequest) returns (stream ArtefactChunk);

// Streaming store - accepts chunks
rpc StoreArtefactStream(stream StoreArtefactChunk) returns (StoreArtefactResponse);

message ArtefactChunk {
    bytes data = 1;           // Chunk of artefact content
    bool is_last = 2;         // True if this is the final chunk
}

message StoreArtefactChunk {
    string kind = 1;          // Only set on first chunk
    string name = 2;          // Only set on first chunk
    bytes data = 3;           // Chunk of content
    bool is_last = 4;         // True if this is the final chunk
}
```

## 9. Performance Characteristics

| Aspect | In-Memory (Small) | Disk-Buffered (Large) |
|--------|-------------------|----------------------|
| **Max Bundle Size** | ~500MB | Limited by disk (10GB+) |
| **Memory Usage** | O(n) - bundle size | O(1) - constant ~64MB |
| **Latency** | Lower (no disk I/O) | Higher (2x disk I/O) |
| **Reliability** | OOM risk | Stable |

**Recommendation:** Use disk buffering for bundles > 100MB. The SDK handles this automatically.

## 10. Cleanup Policy

The Diplomatic Pouch is ephemeral storage. Cleanup occurs:

- **On Success:** Immediately after export/import completes
- **On Failure:** Immediately after error handling
- **On Pod Restart:** Kubernetes deletes `emptyDir` contents automatically

**No persistent state** is stored in the pouch. It's strictly a transit buffer.
