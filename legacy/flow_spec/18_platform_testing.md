# Foundry Flow: Platform Testing Strategy

**Status:** v1 Specification

## 1. Overview

This document defines the testing strategy for **Foundry Flow Core Services** — the control plane, system services, and sidecar. Testing ensures the platform is reliable, correct, and safe to upgrade.

**Scope:**

| Component | Included |
|-----------|----------|
| Flow Operator | ✓ |
| Librarian | ✓ |
| Law Search | ✓ |
| Archivist | ✓ |
| Flow Monitor | ✓ |
| Backup Service | ✓ |
| Sidecar | ✓ |
| Node SDKs | Separate project |
| Standard Nodes | Separate project |

---

## 2. Testing Philosophy

### 2.1 Principles

1. **Tests are Documentation.** A well-written test explains expected behavior better than comments.

2. **Fast Feedback.** Unit tests run in milliseconds. Integration tests run in seconds. E2E tests run in minutes. Optimize for developer iteration speed.

3. **Test Behavior, Not Implementation.** Tests verify what the system does, not how it does it. Refactoring should not break tests.

4. **Deterministic by Default.** Flaky tests are bugs. Time-dependent tests use fake clocks. Network-dependent tests use mocks or test servers.

5. **Coverage is a Signal, Not a Target.** High coverage with poor assertions is worse than moderate coverage with meaningful tests.

### 2.2 Test Categories

| Category | Scope | Speed | Isolation | When Run |
|----------|-------|-------|-----------|----------|
| **Unit** | Single function/type | < 10ms | Full (no I/O) | Every save |
| **Integration** | Component boundaries | < 5s | Partial (test DBs, mock services) | Every commit |
| **E2E** | Full system | < 5min | None (real cluster) | PR merge, nightly |
| **Contract** | API compatibility | < 30s | Full (proto validation) | Every commit |

---

## 3. Unit Testing

### 3.1 Conventions

Foundry Flow follows standard Go testing conventions:

- Test files: `*_test.go` in the same package
- Table-driven tests for multiple cases
- `testify/assert` for assertions (optional, standard library is acceptable)
- `t.Parallel()` for independent tests

### 3.2 Structure

```go
func TestLibrarian_ResolveLaw(t *testing.T) {
    tests := []struct {
        name     string
        lawID    string
        want     *Law
        wantErr  error
    }{
        {
            name:  "existing law returns correctly",
            lawID: "f-101",
            want:  &Law{ID: "f-101", Tier: 3},
        },
        {
            name:    "missing law returns NOT_FOUND",
            lawID:   "nonexistent",
            wantErr: ErrLawNotFound,
        },
    }
    
    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            t.Parallel()
            lib := newTestLibrarian(t)
            
            got, err := lib.ResolveLaw(context.Background(), tt.lawID)
            
            if tt.wantErr != nil {
                assert.ErrorIs(t, err, tt.wantErr)
                return
            }
            assert.NoError(t, err)
            assert.Equal(t, tt.want, got)
        })
    }
}
```

### 3.3 Mocking

Use interfaces for dependencies. Mocks are generated or hand-written as needed.

```go
// Production code
type LawStore interface {
    Get(ctx context.Context, id string) (*Law, error)
}

// Test code
type mockLawStore struct {
    laws map[string]*Law
}

func (m *mockLawStore) Get(ctx context.Context, id string) (*Law, error) {
    if law, ok := m.laws[id]; ok {
        return law, nil
    }
    return nil, ErrLawNotFound
}
```

### 3.4 Time and Randomness

Tests that depend on time use a fake clock:

```go
func TestTimeout_Expires(t *testing.T) {
    clock := clockwork.NewFakeClock()
    guard := NewTimeoutGuard(clock, 30*time.Second)
    
    guard.Start()
    clock.Advance(31 * time.Second)
    
    assert.True(t, guard.IsExpired())
}
```

---

## 4. Integration Testing

### 4.1 Scope

Integration tests verify component boundaries:

- Operator ↔ Kubernetes API (using envtest)
- Librarian ↔ sqlite-vec database
- Sidecar ↔ gRPC services
- Flow Monitor ↔ Prometheus exposition

### 4.2 Test Databases

Each integration test gets an isolated database:

```go
func TestLibrarian_Integration(t *testing.T) {
    // Creates a temporary sqlite-vec database
    db := testutil.NewTestDB(t)
    lib := NewLibrarian(db)
    
    // Test operations...
    
    // Database is automatically cleaned up when test ends
}
```

