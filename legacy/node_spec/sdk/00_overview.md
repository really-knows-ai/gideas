# Foundry Node SDK: Overview

> **Version:** 1.0.0  
> **Status:** Implementation Contract  
> **Last Updated:** 2026-01-08

This directory contains the complete API reference for the Foundry Node SDK.

## Directory Structure

```
sdk/
├── 00_overview.md           # This file - core types and concepts
├── 01_artefact_operations.md
├── 02_legal_operations.md
├── 03_feedback_operations.md
├── 04_workitem_operations.md
├── 05_telemetry_operations.md
├── 06_health_topology.md
├── 07_error_handling.md
├── 08_export_operations.md
├── 08a_import_operations.md
├── 08b_export_import_protocol.md
└── proto/
    ├── types.proto          # Shared message definitions
    ├── sidecar.proto        # Session API (Port 35697)
    ├── system.proto         # System Services (Port 35698)
    ├── governor.proto       # Governor API
    └── queue.proto          # QueuePeer service (Federated Queue Mesh)
```

## SDK Modules

| Module | Responsibility |
|--------|----------------|
| **Artefact Manager** | Store, fetch, stamp artefacts via Archivist |
| **Legal Manager** | Record findings, cite laws, search library |
| **Feedback Manager** | Add, resolve, reject feedback items |
| **Workitem Manager** | Create workitems, update context |
| **Telemetry Manager** | Emit events, report friction |
| **Health Manager** | Heartbeat, readiness signaling |
| **Queue Manager** | Manages `queue.db`, gRPC peer discovery, scatter-gather logic. See [11_federated_queue_mesh.md](../11_federated_queue_mesh.md) |

## Related Documents

- [04_sdk_core.md](../04_sdk_core.md) — Narrative usage guide
- [05_session_api_grpc.md](../05_session_api_grpc.md) — gRPC contract overview
- [flow_spec/12_error_catalog.md](../../flow_spec/12_error_catalog.md) — Complete error reference
- [flow_spec/11_telemetry_catalog.md](../../flow_spec/11_telemetry_catalog.md) — Telemetry event reference

---

## 1. Core Types

### 1.1 NodeContext

The primary interface for node operations. Injected into the handler function.

```go
// Go
type NodeContext interface {
    // Artefact Operations
    StoreArtefact(ctx context.Context, kind, name string, content []byte) (*ArtefactVersion, error)
    FetchArtefact(ctx context.Context, kind string) ([]byte, error)
    FetchArtefactByName(ctx context.Context, kind, name string) ([]byte, error)
    FetchArtefactVersion(ctx context.Context, kind, name, version string) ([]byte, error)
    GetArtefactMetadata(ctx context.Context, kind string) (*ArtefactMetadata, error)
    StampArtefact(ctx context.Context, kind, stampType, role string, laws []LawCitation) error  // stampType: "inspection" or "approval"; laws required for approval
    ValidateArtefact(ctx context.Context, kind, requiredState string) (*ValidationResult, error)
    
    // Legal Operations
    RecordFinding(ctx context.Context, statement string, labels map[string]string) (string, error)
    SearchLibrary(ctx context.Context, query *LibraryQuery) ([]*Law, error)
    Cite(ctx context.Context, lawID string) error
    
    // Feedback Operations
    AddFeedback(ctx context.Context, target, severity, message string) (string, error)
    ResolveFeedback(ctx context.Context, feedbackID, message string) error
    RejectFix(ctx context.Context, feedbackID, message string) error
    UpdateFeedbackState(ctx context.Context, feedbackID, state string, justification *Justification) error
    
    // Workitem Operations
    CreateWorkitem(ctx context.Context, spec *WorkitemSpec) (string, error)
    UpdateContext(ctx context.Context, updates map[string]string, overwrite bool) error
    GetWorkitem() *Workitem
    
    // Telemetry
    RecordTelemetry(ctx context.Context, eventType string, payload map[string]interface{}) error
    ReportFriction(ctx context.Context, value float64, op FrictionOp, reason string, tags map[string]string) error
    
    // Health
    Heartbeat(ctx context.Context) error
    
    // Topology
    GetNodesByRole(ctx context.Context, role string) ([]string, error)
}
```

```typescript
// TypeScript
interface NodeContext {
    // Artefact Operations
    storeArtefact(kind: string, name: string, content: Buffer): Promise<ArtefactVersion>;
    fetchArtefact(kind: string): Promise<Buffer>;
    fetchArtefactByName(kind: string, name: string): Promise<Buffer>;
    fetchArtefactVersion(kind: string, name: string, version: string): Promise<Buffer>;
    getArtefactMetadata(kind: string): Promise<ArtefactMetadata>;
    stampArtefact(kind: string, stampType: StampType, role?: string, laws?: LawCitation[]): Promise<void>;  // stampType: "inspection" or "approval"; laws required for approval
    validateArtefact(kind: string, requiredState: string): Promise<ValidationResult>;
    
    // Legal Operations
    recordFinding(statement: string, labels?: Record<string, string>): Promise<string>;
    searchLibrary(query: LibraryQuery): Promise<Law[]>;
    cite(lawId: string): Promise<void>;
    
    // Feedback Operations
    addFeedback(target: string, severity: Severity, message: string): Promise<string>;
    resolveFeedback(feedbackId: string, message: string): Promise<void>;
    rejectFix(feedbackId: string, message: string): Promise<void>;
    updateFeedbackState(feedbackId: string, state: FeedbackState, justification?: Justification): Promise<void>;
    
    // Workitem Operations
    createWorkitem(spec: WorkitemSpec): Promise<string>;
    getWorkitem(): Workitem;
    
    // Telemetry
    recordTelemetry(eventType: string, payload: Record<string, unknown>): Promise<void>;
    reportFriction(value: number, op: FrictionOp, reason: string, tags?: Record<string, string>): Promise<void>;
    
    // Health
    heartbeat(): Promise<void>;
    
    // Topology
    getNodesByRole(role: string): Promise<string[]>;
}
```

