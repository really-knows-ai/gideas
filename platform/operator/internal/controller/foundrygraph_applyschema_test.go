/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	flowv1gen "github.com/foundry/flow/gen/flow/v1"
	flowv1 "github.com/foundry/flow/operator/api/v1"
)

func TestIsFailedPrecondition(t *testing.T) {
	result := isFailedPrecondition(nil)
	if result {
		t.Error("expected false for nil error")
	}

	if !isFailedPrecondition(status.Error(codes.FailedPrecondition, "open transactions exist")) {
		t.Error("expected true for FAILED_PRECONDITION error")
	}

	// A non-FAILED_PRECONDITION gRPC error must fall through.
	if isFailedPrecondition(status.Error(codes.Internal, "wipe failed")) {
		t.Error("expected false for INTERNAL error")
	}
}

// applySchemaOnExistingDialer returns a mock client via the reconciler's dialer.
func applySchemaOnExistingDialer(wipeGraphFn func(context.Context, *flowv1gen.WipeGraphRequest) (*flowv1gen.WipeGraphResponse, error)) func(ctx context.Context, endpoint string) (CartographerClient, error) {
	return func(ctx context.Context, endpoint string) (CartographerClient, error) {
		return &mockCartographerClient{wipeGraphFn: wipeGraphFn}, nil
	}
}

// TestApplySchemaOnExistingDialFailure covers the dial-failure branch of applySchemaOnExisting
// (item 3): the CartographerDialer returning an error before any RPC (HealthCheck, WipeGraph,
// ApplySchema) must short-circuit with a wrapped error. The dialer returns nil along with the
// error, so no client is created and no RPC can run.
func TestApplySchemaOnExistingDialFailure(t *testing.T) {
	fg := &flowv1.FoundryGraph{ObjectMeta: metav1.ObjectMeta{Name: defaultGraphName, Namespace: testNS}}

	dialErr := errors.New("dial failed: connect refused")
	dialer := func(ctx context.Context, endpoint string) (CartographerClient, error) {
		// Dialing fails before any client exists.
		return nil, dialErr
	}
	r := &FoundryGraphReconciler{CartographerDialer: dialer}

	err := r.applySchemaOnExisting(context.Background(), fg, true)
	if err == nil {
		t.Fatal("expected an error when the dialer fails before any RPC")
	}
	if !strings.Contains(err.Error(), "dial existing cartographer") || !strings.Contains(err.Error(), "connect refused") {
		t.Errorf("expected the dial error to be wrapped with context, got %v", err)
	}
}

func TestApplySchemaOnExistingWipeBlockedSentinel(t *testing.T) {
	fg := &flowv1.FoundryGraph{ObjectMeta: metav1.ObjectMeta{Name: defaultGraphName, Namespace: testNS}}

	// WipeGraph returning FAILED_PRECONDITION (open transactions) must map to the
	// errWipeBlockedByOpenTransactions sentinel — the only condition that warrants
	// the DestructiveChangeBlocked status condition (SPEC R1/R6).
	dialer := applySchemaOnExistingDialer(func(context.Context, *flowv1gen.WipeGraphRequest) (*flowv1gen.WipeGraphResponse, error) {
		return nil, status.Error(codes.FailedPrecondition, "open transactions exist")
	})
	r := &FoundryGraphReconciler{CartographerDialer: dialer}

	err := r.applySchemaOnExisting(context.Background(), fg, true)
	if err == nil {
		t.Fatal("expected error from blocked wipe")
	}
	if !errors.Is(err, errWipeBlockedByOpenTransactions) {
		t.Fatalf("expected errWipeBlockedByOpenTransactions, got %v", err)
	}
}