### 4.3 Kubernetes Integration (envtest)

Operator tests use controller-runtime's `envtest` package:

```go
func TestOperator_WorkitemReconciliation(t *testing.T) {
    env := &envtest.Environment{
        CRDDirectoryPaths: []string{"../crds"},
    }
    cfg, err := env.Start()
    require.NoError(t, err)
    defer env.Stop()
    
    mgr, err := ctrl.NewManager(cfg, ctrl.Options{})
    require.NoError(t, err)
    
    // Register reconciler, create test workitems, verify behavior...
}
```

### 4.4 gRPC Testing

Service-to-service gRPC is tested with in-process servers:

```go
func TestSidecar_ArtefactUpload(t *testing.T) {
    // Start test Archivist server
    archivistServer := testutil.NewTestArchivist(t)
    
    // Create sidecar pointing to test server
    sidecar := NewSidecar(Config{
        ArchivistAddr: archivistServer.Addr(),
    })
    
    // Test upload flow...
}
```

---

## 5. End-to-End Testing

### 5.1 Environment

E2E tests run against a real Kubernetes cluster. The standard environment is:

- **Kind** (Kubernetes in Docker) for local and CI
- **Helm** to install Foundry Flow
- **Dedicated namespace** per test run

### 5.2 Setup

```bash
# Create Kind cluster
kind create cluster --name foundry-e2e

# Install Foundry Flow
helm install foundry-flow ./helm/foundry-flow \
  --namespace e2e-test \
  --create-namespace \
  --wait

# Run E2E tests
go test ./e2e/... -tags=e2e -v
```

### 5.3 Test Structure

E2E tests are tagged and run separately:

```go
//go:build e2e

package e2e

func TestWorkitem_FullLifecycle(t *testing.T) {
    ctx := context.Background()
    client := newE2EClient(t)
    
    // Create a workitem
    wi, err := client.CreateWorkitem(ctx, &Workitem{
        Spec: WorkitemSpec{
            Type: "test-type",
        },
    })
    require.NoError(t, err)
    
    // Wait for completion
    err = client.WaitForPhase(ctx, wi.Name, "completed", 2*time.Minute)
    require.NoError(t, err)
    
    // Verify artefacts were created
    artefacts, err := client.ListArtefacts(ctx, wi.Name)
    require.NoError(t, err)
    assert.Len(t, artefacts, 1)
}
```

### 5.4 Test Fixtures

E2E tests use a minimal Flow configuration with test nodes:

```yaml
# e2e/fixtures/test-flow.yaml
apiVersion: foundry.io/v1
kind: FoundryFlow
metadata:
  name: e2e-test-flow
spec:
  namespace: e2e-test
---
apiVersion: foundry.io/v1
kind: FoundryNode
metadata:
  name: echo-node
spec:
  roles: ["processor"]
  image: foundry/test-echo-node:latest
  outputs:
    - name: "done"
      target: "$terminal:success"
```

### 5.5 Cleanup

Each test run creates resources in an isolated namespace. Cleanup happens automatically:

```go
func TestMain(m *testing.M) {
    namespace := fmt.Sprintf("e2e-%d", time.Now().Unix())
    
    // Setup
    createNamespace(namespace)
    installFoundryFlow(namespace)
    
    // Run tests
    code := m.Run()
    
    // Teardown
    deleteNamespace(namespace)
    
    os.Exit(code)
}
```

---

## 6. Contract Testing

### 6.1 Purpose

Contract tests ensure API compatibility between components. They verify:

- Sidecar gRPC API matches the proto definition
- SDK implementations correctly serialize/deserialize messages
- Breaking changes are detected before release

### 6.2 Proto Validation

```go
func TestSidecarProto_BackwardCompatibility(t *testing.T) {
    // Load current proto
    current := loadProtoDescriptor("sidecar.proto")
    
    // Load baseline proto (last released version)
    baseline := loadProtoDescriptor("testdata/sidecar_v1.0.0.proto")
    
    // Check for breaking changes
    breaking := protocompat.Compare(baseline, current)
    
    assert.Empty(t, breaking, "Breaking changes detected: %v", breaking)
}
```

### 6.3 SDK Contract Tests

Each SDK must pass the same contract test suite:

