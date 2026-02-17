# Node SDK: Import Operations

## 1. Overview

`Import()` unpacks a Foundry Bundle, validates it against Treaties, and creates a new local Workitem with naturalized artefacts.

**Key Principle:** Import is an atomic ingestion. Either everything succeeds (Workitem created, artefacts stored and stamped) or nothing happens.

---

## 2. Signature

```go
// Import unpacks a Foundry Bundle, validates it against Treaties, and creates
// a new local Workitem with naturalized artefacts.
//
// This is an ATOMIC operation:
//   1. Unpack bundle
//   2. Validate integrity (hashes)
//   3. Validate signature (Treaty)
//   4. Create Workitem
//   5. Store artefacts
//   6. Stamp artefacts (Naturalization)
//
// On success: Returns the new local Workitem ID
// On failure: Returns error, no Workitem created, no artefacts stored
//
// For small bundles (<100MB), pass the bytes directly.
// For large bundles, use ImportFromPath for disk-buffered zero-copy handoff.
func (n *Node) Import(ctx context.Context, bundle []byte) (string, error)

// ImportFromPath imports a bundle from disk (zero-copy handoff to Sidecar).
// Use this for large bundles to avoid loading into memory.
// The bundle file is automatically deleted after successful import.
func (n *Node) ImportFromPath(ctx context.Context, path string) (string, error)
```

---

## 3. Import Options

```go
// ImportOptions provides additional configuration for import.
type ImportOptions struct {
    // WorkitemType overrides the type from the bundle manifest.
    // Use this to map foreign types to local types.
    // Empty string means use the type from the bundle.
    WorkitemType string
    
    // IntentPrefix prepends text to the imported intent.
    // Useful for marking imported workitems.
    // Example: "Imported from Vendor: "
    IntentPrefix string
    
    // AdditionalContext adds extra fields to status.context.
    // Merged with the standard foreignSource fields.
    AdditionalContext map[string]string
}

// ImportWithOptions is the extended import function (in-memory).
func (n *Node) ImportWithOptions(ctx context.Context, bundle []byte, opts ImportOptions) (string, error)

// ImportFromPathWithOptions is the extended import function (disk-buffered).
func (n *Node) ImportFromPathWithOptions(ctx context.Context, path string, opts ImportOptions) (string, error)
```

---

## 4. Usage Example: Small Bundle (In-Memory)

```go
func (n *IngressNode) HandleHTTPImport(w http.ResponseWriter, r *http.Request) {
    ctx := r.Context()
    
    // Read bundle from request body (only for small bundles)
    bundle, err := io.ReadAll(r.Body)
    if err != nil {
        http.Error(w, "failed to read bundle", http.StatusBadRequest)
        return
    }
    
    // Import the bundle
    workitemID, err := n.Import(ctx, bundle)
    if err != nil {
        switch {
        case errors.Is(err, ErrTreatyViolation):
            http.Error(w, "unauthorized sender", http.StatusForbidden)
        case errors.Is(err, ErrAlreadyImported):
            http.Error(w, "already imported", http.StatusConflict)
        default:
            http.Error(w, err.Error(), http.StatusBadRequest)
        }
        return
    }
    
    // Success - return new workitem ID
    w.Header().Set("Content-Type", "application/json")
    json.NewEncoder(w).Encode(map[string]string{
        "workitemId": workitemID,
    })
}
```

---

## 5. Usage Example: With Options

```go
func (n *IngressNode) Assigned(ctx context.Context, item Workitem) Result {
    // Fetch the bundle from the artefact (external system deposited it)
    bundleBytes := n.FetchArtefact(ctx, item, "incoming-bundle")
    
    // Import with custom options
    workitemID, err := n.ImportWithOptions(ctx, bundleBytes, ImportOptions{
        WorkitemType: "local-review-v1",  // Map to local type
        IntentPrefix: "[Vendor Import] ",
        AdditionalContext: map[string]string{
            "import_batch":  item.Spec.BatchID,
            "import_reason": "quarterly_vendor_sync",
        },
    })
    
    if err != nil {
        n.AddFeedback(ctx, FeedbackItem{
            Target:   "incoming-bundle",
            Severity: "HIGH",
            Message:  fmt.Sprintf("Import failed: %v", err),
        })
        return RouteToOutput("import-failed")
    }
    
    // Successfully imported - this workitem's job is done
    return RouteToOutput("import-succeeded")
}
```

---

## 6. Usage Example: Large Bundle (Disk-Buffered)

For bundles > 100MB, use disk buffering to avoid OOM:

