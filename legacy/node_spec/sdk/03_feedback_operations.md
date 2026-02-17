# SDK Reference: Feedback Operations

> **Version:** 1.0.0  
> **Status:** Implementation Contract

This document defines the feedback operations available in the Foundry Node SDK.

---

## 1. AddFeedback

Creates a new feedback item on an artefact.

### Signature

```go
func (n *NodeContext) AddFeedback(
    ctx context.Context,
    target string,    // Artefact kind being critiqued
    severity string,  // "LOW" | "MEDIUM" | "HIGH" | "CRITICAL"
    message string,   // Feedback content
) (string, error)
```

### Parameters

| Name | Type | Required | Constraints | Description |
|------|------|----------|-------------|-------------|
| `target` | `string` | Yes | Must match artefact kind | Artefact being reviewed |
| `severity` | `string` | Yes | One of: LOW, MEDIUM, HIGH, CRITICAL | Issue severity |
| `message` | `string` | Yes | Max 1024 chars | Feedback content |

### Returns

| Type | Description |
|------|-------------|
| `string` | Feedback ID (e.g., `"fb-101"`) |

### Behavior

1. **Initial State:** Creates feedback with `state: "pending"`.
2. **History Init:** Adds initial `FeedbackEvent{action: "opened", ...}`.
3. **Artefact Link:** Feedback is scoped to specific artefact kind.

### Errors

| gRPC Code | Foundry Reason | Condition | Recovery |
|-----------|----------------|-----------|----------|
| `INVALID_ARGUMENT` | `MESSAGE_TOO_LONG` | Message exceeds 1024 chars | Use Store & Link pattern |
| `NOT_FOUND` | `ARTEFACT_NOT_FOUND` | Target artefact not in workitem | Store artefact first |
| `INVALID_ARGUMENT` | `INVALID_SEVERITY` | Unknown severity value | Use valid severity |

### Example

```go
feedbackID, err := node.AddFeedback(ctx,
    "petition-draft",
    "HIGH",
    "Input validation missing for user-provided paths",
)
if err != nil {
    return foundry.RouteToOutput("error")
}
```

### Pattern: Store & Link (Large Feedback)

For feedback longer than 1024 chars, store detailed analysis as an artefact and reference it:

```go
// Store detailed analysis
analysis := []byte(`# Security Analysis
## Issue: SQL Injection Vulnerability
... detailed 5000 char analysis ...`)

_, err := node.StoreArtefact(ctx, "analysis-report", "security_analysis.md", analysis)
if err != nil {
    return foundry.RouteToOutput("error")
}

// Create short feedback linking to analysis
feedbackID, err := node.AddFeedback(ctx,
    "petition-draft",
    "CRITICAL",
    "SQL injection vulnerability detected. See analysis-report/security_analysis.md for details.",
)
```

---

## 2. ResolveFeedback

Marks a feedback item as addressed by the refiner.

### Signature

```go
func (n *NodeContext) ResolveFeedback(
    ctx context.Context,
    feedbackID string,
    message string,
) error
```

### Parameters

| Name | Type | Required | Description |
|------|------|----------|-------------|
| `feedbackID` | `string` | Yes | Target feedback ID |
| `message` | `string` | Yes | Resolution message (max 1024 chars) |

### Behavior

1. **State Transition:** Sets `state: "actioned"`.
2. **History Append:** Adds `FeedbackEvent{action: "fixed", message: message}`.
3. **Rejected → Resolved:** If the feedback is in `rejected` state due to an Assay verdict (`linkedRuling` set), calling `ResolveFeedback` records compliance and transitions to `resolved`.

### Errors

| gRPC Code | Foundry Reason | Condition | Recovery |
|-----------|----------------|-----------|----------|
| `NOT_FOUND` | `FEEDBACK_NOT_FOUND` | Feedback ID not in workitem | Check `workitem.Feedback` |
| `INVALID_ARGUMENT` | `MESSAGE_TOO_LONG` | Message exceeds 1024 chars | Shorten message |

### Example

```go
err := node.ResolveFeedback(ctx, "fb-101", "Added input validation in lines 45-52")
if err != nil {
    return foundry.RouteToOutput("error")
}
```

---

## 3. RejectFix

Marks a fix attempt as insufficient (appraiser role).

### Signature

```go
func (n *NodeContext) RejectFix(
    ctx context.Context,
    feedbackID string,
    message string,
) error
```

### Parameters

| Name | Type | Required | Description |
|------|------|----------|-------------|
| `feedbackID` | `string` | Yes | Target feedback ID |
| `message` | `string` | Yes | Rejection reason (max 1024 chars) |

### Behavior

1. **State Transition:** Resets `state: "pending"`.
2. **History Append:** Adds `FeedbackEvent{action: "rejected", message: message}`.
3. **Fatigue Signal:** Increments history depth (triggers escalation if > `maxFeedbackDepth`).

### Errors

