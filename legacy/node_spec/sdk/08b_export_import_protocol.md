# Node SDK: Export/Import Protocol

## 1. Supporting Types

### 1.1 Bundle Inspection

```go
// InspectBundle parses a bundle without importing it.
// Useful for validation, logging, or routing decisions.
func (n *Node) InspectBundle(bundle []byte) (*BundleManifest, error)

type BundleManifest struct {
    BundleVersion             string
    SourceFlow                string
    SourceWorkitemId          string
    ExportedAt                time.Time
    ExportNode                string
    ExportNodeCertFingerprint string
    WorkitemSpec              map[string]interface{}
    Artefacts                 []ArtefactEntry
}

type ArtefactEntry struct {
    Kind string
    Name string
    Hash string
    Path string
}
```

### 1.2 Treaty Query

```go
// GetTreaties returns the configured treaties for this Flow.
// Useful for debugging or introspection.
func (n *Node) GetTreaties(ctx context.Context) ([]Treaty, error)

type Treaty struct {
    Name                 string
    CAFingerprint        string
    AllowedSubjects      []string
    AllowedWorkitemTypes []string
}

// IsTrusted checks if a given CA fingerprint is trusted.
func (n *Node) IsTrusted(ctx context.Context, caFingerprint string) (bool, error)
```

---

## 2. gRPC Protocol

### 2.1 Sidecar Proto Additions

```protobuf
// sidecar.proto additions

service FoundrySidecar {
    // ... existing RPCs ...
    
    // Export creates a bundle and transmits to target.
    // SESSION TERMINATOR on success.
    rpc Export(ExportRequest) returns (ExportResponse);
    
    // Import unpacks and ingests a foreign bundle.
    rpc Import(ImportRequest) returns (ImportResponse);
    
    // InspectBundle parses a bundle without importing.
    rpc InspectBundle(InspectBundleRequest) returns (InspectBundleResponse);
}

message ExportRequest {
    string endpoint = 1;
    string flow_id = 2;              // Optional, for logging
    repeated string artefacts = 3;   // Empty = all
    int32 timeout_ms = 4;            // 0 = default (30s)
}

message ExportResponse {
    // Empty on success (workitem already completed)
    // Only returned on failure
}

message ImportRequest {
    bytes bundle = 1;
    ImportOptions options = 2;
}

message ImportOptions {
    string workitem_type = 1;        // Override type
    string intent_prefix = 2;        // Prepend to intent
    map<string, string> additional_context = 3;
}

message ImportResponse {
    string workitem_id = 1;          // New local workitem ID
}

message InspectBundleRequest {
    bytes bundle = 1;
}

message InspectBundleResponse {
    string bundle_version = 1;
    string source_flow = 2;
    string source_workitem_id = 3;
    string exported_at = 4;
    string export_node = 5;
    string export_node_cert_fingerprint = 6;
    bytes workitem_spec_json = 7;    // JSON-encoded spec
    repeated ArtefactEntry artefacts = 8;
}

message ArtefactEntry {
    string kind = 1;
    string name = 2;
    string hash = 3;
}
```

---

## 3. Capability Reference

| Capability | Required For | Description |
|------------|--------------|-------------|
| `EXPORT` | `Export()` | Bundle and transmit workitem to foreign Flow |
| `CREATE:workitem` | `Import()` | Create new workitem from bundle |
| `WRITE:artefact/*` | `Import()` | Store imported artefacts |
| `INSPECT:artefact/*` | `Import()` | Apply naturalization stamp (inspection type) |

**Note:** `EXPORT` implicitly requires `READ:artefact/*` to bundle artefacts.

---

## 4. Node Configuration Examples

### 4.1 Export Terminal Node

```yaml
apiVersion: flow.gideas.io/v1
kind: FoundryNode
metadata:
  name: export-to-execution
spec:
  image: "registry/nodes/exporter:v1.0"
  roles: ["exporter"]
  timeout: "60s"
  
  capabilities:
    - "READ:artefact/*"
    - "EXPORT"
  
  outputs: []
  isTerminal: true
  terminalContract: "exported"
  
  env:
    - name: EXPORT_TARGET_URL
      value: "https://flow-execute.external.com/ingress"
```

### 4.2 Ingress Node

```yaml
apiVersion: flow.gideas.io/v1
kind: FoundryNode
metadata:
  name: vendor-ingress
spec:
  image: "registry/nodes/ingress:v1.0"
  roles: ["importer"]
  timeout: "30s"
  
  capabilities:
    - "CREATE:workitem"
    - "WRITE:artefact/*"
    - "INSPECT:artefact/*"
  
  outputs:
    - name: "accepted"
      targetRole: "initial-processor"
    - name: "rejected"
      target: "terminal-rejected"
  
  # Embassy configuration for large bundles
  volumes:
    - name: diplomatic-pouch
      emptyDir:
        sizeLimit: "10Gi"
  
  volumeMounts:
    - name: diplomatic-pouch
      mountPath: /var/run/foundry/pouch
```

---

## 5. Error Code Reference

| Error | gRPC Status | Meaning |
|-------|-------------|---------|
| `EXPORT_NOT_TERMINAL` | `FAILED_PRECONDITION` | Node lacks `isTerminal: true` |
| `EXPORT_NO_CAPABILITY` | `PERMISSION_DENIED` | Node lacks `EXPORT` capability |
| `EXPORT_TARGET_UNREACHABLE` | `UNAVAILABLE` | Target endpoint is down |
| `EXPORT_REJECTED` | `PERMISSION_DENIED` | Target rejected the bundle |
| `BUNDLE_CORRUPT` | `INVALID_ARGUMENT` | Bundle cannot be unpacked |
| `HASH_MISMATCH` | `INVALID_ARGUMENT` | Artefact hash doesn't match manifest |
| `TREATY_VIOLATION` | `PERMISSION_DENIED` | Signer CA not trusted |
| `UNAUTHORIZED_SUBJECT` | `PERMISSION_DENIED` | Signer CN not in allowedSubjects |
| `ALREADY_IMPORTED` | `ALREADY_EXISTS` | Bundle was previously imported |
| `WORKITEM_TYPE_NOT_ALLOWED` | `PERMISSION_DENIED` | Type restricted by Treaty |
| `BUNDLE_TOO_LARGE` | `RESOURCE_EXHAUSTED` | Exceeds Treaty size limit |