---

### 1.2 Result

The return type for node handlers. Determines workitem routing.

```go
// Go
type Result struct {
    resultType ResultType
    output     string
    node       string
}

type ResultType int
const (
    ResultRouteToOutput ResultType = iota
    ResultRouteTo
    ResultComplete
)

// Constructors
func RouteToOutput(outputName string) Result
func RouteTo(nodeName string) Result
func Complete() Result
```

```typescript
// TypeScript
type Result = 
    | { type: 'route_to_output'; output: string }
    | { type: 'route_to'; node: string }
    | { type: 'complete' };

// Constructors
function routeToOutput(outputName: string): Result;
function routeTo(nodeName: string): Result;
function complete(): Result;
```

---

### 1.3 Handler Function

The signature for node handler implementations.

```go
// Go
type Handler func(ctx context.Context, node NodeContext, workitem *Workitem) Result
```

```typescript
// TypeScript
type Handler = (ctx: Context, node: NodeContext, workitem: Workitem) => Promise<Result>;
```

---

## 2. Common Data Types

### 2.1 ArtefactVersion

```go
type ArtefactVersion struct {
    Kind    string    // GovernedArtefact kind
    Name    string    // Filename
    Version string    // SHA256 hash (e.g., "sha256:abc123...")
}
```

### 2.2 ArtefactMetadata

```go
type ArtefactMetadata struct {
    Kind          string
    Name          string
    LatestVersion string
    Versions      []VersionInfo
    Passport      []Stamp
}

type VersionInfo struct {
    Hash          string
    CreatedAt     time.Time
    CreatedByNode string
}

type Stamp struct {
    Role             string
    Type             string    // "inspection" or "approval"
    Node             string
    Timestamp        time.Time
    Hash             string    // Content hash at stamp time
    Signature        string    // Base64-encoded RSA signature
    CertificateChain []string  // PEM-encoded certificates
    Laws             []LawCitation // Present for "approval" type
}

type LawCitation struct {
    ID      string
    Version string
}

type StampType string
const (
    StampTypeInspection StampType = "inspection"  // Review marker
    StampTypeApproval   StampType = "approval"    // Governance certification
)
```

### 2.3 ValidationResult

```go
type ValidationResult struct {
    Valid        bool
    MissingRoles []string
}
```

### 2.4 Law

```go
type Law struct {
    ID            string
    Tier          int       // 1=Finding, 2=Ruling, 3=Statute, 4=Federal
    Statement     string
    Labels        map[string]string
    ExpiresAt     time.Time
    CitationCount int
    CreatedAt     time.Time
    CreatedBy     string
}
```

### 2.5 LibraryQuery

```go
type LibraryQuery struct {
    SemanticQuery string            // Natural language query
    Labels        map[string]string // Label filter (AND logic)
    Tier          int               // Filter by tier (0 = all)
    Limit         int               // Max results (default: 10)
}
```

### 2.6 Justification

```go
type Justification struct {
    Type        string   // "citation" | "novel_argument"
    CitationIDs []string // If type="citation"
    Argument    string   // If type="novel_argument"
}
```

### 2.7 Workitem

```go
type Workitem struct {
    ID               string
    Type             string
    Intent           string
    Priority         string
    State            string  // "Pending" | "Running" | "Completed" | "Failed"
    CurrentAssignee  string
    PreviousAssignee string
    Artefacts        []ArtefactRef
    Feedback         []FeedbackItem
    Context          map[string]string
}
```

### 2.8 FeedbackItem

```go
type FeedbackItem struct {
    ID           string
    Target       string
    Source       string
    Severity     string  // "LOW" | "MEDIUM" | "HIGH" | "CRITICAL"
    State        string  // "pending" | "actioned" | "wont-fix" | "disputed" | "rejected" | "resolved"
    Message      string
    LinkedRuling string
    History      []FeedbackEvent
}

type FeedbackEvent struct {
    Timestamp time.Time
    Author    string
    Role      string
    Action    string  // "opened" | "fixed" | "rejected" | "wont-fix" | "escalated"
    Message   string
}
```

### 2.9 FrictionOp

```go
type FrictionOp int
const (
    FrictionLog FrictionOp = iota  // Score += log(1 + value)
    FrictionAdd                     // Score += value
    FrictionMultiply                // Score *= value
    FrictionSet                     // Score = value
)
```

---

## 3. Core Principle: Async by Default

All SDK operations that involve network I/O (gRPC calls to the Sidecar, HTTP requests to external services) are **asynchronous by default**. They return a future/promise and do not block the Node's execution thread.

This ensures that a slow network dependency (e.g., a slow embedding provider, a cross-flow export to a lagging peer) does not cause cascading backpressure within the Node.

**Example (TypeScript):**
```typescript
// Async by default - returns immediately
const exportPromise = Node.export(ctx, request);

// Optional: block until completion if needed
const result = await exportPromise;
```

### 3.1 Opting into Synchronous Behavior

In rare cases where blocking behavior is required, a `WithSync()` option can be provided:

```typescript
// Opt-in to blocking behavior
const result = await Node.export(ctx, request, { sync: true });
```

This should be used with extreme caution, as it makes the Node vulnerable to network latency.
```