| gRPC Code | Foundry Reason | Condition | Recovery |
|-----------|----------------|-----------|----------|
| `NOT_FOUND` | `FEEDBACK_NOT_FOUND` | Feedback ID not found | Check workitem feedback |
| `FAILED_PRECONDITION` | `INVALID_STATE` | Feedback not in `actioned` state | Only reject actioned feedback |

### Example

```go
// Appraiser finds the fix insufficient
err := node.RejectFix(ctx, "fb-101", 
    "Validation added but does not cover directory traversal attacks. See OWASP guidelines.",
)
if err != nil {
    return foundry.RouteToOutput("error")
}
```

---

## 4. UpdateFeedbackState

Updates feedback state with optional justification (for wont-fix).

### Signature

```go
func (n *NodeContext) UpdateFeedbackState(
    ctx context.Context,
    feedbackID string,
    state string,
    justification *Justification,
) error
```

### Parameters

```go
type Justification struct {
    Type        string   // "citation" | "novel_argument"
    CitationIDs []string // If type="citation"
    Argument    string   // If type="novel_argument" (natural language string)
}
```

| Name | Type | Required | Description |
|------|------|----------|-------------|
| `feedbackID` | `string` | Yes | Target feedback ID |
| `state` | `string` | Yes | New state: `"pending"`, `"actioned"`, `"wont-fix"`, `"disputed"` |
| `justification` | `*Justification` | Required for `wont-fix` | Legal basis for refusal |

### Errors

| gRPC Code | Foundry Reason | Condition | Recovery |
|-----------|----------------|-----------|----------|
| `PERMISSION_DENIED` | `CONTEMPT_VIOLATION` | Attempting wont-fix on `rejected` item with `linkedRuling` | Comply with judicial mandate |
| `INVALID_ARGUMENT` | `JUSTIFICATION_REQUIRED` | `wont-fix` without justification | Provide citation or novel_argument |
| `NOT_FOUND` | `FEEDBACK_NOT_FOUND` | Feedback ID not found | Check workitem feedback |

### Contempt Guard

The Sidecar enforces judicial finality. Once an Assay Node renders a verdict with a `linkedRuling`, that feedback item is under a **Judicial Mandate**:

```go
// This will fail with CONTEMPT_VIOLATION
err := node.UpdateFeedbackState(ctx, "fb-101", "wont-fix", &foundry.Justification{
    Type:     "novel_argument",
    Argument: "We disagree with the ruling",
})
// Error: "Cannot refuse a Judicial Mandate. Feedback fb-101 is subject to Ruling r-205"
```

### Example: Wont-Fix with Citation

```go
// Refuse feedback citing existing law
err := node.UpdateFeedbackState(ctx, "fb-101", "wont-fix", &foundry.Justification{
    Type:        "citation",
    CitationIDs: []string{"s-001-camelcase-standard"},
})
if err != nil {
    if foundry.IsError(err, foundry.CONTEMPT_VIOLATION) {
        log.Error("Cannot refuse - under judicial mandate")
        // Must comply with the ruling
        return fixTheIssue(ctx, node, "fb-101")
    }
    return foundry.RouteToOutput("error")
}
```

### Example: Wont-Fix with Novel Argument

```go
// Refuse with novel argument (may trigger escalation to Assay)
err := node.UpdateFeedbackState(ctx, "fb-102", "wont-fix", &foundry.Justification{
    Type:     "novel_argument",
    Argument: "The suggested pattern would break backwards compatibility with v1 API clients",
})
```

### Example: Mark as Disputed

```go
// Escalate to judicial review
err := node.UpdateFeedbackState(ctx, "fb-103", "disputed", nil)
// Workitem will be routed to Assay Node for adjudication
```

---

## Feedback State Machine

```
                    ┌─────────────┐
                    │   opened    │
                    └──────┬──────┘
                           │
                           ▼
          ┌────────────────────────────────┐
          │            pending             │◄────────────────┐
          └────────────────┬───────────────┘                 │
                           │                                 │
           ┌───────────────┼───────────────┐                 │
           │               │               │                 │
           ▼               ▼               ▼                 │
    ┌──────────┐    ┌──────────┐    ┌──────────┐            │
    │ actioned │    │ wont-fix │    │ disputed │            │
    └────┬─────┘    └──────────┘    └────┬─────┘            │
         │                               │                   │
         │ RejectFix()                   │ Assay verdict     │
         └───────────────────────────────┼───────────────────┘
                                         │
                                         ▼
                                  ┌──────────┐
                                  │ rejected │ (with linkedRuling)
                                  └──────────┘
                                         │
                                        │ Must comply (ResolveFeedback)
                                         ▼
                                  ┌──────────┐
                                  │ resolved │
                                  └──────────┘
```

---

## Severity Reference

| Severity | Description | Typical Response |
|----------|-------------|------------------|
| `LOW` | Minor style or preference issue | May be ignored or deferred |
| `MEDIUM` | Code quality issue | Should be addressed |
| `HIGH` | Functional or security concern | Must be addressed |
| `CRITICAL` | Blocking issue, potential data loss | Immediate attention required |
