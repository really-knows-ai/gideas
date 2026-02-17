# Foundry Node: Examples

## 5.1 Go Example: Simple Routing

```go
package main

import (
    "context"
    foundry "github.com/gideas/foundry-sdk-go"
)

type UserCheckNode struct {
    foundry.Node
    db *sql.DB
}

func (n *UserCheckNode) Assigned(ctx context.Context, item foundry.Workitem) foundry.Result {
    userID := item.Spec.Labels["user_id"]
    
    var isActive bool
    err := n.db.QueryRowContext(ctx, "SELECT is_active FROM users WHERE id = ?", userID).Scan(&isActive)
    if err != nil {
        n.RecordFinding(ctx, "User not found: "+userID, "user-lookup")
        return foundry.RouteToOutput("not-found")
    }
    
    if isActive {
        n.Cite(ctx, "f-101")
        return foundry.RouteToOutput("active")
    }
    
    return foundry.RouteToOutput("inactive")
}
```

**Node Configuration:**
```yaml
kind: FoundryNode
metadata:
  name: user-check-node
spec:
  roles: ["user-validator"]
  outputs:
    - name: "active"
      target: "process-node"
    - name: "inactive"
      target: "notify-node"
    - name: "not-found"
      target: "error-handler"
```

---

## 5.2 Go Example: Forge Node (Context Seeding)

```go
type ForgePythonNode struct {
    foundry.Node
    llm LLMClient
}

func (n *ForgePythonNode) Assigned(ctx context.Context, item foundry.Workitem) foundry.Result {
    // Seed context with applicable laws
    laws, err := n.SearchLibrary(ctx, foundry.LibraryQuery{
        Labels: map[string]string{
            "applies-to.artefact/python-source": "true",
        },
    })
    if err != nil {
        return foundry.RouteToOutput("error")
    }
    
    // Build constrained prompt
    systemPrompt := "Adhere to these laws:\n"
    for _, law := range laws {
        systemPrompt += "- " + law.Spec.Statement + "\n"
    }
    
    // Fetch the input artefact
    input, err := n.FetchArtefact(ctx, "input")
    if err != nil {
        return foundry.RouteToOutput("error")
    }
    
    // Generate code
    code, err := n.llm.Generate(ctx, systemPrompt, string(input))
    if err != nil {
        return foundry.RouteToOutput("error")
    }
    
    // Store artefact (creates new version)
    _, err = n.StoreArtefact(ctx, foundry.StoreArtefactRequest{
        Kind:    "python-source",
        Name:    "source.py",
        Content: []byte(code),
    })
    if err != nil {
        return foundry.RouteToOutput("error")
    }
    
    // Stamp artefact (node's role added to latest version's passport)
    err = n.Stamp(ctx, "python-source", "source.py", "Initial generation")
    if err != nil {
        return foundry.RouteToOutput("error")
    }
    
    // Cite all laws used
    for _, law := range laws {
        n.Cite(ctx, law.Metadata.Name)
    }
    
    return foundry.RouteToOutput("default")
}
```

---

## 5.3 Go Example: Sort Node (Smart Gate)

The Sort Node implements the "Smart Gate" pattern - checking feedback state and depth to decide routing.

```go
type SortNode struct {
    foundry.Node
    maxFeedbackDepth int  // e.g., 3
}

func (n *SortNode) Assigned(ctx context.Context, item foundry.Workitem) foundry.Result {
    // Check for semantic fatigue (feedback history depth exceeded)
    // Note: guestbook is NOT exposed to nodes - thrash detection is Sidecar's job
    for _, fb := range filterFeedback(item.Status.Feedback, "pending") {
        if len(fb.History) > n.maxFeedbackDepth {
            // Too many back-and-forth rejections - escalate to judiciary
            n.UpdateFeedbackState(ctx, fb.ID, "disputed")
            return foundry.RouteToOutput("deadlock")
        }
    }
    
    // Check if all feedback is resolved
    pending := filterFeedback(item.Status.Feedback, "pending")
    if len(pending) > 0 {
        return foundry.RouteToOutput("refine")
    }
    
    // All clear - proceed to next stage
    return foundry.RouteToOutput("done")
}
```

**Node Configuration:**
```yaml
kind: FoundryNode
metadata:
  name: sort-node
spec:
  roles: ["gate"]
  env:
    - name: MAX_FEEDBACK_DEPTH
      value: "3"
  outputs:
    - name: "done"
      target: "next-stage-node"
    - name: "refine"
      targetRole: "refiner"
    - name: "deadlock"
      target: "assay-node"
```

---

## 5.4 Go Example: Terminal Node

```go
type ApprovalTerminal struct {
    foundry.Node
}

func (n *ApprovalTerminal) Assigned(ctx context.Context, item foundry.Workitem) foundry.Result {
    // Final stamp on all artefacts before completion
    for _, artefact := range item.Status.Artefacts {
        n.Stamp(ctx, artefact.Kind, artefact.Name, "Final approval")
    }
    
    // Signal completion - Terminal Guard will validate contract
    return foundry.Complete()
}
```

**Node Configuration:**
```yaml
kind: FoundryNode
metadata:
  name: terminal-approved
spec:
  roles: ["final-approver"]
  isTerminal: true
  terminalContract: "approved"
  outputs: []  # Terminal nodes have no outputs
```

---

## 5.5 Go Example: Sustain Node (Telemetry Monitoring)

```go
type CostMonitorNode struct {
    foundry.Node
    threshold float64
}

func (n *CostMonitorNode) OnTelemetry(ctx context.Context, event foundry.TelemetryEvent) error {
    if event.Type != "foundry.cost.llm" {
        return nil
    }
    
    var payload struct {
        CostUSD float64 `json:"cost_usd"`
    }
    if err := json.Unmarshal([]byte(event.PayloadJSON), &payload); err != nil {
        return err
    }
    
    if payload.CostUSD > n.threshold {
        _, err := n.CreateWorkitem(ctx, foundry.WorkitemSpec{
            Type:   "incident-investigation-v1",
            Intent: fmt.Sprintf("Investigate excessive LLM costs ($%.2f) for %s", 
                payload.CostUSD, event.Context.WorkitemID),
        })
        return err
    }
    
    return nil
}
```

**Node Configuration:**
```yaml
kind: FoundryNode
metadata:
  name: cost-monitor
spec:
  roles: ["observer"]
  capabilities:
    - "LISTEN:telemetry"
  subscriptions:
    - "foundry.cost.*"
```

---

## 5.6 FoundryAgent Example (LLM with Auto-Heartbeat)

```go
type CodeReviewAgent struct {
    foundry.FoundryAgent[CodeReviewInput, CodeReviewOutput]
}

type CodeReviewInput struct {
    Code     string   `json:"code"`
    Language string   `json:"language"`
    Laws     []string `json:"laws"`
}

type CodeReviewOutput struct {
    Issues   []Issue `json:"issues"`
    Approved bool    `json:"approved"`
}

func (a *CodeReviewAgent) Infer(ctx context.Context, input CodeReviewInput) (CodeReviewOutput, error) {
    // FoundryAgent automatically pulses heartbeat during this call
    // No manual heartbeat management needed
    return a.llm.StructuredGenerate(ctx, reviewPrompt, input)
}
```