```go
// contract/suite.go - shared across all SDKs

type SDKContract interface {
    CreateWorkitem(ctx context.Context, spec WorkitemSpec) error
    StampArtefact(ctx context.Context, artefactID, lawID string) error
    ReportFriction(ctx context.Context, value float64, op FrictionOp) error
}

func RunContractSuite(t *testing.T, sdk SDKContract) {
    t.Run("CreateWorkitem", func(t *testing.T) {
        // Test workitem creation contract...
    })
    
    t.Run("StampArtefact", func(t *testing.T) {
        // Test stamping contract...
    })
    
    // ... more contract tests
}
```

---

## 7. CI/CD Pipeline

### 7.1 Pipeline Stages

| Stage | Trigger | Tests Run | Duration |
|-------|---------|-----------|----------|
| **Lint** | Every push | `golangci-lint` | < 1min |
| **Unit** | Every push | `go test ./...` | < 2min |
| **Integration** | Every push | `go test -tags=integration ./...` | < 5min |
| **Contract** | Every push | `go test -tags=contract ./...` | < 1min |
| **E2E** | PR merge to main | `go test -tags=e2e ./e2e/...` | < 10min |
| **Nightly** | Scheduled (daily) | Full suite + extended E2E | < 30min |

### 7.2 GitHub Actions Example

```yaml
name: CI

on:
  push:
    branches: [main]
  pull_request:
    branches: [main]

jobs:
  lint:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: golangci/golangci-lint-action@v3

  test:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version: '1.22'
      
      - name: Unit Tests
        run: go test -race -coverprofile=coverage.out ./...
      
      - name: Integration Tests
        run: go test -tags=integration -race ./...
      
      - name: Contract Tests
        run: go test -tags=contract ./...

  e2e:
    runs-on: ubuntu-latest
    if: github.event_name == 'push' && github.ref == 'refs/heads/main'
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version: '1.22'
      
      - name: Create Kind Cluster
        uses: helm/kind-action@v1
      
      - name: Install Foundry Flow
        run: |
          helm install foundry-flow ./helm/foundry-flow \
            --namespace e2e --create-namespace --wait
      
      - name: Run E2E Tests
        run: go test -tags=e2e -v ./e2e/...
```

### 7.3 Coverage Requirements

| Component | Minimum Coverage | Target Coverage |
|-----------|------------------|-----------------|
| Operator | 70% | 85% |
| Librarian | 70% | 85% |
| Sidecar | 70% | 85% |
| System Services | 60% | 75% |

Coverage is tracked but not enforced as a gate. Meaningful tests matter more than coverage percentage.

---

## 8. Local Development

### 8.1 Running Tests

```bash
# All unit tests
go test ./...

# With race detection
go test -race ./...

# Specific package
go test ./pkg/operator/...

# Integration tests (requires test databases)
go test -tags=integration ./...

# Verbose output
go test -v ./...

# Run specific test
go test -run TestLibrarian_ResolveLaw ./pkg/librarian/...
```

### 8.2 Local E2E Environment

```bash
# Create local Kind cluster
make kind-create

# Install Foundry Flow (development mode)
make helm-install-dev

# Run E2E tests locally
make e2e

# Cleanup
make kind-delete
```

### 8.3 Debugging Tests

```bash
# Run with delve debugger
dlv test ./pkg/operator/... -- -test.run TestReconciler

# Generate coverage report
go test -coverprofile=coverage.out ./...
go tool cover -html=coverage.out
```

---

## 9. Test Data Management

### 9.1 Fixtures

Test fixtures live in `testdata/` directories:

```
pkg/
  librarian/
    librarian.go
    librarian_test.go
    testdata/
      laws/
        f-101.yaml
        f-102.yaml
      snapshots/
        valid_snapshot.db
        corrupted_snapshot.db
```

### 9.2 Golden Files

For complex output validation, use golden files:

```go
func TestLibrarian_ExportLaws(t *testing.T) {
    lib := newTestLibrarian(t)
    
    got, err := lib.ExportLaws(context.Background())
    require.NoError(t, err)
    
    golden := filepath.Join("testdata", "golden", "exported_laws.json")
    if *update {
        os.WriteFile(golden, got, 0644)
    }
    
    want, _ := os.ReadFile(golden)
    assert.JSONEq(t, string(want), string(got))
}
```

Run with `-update` flag to regenerate golden files:

```bash
go test -update ./...
```
