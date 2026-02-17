# System Services: The Archivist

> **Plane:** Data

A high-performance, persistent object store running locally within the namespace. It handles two distinct storage domains.

## 1. Multi-Tenant Access

The Archivist serves all trusted infrastructure via port **35698** (the System Services Port):

| Client | Use Case |
|--------|----------|
| **Sidecars** | Store/fetch artefact content for workitems |
| **Librarian** | Push cold snapshots and mutation logs |
| **Law Search** | Fetch cold snapshots and replay mutation logs |
| **Flow Monitor** | Optionally store telemetry buffer snapshots |

All clients authenticate via mTLS. The port 35698 boundary separates trusted infrastructure from user containers. User containers access artefacts exclusively through the Sidecar.

---

## 2. Pluggable Storage Backend

The Archivist separates **logic** (caching, versioning, deduplication, pruning) from **storage** (bits on disk/cloud).

**Storage Driver Interface:**

```go
type StorageDriver interface {
    // Stream content to backend
    Put(ctx context.Context, path string, content io.Reader) (size int64, err error)
    
    // Stream content from backend
    Get(ctx context.Context, path string) (content io.ReadCloser, err error)
    
    // Delete content
    Delete(ctx context.Context, path string) error
    
    // Check existence
    Exists(ctx context.Context, path string) (bool, error)
}
```

**Supported Drivers:**

| Driver | Backend | Layout | Use Case | Consistency |
|--------|---------|--------|----------|-------------|
| `filesystem` | Kubernetes PVC | `/data/artefacts/<wi>/<kind>/<hash>` | Air-gapped, local dev, high-performance local I/O | Strong (POSIX) |
| `blobstore` | S3, GCS, Azure Blob, MinIO | `s3://bucket/artefacts/<wi>/<kind>/<hash>` | Infinite scale, multi-region, cost-optimization | Eventual (masked by Hot Tier) |

**Driver Selection:**
```yaml
archivist:
  storage:
    backend: "filesystem"  # or "blobstore"
```

---

## 3. Consistency Model (Hot Tier Guarantee)

**The Archivist provides Strong Read-After-Write Consistency** regardless of the backend.

Even though S3 is historically eventually consistent, the Archivist's **Hot Tier (RAM cache)** masks this:

1. **Write:** Stream to Backend (S3) **AND** Cache in Hot Tier (RAM)
2. **Ack:** Return success to Node
3. **Immediate Read:** Node requests the file immediately
   - Archivist serves from **Hot Tier** (RAM)
   - Serves from RAM (avoids backend latency)
4. **Cache Eviction:** Hot Tier entries expire after TTL or LRU pressure
   - By this time, S3 has converged to consistent state

### Hot Tier Sizing Guidance

To effectively mask backend latency, the Hot Tier should be sized to hold the active working set of artefacts. Use the following formula as a starting point:

```
Recommended Hot Tier Size (in MB) = 
  (Average Artefact Size in MB) 
  × (Max Concurrent Workitems) 
  × (Average Artefacts per Workitem) 
  × 1.5 (for overhead)
```

This can be configured via Helm:

```yaml
archivist:
  hotTier:
    maxSize: "512Mi"
```

```
┌─────────────────────────────────────────────────────────────────────┐
│                         ARCHIVIST                                   │
│  ┌─────────────────┐      ┌─────────────────────────────────────┐  │
│  │    Hot Tier     │      │        Storage Driver               │  │
│  │   (RAM Cache)   │      │  (filesystem / blobstore)           │  │
│  │                 │      │                                     │  │
│  │  Write: Cache   │◄────▶│  Write: Persist                     │  │
│  │  Read: Serve    │      │  Read: Fallback (on cache miss)     │  │
│  │                 │      │                                     │  │
│  └─────────────────┘      └─────────────────────────────────────┘  │
└─────────────────────────────────────────────────────────────────────┘
```

**Implication:** Nodes always see their own writes immediately. Cross-node reads may hit the backend if the Hot Tier misses, but by then the backend has converged.