func TestApplySchemaOnExistingNonBlockedWipeError(t *testing.T) {
	fg := &flowv1.FoundryGraph{ObjectMeta: metav1.ObjectMeta{Name: defaultGraphName, Namespace: testNS}}

	// A WipeGraph INTERNAL failure must NOT map to the open-transactions sentinel.
	dialer := applySchemaOnExistingDialer(func(ctx context.Context, _ *flowv1gen.WipeGraphRequest) (*flowv1gen.WipeGraphResponse, error) {
		return nil, status.Error(codes.Internal, "wipe failed partway")
	})
	r := &FoundryGraphReconciler{CartographerDialer: dialer}

	err := r.applySchemaOnExisting(context.Background(), fg, true)
	if err == nil {
		t.Fatal("expected error from failed wipe")
	}
	if errors.Is(err, errWipeBlockedByOpenTransactions) {
		t.Fatalf("INTERNAL wipe error must not be treated as blocked by open transactions, got %v", err)
	}
}

// TestApplySchemaOnExistingHealthCheckFailure verifies the HealthCheck error branch:
// a HealthCheck failure short-circuits before any WipeGraph (achieving
// HealthCheck→WipeGraph→ApplySchema ordering) and is NOT treated as a blocked sentinel.
func TestApplySchemaOnExistingHealthCheckFailure(t *testing.T) {
	fg := &flowv1.FoundryGraph{ObjectMeta: metav1.ObjectMeta{Name: defaultGraphName, Namespace: testNS}}

	wipeCalled := false
	dialer := func(ctx context.Context, endpoint string) (CartographerClient, error) {
		return &mockCartographerClient{
			healthCheckFn: func(context.Context, *flowv1gen.HealthCheckRequest) (*flowv1gen.HealthCheckResponse, error) {
				return nil, status.Error(codes.Unavailable, "cartographer down")
			},
			wipeGraphFn: func(context.Context, *flowv1gen.WipeGraphRequest) (*flowv1gen.WipeGraphResponse, error) {
				wipeCalled = true
				return &flowv1gen.WipeGraphResponse{}, nil
			},
		}, nil
	}
	r := &FoundryGraphReconciler{CartographerDialer: dialer}

	err := r.applySchemaOnExisting(context.Background(), fg, true)
	if err == nil {
		t.Fatal("expected error from HealthCheck failure")
	}
	if errors.Is(err, errWipeBlockedByOpenTransactions) {
		t.Fatalf("HealthCheck error must not be treated as blocked, got %v", err)
	}
	if wipeCalled {
		t.Error("expected WipeGraph NOT to be called when HealthCheck fails")
	}
}

// TestApplySchemaOnExistingApplySchemaFailure verifies the ApplySchema error branch for a
// non-destructive change: HealthCheck succeeds then ApplySchema fails → error propagated,
// no WipeGraph is called (destructive=false).
func TestApplySchemaOnExistingApplySchemaFailure(t *testing.T) {
	fg := &flowv1.FoundryGraph{ObjectMeta: metav1.ObjectMeta{Name: defaultGraphName, Namespace: testNS}}

	wipeCalled := false
	dialer := func(ctx context.Context, endpoint string) (CartographerClient, error) {
		return &mockCartographerClient{
			applySchemaFn: func(context.Context, *flowv1gen.ApplySchemaRequest) (*flowv1gen.ApplySchemaResponse, error) {
				return nil, status.Error(codes.InvalidArgument, "bad schema")
			},
			wipeGraphFn: func(context.Context, *flowv1gen.WipeGraphRequest) (*flowv1gen.WipeGraphResponse, error) {
				wipeCalled = true
				return &flowv1gen.WipeGraphResponse{}, nil
			},
		}, nil
	}
	r := &FoundryGraphReconciler{CartographerDialer: dialer}

	err := r.applySchemaOnExisting(context.Background(), fg, false)
	if err == nil {
		t.Fatal("expected error from ApplySchema failure")
	}
	if errors.Is(err, errWipeBlockedByOpenTransactions) {
		t.Fatalf("ApplySchema error must not be treated as blocked, got %v", err)
	}
	if wipeCalled {
		t.Error("non-destructive change must not call WipeGraph")
	}
}

