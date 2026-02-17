# Node SDK: Testing Requirements

**Status:** v1 Specification

## 1. Overview

Each Node SDK implementation (Go, TypeScript, Python, Java, Rust) must meet the testing requirements defined in this document. These requirements ensure SDK quality and API compatibility across languages.

---

## 2. Test Categories

| Category | Purpose | Required |
|----------|---------|----------|
| **Unit Tests** | Verify SDK internal logic | ✓ |
| **Contract Tests** | Verify proto compatibility | ✓ |
| **Integration Tests** | Verify against real sidecar | ✓ |

---

## 3. Unit Testing

### 3.1 Requirements

Each SDK must have unit tests covering:

- Serialization/deserialization of all message types
- Error handling and error code mapping
- Retry logic and backoff behavior
- Context propagation and cancellation
- All public API methods

### 3.2 Mock Sidecar

Each SDK must provide a **mock sidecar** for unit testing node logic without a live sidecar:

```go
// Go SDK example
func TestMyNode_ProcessWorkitem(t *testing.T) {
    mock := foundry.NewMockSidecar()
    mock.OnStampArtefact(func(req *StampRequest) (*StampResponse, error) {
        return &StampResponse{Success: true}, nil
    })
    
    node := NewMyNode(mock)
    result, err := node.Process(context.Background(), workitem)
    
    assert.NoError(t, err)
    assert.Equal(t, "success", result.Output)
}
```

```typescript
// TypeScript SDK example
describe('MyNode', () => {
  it('processes workitem correctly', async () => {
    const mock = new MockSidecar();
    mock.onStampArtefact(() => ({ success: true }));
    
    const node = new MyNode(mock);
    const result = await node.process(workitem);
    
    expect(result.output).toBe('success');
  });
});
```

### 3.3 Coverage

SDK implementations should target 80% code coverage for core modules.

---

## 4. Contract Testing

### 4.1 Purpose

Contract tests ensure each SDK correctly implements the sidecar gRPC protocol. All SDKs must pass the same contract test suite.

### 4.2 Shared Contract Suite

A language-agnostic contract test suite is defined in the `contracts/` directory. Each SDK must implement a test harness that runs these contracts.

**Contract Categories:**

| Contract | Verifies |
|----------|----------|
| `artefact_store` | StoreArtefact request/response format |
| `artefact_stamp` | StampArtefact with law citations |
| `legal_search` | SearchLibrary query and response parsing |
| `feedback_add` | AddFeedback with all field types |
| `friction_report` | ReportFriction with all operations |
| `workitem_create` | CreateWorkitem spec serialization |
| `routing` | RouteToOutput contract names |

### 4.3 Contract Test Structure

```yaml
# contracts/artefact_stamp.yaml
name: artefact_stamp
description: Verify StampArtefact correctly cites laws

setup:
  artefact_id: "art-123"
  law_id: "f-101"

request:
  method: StampArtefact
  payload:
    artefact_id: "{{ artefact_id }}"
    law_id: "{{ law_id }}"
    role: "reviewer"

expected_response:
  success: true
  stamp:
    artefact_id: "{{ artefact_id }}"
    law_id: "{{ law_id }}"
    node: "test-node"
```

### 4.4 Running Contract Tests

Each SDK provides a contract test runner:

```bash
# Go SDK
go test -tags=contract ./...

# TypeScript SDK
npm run test:contract

# Python SDK
pytest tests/contract/

# Java SDK
./gradlew contractTest

# Rust SDK
cargo test --features contract
```

---

## 5. Integration Testing

### 5.1 Purpose

Integration tests verify the SDK works correctly against a real sidecar. These tests catch issues that unit tests and contract tests miss:

- Network behavior (timeouts, retries, connection handling)
- Streaming responses
- Concurrent request handling

### 5.2 Test Environment

Integration tests run against a sidecar in a test container:

```bash
# Start test sidecar
docker run -d --name test-sidecar \
  -p 35697:35697 \
  foundry/sidecar:latest \
  --mode=test

# Run integration tests
go test -tags=integration ./...

# Cleanup
docker rm -f test-sidecar
```

### 5.3 Test Cases

Required integration test scenarios:

| Scenario | Description |
|----------|-------------|
| **Basic Flow** | Store artefact → Stamp → Route |
| **Concurrent Requests** | Multiple simultaneous API calls |
| **Large Artefact** | Upload artefact > 1MB |
| **Timeout Handling** | Request exceeds deadline |
| **Reconnection** | Sidecar restart during operation |
| **Streaming** | SearchLibrary with many results |

---

## 6. CI Requirements

Each SDK repository must have CI that runs:

| Stage | Trigger | Tests |
|-------|---------|-------|
| **Lint** | Every push | Language-specific linter |
| **Unit** | Every push | All unit tests |
| **Contract** | Every push | Contract test suite |
| **Integration** | PR merge | Integration tests with test sidecar |

### 6.1 CI Badge

Each SDK must display CI status in its README:

```markdown
![CI](https://github.com/foundry/sdk-go/workflows/CI/badge.svg)
```

---

## 7. Release Verification

Before releasing a new SDK version:

1. All unit tests pass
2. All contract tests pass
3. Integration tests pass against the target sidecar version
4. No breaking API changes (or major version bump)

### 7.1 Compatibility Matrix

Each SDK release documents compatibility:

| SDK Version | Sidecar Version | Status |
|-------------|-----------------|--------|
| 1.0.x | 1.0.x | ✓ Supported |
| 1.1.x | 1.0.x | ✓ Backward compatible |
| 1.1.x | 1.1.x | ✓ Supported |