---

## 4. Data Residency

**Important distinction:**
- **Workitems (Metadata):** Always local to the Kubernetes cluster (stored in etcd as CRDs)
- **Artefacts (Data):** May reside in external Object Storage if `blobstore` driver is configured

This means:
- **Sovereignty:** Workitem lifecycle, routing, and provenance remain within the cluster
- **Data Location:** Artefact content may live in S3/GCS (subject to bucket region/compliance settings)
- **Export/Import:** Bundles are assembled from wherever artefacts reside (Archivist abstracts this)

---

## 5. Artefact Isolation Model (Silo, Not Library)

**Artefacts are strictly isolated per-Workitem.** Each artefact belongs to exactly one Workitem.

| Constraint | Enforcement | Implication |
|------------|-------------|-------------|
| **Physical Key** | Storage layout: `<workitem_id>/<kind>/<name>/<hash>` | Workitem ID is the root directory |
| **SDK Enforcement** | No `targetWorkitemID` parameter exists | SDK auto-injects current context |
| **Sidecar Enforcement** | Context bound to leased Workitem | Won't serve requests for unowned IDs |

**Why This Matters:**
- Artefacts are scoped to their owning Workitem
- Every byte of data belongs to exactly one Workitem
- Cross-Workitem access is enforced at the SDK and Sidecar layers

### 5.1 Template Pattern (Shared Reference Documents)

Since sharing is impossible, use these patterns for common assets:

| Pattern | Storage | Use Case |
|---------|---------|----------|
| **Container Image** | Baked into Node container at `/templates/` | Immutable, versioned with code |
| **ConfigMap** | Mounted to Node via K8s volume | Environment-specific, managed by GitOps |
| **Injection** | Entry Node calls `StoreArtefact()` | Creates unique copy for Workitem |

**Example: Template Injection**
```go
// Forge Node: inject template as artefact
func (n *ForgeNode) Assigned(ctx context.Context, item Workitem) Result {
    // Read immutable template from container
    template, _ := os.ReadFile("/templates/petition_template.md")
    
    // Create unique copy for this Workitem
    n.StoreArtefact(ctx, "petition-draft", "draft.md", template)
    
    return RouteToOutput("default")
}
```

---

## 6. Logical Storage Layout

```
<workitem_id>/<kind>/<name>/
  ├── <hash_v1>                 # Version 1 content
  ├── <hash_v1>.passport.json   # Version 1 stamps (passport metadata)
  ├── <hash_v2>                 # Version 2 content
  ├── <hash_v2>.passport.json   # Version 2 stamps (passport metadata)
  └── latest -> <hash_v2>       # Pointer to latest
```

The driver maps this logical path to physical storage:
- **filesystem:** `/data/artefacts/<workitem_id>/<kind>/<name>/<hash>`
- **blobstore:** `s3://bucket/artefacts/<workitem_id>/<kind>/<name>/<hash>`

### 6.1 Passport Storage

Passport stamps are stored alongside artefact content as `<hash>.passport.json`. This co-location ensures:

1. **Atomic provenance:** Content and its stamps travel together during export/import
2. **Version-specific stamps:** Each content version has its own passport
3. **Content-addressable integrity:** Stamps reference the exact hash they certify

**Passport JSON Structure:**
```json
{
  "stamps": [
    {
      "role": "security-reviewer",
      "type": "inspection",
      "node": "security-quench-pod-1",
      "timestamp": "2026-01-04T14:30:00Z",
      "hash": "sha256:def456...",
      "signature": "base64...",
      "certificateChain": ["-----BEGIN CERTIFICATE-----..."]
    },
    {
      "role": "compliance-officer",
      "type": "approval",
      "node": "compliance-node-pod-1",
      "timestamp": "2026-01-04T14:35:00Z",
      "hash": "sha256:def456...",
      "signature": "base64...",
      "certificateChain": ["-----BEGIN CERTIFICATE-----..."],
      "laws": [
        { "id": "GDPR-17", "version": "2024-01", "section": "3.2" }
      ]
    }
  ]
}
```