// TestApplySchemaOnExistingDestructiveOrdering asserts the SPEC R6 destructive ordering:
// HealthCheck succeeds, then WipeGraph is called, then ApplySchema — verified via call
// interleaving and that WipeGraph precedes ApplySchema. The destructive success path also
// persists the last-applied-spec annotation between WipeGraph and ApplySchema (the
// crash-idempotency fix), so the reconciler is given a Client with the FoundryGraph
// seeded.
func TestApplySchemaOnExistingDestructiveOrdering(t *testing.T) {
	s := scheme.Scheme
	_ = flowv1.AddToScheme(s)
	fg := &flowv1.FoundryGraph{ObjectMeta: metav1.ObjectMeta{Name: defaultGraphName, Namespace: testNS}}
	fakeCli := fake.NewClientBuilder().WithScheme(s).WithObjects(fg).Build()

	var order []string
	dialer := func(ctx context.Context, endpoint string) (CartographerClient, error) {
		return &mockCartographerClient{
			healthCheckFn: func(ctx context.Context, req *flowv1gen.HealthCheckRequest) (*flowv1gen.HealthCheckResponse, error) {
				order = append(order, rpcHealth)
				return &flowv1gen.HealthCheckResponse{}, nil
			},
			wipeGraphFn: func(ctx context.Context, _ *flowv1gen.WipeGraphRequest) (*flowv1gen.WipeGraphResponse, error) {
				order = append(order, rpcWipe)
				return &flowv1gen.WipeGraphResponse{}, nil
			},
			applySchemaFn: func(ctx context.Context, _ *flowv1gen.ApplySchemaRequest) (*flowv1gen.ApplySchemaResponse, error) {
				order = append(order, rpcApply)
				return &flowv1gen.ApplySchemaResponse{}, nil
			},
		}, nil
	}
	r := &FoundryGraphReconciler{CartographerDialer: dialer, Client: fakeCli, Scheme: s}

	if err := r.applySchemaOnExisting(context.Background(), fg, true); err != nil {
		t.Fatalf("applySchemaOnExisting: %v", err)
	}
	want := []string{rpcHealth, rpcWipe, rpcApply}
	if len(order) != len(want) {
		t.Fatalf("expected call order %v, got %v", want, order)
	}
	for i := range want {
		if order[i] != want[i] {
			t.Errorf("expected order[%d]=%s, got %s (full: %v)", i, want[i], order[i], order)
		}
	}

	// The crash-idempotency fix: the annotation must be persisted by the destructive
	// success path (between WipeGraph and ApplySchema), recording the spec the apply
	// pushed — so a crash after the apply cannot re-detect the destructive diff.
	var got flowv1.FoundryGraph
	if err := fakeCli.Get(context.Background(), client.ObjectKeyFromObject(fg), &got); err != nil {
		t.Fatalf("get FoundryGraph: %v", err)
	}
	if _, ok := got.Annotations[lastAppliedSpecAnnotation]; !ok {
		t.Error("expected the last-applied-spec annotation to be persisted on the destructive path")
	}
}