```go
const (
    PouchPath       = "/var/run/foundry/pouch"
    MaxMemoryBundle = 100 * 1024 * 1024 // 100MB threshold
)

func (n *EmbassyNode) HandleLargeImport(w http.ResponseWriter, r *http.Request) {
    ctx := r.Context()
    
    // Check Content-Length to decide buffering strategy
    if r.ContentLength > MaxMemoryBundle || r.ContentLength == -1 {
        // Stream to disk
        bundlePath := filepath.Join(PouchPath, fmt.Sprintf("incoming-%d.fb", time.Now().UnixNano()))
        
        f, err := os.Create(bundlePath)
        if err != nil {
            http.Error(w, "failed to create buffer", http.StatusInternalServerError)
            return
        }
        
        // Stream request body directly to disk
        _, err = io.Copy(f, r.Body)
        f.Close()
        if err != nil {
            os.Remove(bundlePath)
            http.Error(w, "failed to receive bundle", http.StatusBadRequest)
            return
        }
        
        // Zero-copy handoff to Sidecar
        workitemID, err := n.ImportFromPath(ctx, bundlePath)
        // bundlePath is automatically deleted by Sidecar after import
        
        if err != nil {
            http.Error(w, err.Error(), http.StatusBadRequest)
            return
        }
        
        w.Header().Set("Content-Type", "application/json")
        json.NewEncoder(w).Encode(map[string]string{"workitem_id": workitemID})
        return
    }
    
    // Small bundle - use in-memory path
    bundle, _ := io.ReadAll(r.Body)
    workitemID, err := n.Import(ctx, bundle)
    // ... handle response
}
```

---

## 7. Error Handling

```go
// Import errors
var (
    // ErrBundleCorrupt indicates the bundle cannot be unpacked.
    ErrBundleCorrupt = errors.New("bundle corrupt or invalid format")
    
    // ErrHashMismatch indicates artefact content doesn't match manifest.
    ErrHashMismatch = errors.New("artefact hash mismatch")
    
    // ErrTreatyViolation indicates the signer CA is not trusted.
    ErrTreatyViolation = errors.New("treaty violation: untrusted CA")
    
    // ErrUnauthorizedSubject indicates the signer CN is not allowed.
    ErrUnauthorizedSubject = errors.New("unauthorized subject")
    
    // ErrAlreadyImported indicates this bundle was already imported.
    ErrAlreadyImported = errors.New("bundle already imported")
    
    // ErrWorkitemTypeNotAllowed indicates the type is restricted by Treaty.
    ErrWorkitemTypeNotAllowed = errors.New("workitem type not allowed by treaty")
    
    // ErrBundleTooLarge indicates the bundle exceeds Treaty size limit.
    ErrBundleTooLarge = errors.New("bundle exceeds size limit")
)

// Checking specific errors
if errors.Is(err, ErrTreatyViolation) {
    // Handle unauthorized sender
}
```

---

## 8. Behavior Notes

| Scenario | Behavior |
|----------|----------|
| Valid bundle, trusted sender | Workitem created, artefacts stored and stamped |
| Invalid bundle format | `ErrBundleCorrupt`, nothing created |
| Hash mismatch | `ErrHashMismatch`, nothing created |
| Untrusted CA | `ErrTreatyViolation`, nothing created |
| Duplicate import | `ErrAlreadyImported`, nothing created |
| Artefact store fails | Transaction rolled back, nothing created |

---

## 9. Complete Example: Ingress Node

```go
package main

import (
    "context"
    "errors"
    "io"
    "net/http"
    
    foundry "github.com/gideas/foundry-sdk-go"
)

type VendorIngressNode struct {
    foundry.AbstractNode
}

// HandleExternalHTTP is called by the HTTP server (external traffic)
func (n *VendorIngressNode) HandleExternalHTTP(w http.ResponseWriter, r *http.Request) {
    ctx := r.Context()
    
    // Read bundle
    bundle, err := io.ReadAll(io.LimitReader(r.Body, 50*1024*1024)) // 50MB limit
    if err != nil {
        http.Error(w, "failed to read bundle", http.StatusBadRequest)
        return
    }
    
    // Optionally inspect before importing
    manifest, err := n.InspectBundle(bundle)
    if err != nil {
        http.Error(w, "invalid bundle format", http.StatusBadRequest)
        return
    }
    
    // Log the incoming bundle
    n.EmitTelemetry(ctx, "import_received", map[string]any{
        "source_flow":     manifest.SourceFlow,
        "source_workitem": manifest.SourceWorkitemId,
        "bundle_size":     len(bundle),
    })
    
    // Import with options
    workitemID, err := n.ImportWithOptions(ctx, bundle, foundry.ImportOptions{
        IntentPrefix: "[Vendor] ",
        AdditionalContext: map[string]string{
            "ingress_method": "http",
            "client_ip":      r.RemoteAddr,
        },
    })
    
    if err != nil {
        status := http.StatusInternalServerError
        switch {
        case errors.Is(err, foundry.ErrTreatyViolation):
            status = http.StatusForbidden
        case errors.Is(err, foundry.ErrAlreadyImported):
            status = http.StatusConflict
        case errors.Is(err, foundry.ErrBundleCorrupt):
            status = http.StatusBadRequest
        }
        http.Error(w, err.Error(), status)
        return
    }
    
    // Success
    w.Header().Set("Content-Type", "application/json")
    w.Write([]byte(`{"workitem_id":"` + workitemID + `"}`))
}

func main() {
    node := &VendorIngressNode{}
    
    // Start HTTP server for external traffic
    go func() {
        http.HandleFunc("/import", node.HandleExternalHTTP)
        http.ListenAndServe(":8080", nil)
    }()
    
    // Run the Foundry node (handles internal workitem assignment)
    foundry.Run(node)
}
```