**Stamp Invalidation:** When new content is stored (new hash), the old passport remains with the old version. The new version starts with an empty passport. Governance checks validate stamps against the current `latestVersion` hash.

---

## 7. gRPC Contract (Sidecar → Archivist)

```protobuf
service ArchivistArtefacts {
  // Content operations
  rpc Store(ArtefactStoreRequest) returns (ArtefactStoreResponse);
  rpc Fetch(ArtefactFetchRequest) returns (ArtefactFetchResponse);
  rpc GetVersions(ArtefactVersionsRequest) returns (ArtefactVersionsResponse);
  rpc Prune(ArtefactPruneRequest) returns (Empty);
  
  // Passport operations
  rpc AddStamp(AddStampRequest) returns (AddStampResponse);
  rpc GetPassport(GetPassportRequest) returns (GetPassportResponse);
}

message AddStampRequest {
  string workitem_id = 1;
  string kind = 2;
  string name = 3;
  string version = 4;  // Hash of the content being stamped
  Stamp stamp = 5;
}

message AddStampResponse {
  // Empty on success
}

message GetPassportRequest {
  string workitem_id = 1;
  string kind = 2;
  string name = 3;
  string version = 4;  // Empty = latest
}

message GetPassportResponse {
  string version = 1;  // Hash of the content
  repeated Stamp stamps = 2;
}
```

---

## 8. Artefact Operations Flow

### 8.1 Store Flow

```
Node calls StoreArtefact(kind, name, content)
        │
        ▼
┌─────────────────────────────────────────────────────────────┐
│ Sidecar                                                     │
│ 1. Check capability: WRITE:artefact/<kind>                  │
│ 2. Compute hash = SHA256(content)                           │
│ 3. Call Archivist.Store(workitem_id, kind, name, content)   │
└─────────────────────────────────────────────────────────────┘
        │
        ▼
┌─────────────────────────────────────────────────────────────┐
│ Archivist                                                   │
│ 1. Cache in Hot Tier (RAM)                                  │
│ 2. Write to backend via StorageDriver.Put()                 │
│    - filesystem: /data/artefacts/<wi>/<kind>/<name>/<hash>  │
│    - blobstore: s3://bucket/artefacts/<wi>/<kind>/<hash>    │
│ 3. Update 'latest' pointer                                  │
│ 4. Return hash                                              │
└─────────────────────────────────────────────────────────────┘
        │
        ▼
┌─────────────────────────────────────────────────────────────┐
│ Sidecar                                                     │
│ 1. Patch Workitem CRD:                                      │
│    - Add/update artefact entry in status.artefacts[]        │
│    - Set latestVersion = hash                               │
│    - Append to versions[] history                           │
│ 2. Return StoreArtefactResponse{kind, name, version: hash}  │
└─────────────────────────────────────────────────────────────┘
```

### 8.2 Fetch Flow

```
Node calls FetchArtefact(kind, name, version?)
        │
        ▼
┌─────────────────────────────────────────────────────────────┐
│ Sidecar                                                     │
│ 1. Check capability: READ:artefact/<kind>                   │
│ 2. Resolve version (empty = read latestVersion from CRD)    │
│ 3. Call Archivist.Fetch(workitem_id, kind, name, version)   │
└─────────────────────────────────────────────────────────────┘
        │
        ▼
┌─────────────────────────────────────────────────────────────┐
│ Archivist                                                   │
│ 1. Check Hot Tier (RAM cache)                               │
│    - HIT: Return immediately (strong consistency)           │
│    - MISS: Continue to step 2                               │
│ 2. Read from backend via StorageDriver.Get()                │
│ 3. Cache in Hot Tier for future reads                       │
│ 4. Return content bytes                                     │
└─────────────────────────────────────────────────────────────┘
        │
        ▼
┌─────────────────────────────────────────────────────────────┐
│ Sidecar                                                     │
│ 1. Verify: SHA256(content) == requested version hash        │
│ 2. REJECT if mismatch (ARTEFACT_CORRUPTED)                  │
│ 3. Return FetchArtefactResponse{kind, name, version, content}
└─────────────────────────────────────────────────────────────┘
```