// TestApplySchemaDialFailure covers the step-10 dial-failure branch of applySchema
// (foundrygraph_controller.go:386-389): the CartographerDialer returning an error before
// any RPC (HealthCheck, ApplySchema) must short-circuit with a wrapped error. Unlike the
// schema-diff branch (applySchemaOnExisting), the step-10 dial failure is NOT wrapped with
// errExistingPodUnreachable — the new pod passed readiness, so there is no
// missing-Deployment fall-through; it is an ordinary failure that funnels into the SPEC R6
// transient-error requeue-with-backoff path.
func TestApplySchemaDialFailure(t *testing.T) {
	s := scheme.Scheme
	_ = flowv1.AddToScheme(s)

	fg := &flowv1.FoundryGraph{ObjectMeta: metav1.ObjectMeta{Name: defaultGraphName, Namespace: testNS}}
	// applySchema re-fetches the CR (concurrent-deletion guard) before dialing, so the
	// reconciler needs a client that can satisfy that Get.
	fakeCli := fake.NewClientBuilder().WithScheme(s).WithObjects(fg).Build()

	dialErr := errors.New("dial failed: connect refused")
	r := &FoundryGraphReconciler{
		Client: fakeCli,
		Scheme: s,
		CartographerDialer: func(ctx context.Context, endpoint string) (CartographerClient, error) {
			// Dialing fails before any client exists.
			return nil, dialErr
		},
	}

	err := r.applySchema(context.Background(), fg, &flowv1.FoundryGraphSpec{})
	if err == nil {
		t.Fatal("expected an error when the dialer fails before any RPC")
	}
	if !strings.Contains(err.Error(), "dial cartographer") || !strings.Contains(err.Error(), "connect refused") {
		t.Errorf("expected the dial error to be wrapped with context, got %v", err)
	}
}

// TestApplySchemaHealthCheckFailure covers the step-10 HealthCheck-failure branch of
// applySchema (foundrygraph_controller.go:396-399): a HealthCheck failure after a
// successful dial must short-circuit before ApplySchema and surface a wrapped error (SPEC
// R6 step 6 transient-error requeue path — the error funnels through setFailedCondition to
// the requeue-with-backoff result).
func TestApplySchemaHealthCheckFailure(t *testing.T) {
	s := scheme.Scheme
	_ = flowv1.AddToScheme(s)

	fg := &flowv1.FoundryGraph{ObjectMeta: metav1.ObjectMeta{Name: defaultGraphName, Namespace: testNS}}
	fakeCli := fake.NewClientBuilder().WithScheme(s).WithObjects(fg).Build()

	applyCalled := false
	r := &FoundryGraphReconciler{
		Client: fakeCli,
		Scheme: s,
		CartographerDialer: func(ctx context.Context, endpoint string) (CartographerClient, error) {
			return &mockCartographerClient{
				healthCheckFn: func(context.Context, *flowv1gen.HealthCheckRequest) (*flowv1gen.HealthCheckResponse, error) {
					return nil, status.Error(codes.Unavailable, "cartographer down")
				},
				applySchemaFn: func(context.Context, *flowv1gen.ApplySchemaRequest) (*flowv1gen.ApplySchemaResponse, error) {
					applyCalled = true
					return &flowv1gen.ApplySchemaResponse{}, nil
				},
			}, nil
		},
	}

	err := r.applySchema(context.Background(), fg, &flowv1.FoundryGraphSpec{})
	if err == nil {
		t.Fatal("expected error from HealthCheck failure")
	}
	if !strings.Contains(err.Error(), "health check") || !strings.Contains(err.Error(), "cartographer down") {
		t.Errorf("expected the HealthCheck error to be wrapped with context, got %v", err)
	}
	if applyCalled {
		t.Error("expected ApplySchema NOT to be called when HealthCheck fails")
	}
}