### 8.3 Stamp Flow

```
Node calls StampArtefact(kind, stampType, role, laws)
        │
        ▼
┌─────────────────────────────────────────────────────────────┐
│ Sidecar                                                     │
│ 1. Check capability based on stampType:                     │
│    - "inspection": INSPECT:artefact/<kind>                  │
│    - "approval": APPROVE:artefact/<kind>                    │
│ 2. If stampType="approval", verify laws is non-empty        │
│ 3. Fetch latest version content (via Archivist)             │
│ 4. Verify hash matches CRD latestVersion                    │
│ 5. Sign: signature = RSA_SIGN(privateKey, hash||type||role) │
│ 6. Build stamp:                                             │
│    - role: from parameter (or default if single role)       │
│    - type: stampType                                        │
│    - node: node name                                        │
│    - timestamp: now                                         │
│    - hash: content hash at stamp time                       │
│    - signature: signature                                   │
│    - certificateChain: [node cert, operator cert, ...]      │
│    - laws: law citations (for approval type)                │
│ 7. Call Archivist.AddStamp(workitem_id, kind, name, hash, stamp) │
└─────────────────────────────────────────────────────────────┘
```

---

## 9. Retention & Capability Enforcement

**Retention:** The Archivist runs a background pruning loop based on `FoundryFlow.spec.artefactRetention` config (maxVersions / maxAge). Old versions are deleted but latest is always preserved.

**Capability Enforcement:** The Sidecar enforces artefact capabilities before calling Archivist:
- `READ:artefact/<kind>` or `READ:artefact/*` - required for Fetch
- `WRITE:artefact/<kind>` or `WRITE:artefact/*` - required for Store
- `INSPECT:artefact/<kind>` or `INSPECT:artefact/*` - required for inspection stamps
- `APPROVE:artefact/<kind>` or `APPROVE:artefact/*` - required for approval stamps


---

## 10. Backup Integration

The Archivist maintains an internal SQLite database for its metadata index (artefact locations, versions, hot tier state). This database is backed up by the central **Backup Service**.

### 10.1 Backup Interface

The Archivist implements the `BackupSource` gRPC service defined in `proto/backup.proto`. When the Backup Service calls `StreamDatabaseSnapshot`, the Archivist uses the SQLite Online Backup API to create a consistent snapshot of its metadata database and streams it back in chunks.

### 10.2 Artefact Content Durability

The backup of the Archivist's metadata index is distinct from the durability of the artefact content itself.

| Storage Backend | Content Durability | Backup Responsibility |
|---|---|---|
| `blobstore` (S3, GCS, Azure Blob) | Handled by the cloud provider (11 nines durability). | Cloud provider. |
| `filesystem` (PVC) | Depends on the underlying storage class. | Kubernetes storage administrator. |

The Backup Service backs up the **metadata index**, which allows the Archivist to know what artefacts exist and where they are located. The actual artefact bytes are protected by the storage backend's own durability mechanisms.

### 10.3 Restoration

In a disaster recovery scenario, the Archivist metadata database is restored by the cluster administrator using the following procedure:

1. Retrieve the desired snapshot from the Backup Service's storage destination.
2. Scale the Archivist deployment to 0 replicas.
3. Copy the snapshot file to the Archivist's PVC, replacing the existing metadata database file.
4. Scale the Archivist deployment back to 1 replica.

The Archivist will start with the restored metadata state. If artefact content was lost from the storage backend (e.g., a PVC was corrupted), the metadata will point to missing files, which will result in `ARTEFACT_NOT_FOUND` errors on fetch.