// TestApplySchemaUsesSnapshotSpecNotRefetchedSpec pins the step-10 snapshot contract of
// applySchema (foundrygraph_controller.go): the schema sent to the Cartographer is built
// from the passed reconcile-start snapshot, NOT from the CR re-fetched inside
// applySchema. A spec edited on the apiserver between the reconcile-start snapshot and
// the re-fetch must not be applied while the schema diff and the last-applied-spec
// annotation still reference the start snapshot — otherwise the next reconcile
// re-detects a phantom diff and, for destructive changes, re-runs WipeGraph (wiping
// graph data written since the first apply).
func TestApplySchemaUsesSnapshotSpecNotRefetchedSpec(t *testing.T) {
	s := scheme.Scheme
	_ = flowv1.AddToScheme(s)

	// The spec the reconcile diff was computed against (reconcile-start snapshot).
	snapshot := &flowv1.FoundryGraphSpec{EntityTypes: []flowv1.EntityTypeSpec{{Name: "Widget"}}}
	// The CR's spec at applySchema's re-fetch — a mid-reconcile user edit.
	edited := flowv1.FoundryGraphSpec{EntityTypes: []flowv1.EntityTypeSpec{{Name: "Gadget"}}}

	var pushed *flowv1gen.Schema
	dialer := func(ctx context.Context, endpoint string) (CartographerClient, error) {
		return &mockCartographerClient{
			applySchemaFn: func(ctx context.Context, req *flowv1gen.ApplySchemaRequest) (*flowv1gen.ApplySchemaResponse, error) {
				pushed = req.Schema
				return &flowv1gen.ApplySchemaResponse{}, nil
			},
		}, nil
	}

	// The re-fetch inside applySchema returns a CR whose spec differs from the
	// snapshot (the mid-reconcile edit). applySchema must still apply the snapshot.
	fakeCli := fake.NewClientBuilder().WithScheme(s).WithInterceptorFuncs(interceptor.Funcs{
		Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			fg := obj.(*flowv1.FoundryGraph)
			fg.Name = defaultGraphName
			fg.Namespace = testNS
			fg.Spec = *edited.DeepCopy()
			return nil
		},
	}).Build()
	r := &FoundryGraphReconciler{Client: fakeCli, Scheme: s, CartographerDialer: dialer}
	fg := &flowv1.FoundryGraph{ObjectMeta: metav1.ObjectMeta{Name: defaultGraphName, Namespace: testNS}}

	if err := r.applySchema(context.Background(), fg, snapshot); err != nil {
		t.Fatalf("applySchema: %v", err)
	}
	want := (&FoundryGraphReconciler{}).schemaFromCRD(snapshot)
	if !reflect.DeepEqual(pushed, want) {
		t.Errorf("expected the applied schema to be built from the reconcile-start snapshot, got %+v", pushed)
	}
	if notWant := (&FoundryGraphReconciler{}).schemaFromCRD(&edited); reflect.DeepEqual(pushed, notWant) {
		t.Error("expected applySchema NOT to apply the re-fetched (edited) spec")
	}
}

// TestApplySchemaReFetchNotFoundSurfacesError pins the item's SPEC-R6 fix for the
// applySchema re-fetch: an IsNotFound on the re-fetch must NOT be swallowed. A
// concurrently-deleted CR must not flow through as a zero-valued object whose empty schema
// gets dialed and applied against a live Cartographer. The re-fetch error must surface so
// the schema-apply aborts; the next reconcile's initial Get drops the now-absent request.
func TestApplySchemaReFetchNotFoundSurfacesError(t *testing.T) {
	s := scheme.Scheme
	_ = flowv1.AddToScheme(s)
	_ = corev1.AddToScheme(s)

	// No FoundryGraph object is seeded, so the re-fetch inside applySchema returns NotFound.
	fakeCli := fake.NewClientBuilder().WithScheme(s).Build()
	dialed := false
	r := &FoundryGraphReconciler{
		Client: fakeCli,
		Scheme: s,
		CartographerDialer: func(ctx context.Context, endpoint string) (CartographerClient, error) {
			dialed = true
			return &mockCartographerClient{}, nil
		},
	}

	// The in-memory zero-valued object stands in for the stale object the reconciler holds.
	fg := &flowv1.FoundryGraph{ObjectMeta: metav1.ObjectMeta{Name: defaultGraphName, Namespace: testNS}}
	err := r.applySchema(context.Background(), fg, &fg.Spec)
	if err == nil {
		t.Fatal("expected applySchema to surface the NotFound re-fetch, got nil")
	}
	if dialed {
		t.Fatal("applySchema must not dial a Cartographer when the re-fetch CR is absent")
	}
}
