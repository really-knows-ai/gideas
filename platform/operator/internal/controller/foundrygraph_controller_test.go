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
	"io"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	ctrl "sigs.k8s.io/controller-runtime"

	flowv1gen "github.com/foundry/flow/gen/flow/v1"
	flowv1 "github.com/foundry/flow/operator/api/v1"
)

func TestFoundryGraphReconciler_CartographerServiceName(t *testing.T) {
	r := &FoundryGraphReconciler{}
	fg := &flowv1.FoundryGraph{}
	fg.Name = "flow-graph"
	fg.Namespace = "test-ns"

	name := r.cartographerServiceName(fg)
	if name != "cartographer-flow-graph" {
		t.Errorf("expected cartographer-flow-graph, got %q", name)
	}
}

func TestFoundryGraphReconciler_RegisterProxyRoute(t *testing.T) {
	rt := NewProxyRoutingTable()
	r := &FoundryGraphReconciler{
		CartographerPort:  50051,
		ProxyRoutingTable: rt,
	}

	fg := &flowv1.FoundryGraph{}
	fg.Name = "flow-graph"
	fg.Namespace = "test-ns"

	r.registerProxyRoute(fg)

	endpoint, ok := rt.Lookup("test-ns", "flow-graph")
	if !ok {
		t.Fatal("expected route to be registered")
	}
	expected := "cartographer-flow-graph.test-ns.svc.cluster.local:50051"
	if endpoint != expected {
		t.Errorf("expected %q, got %q", expected, endpoint)
	}
}

func TestFoundryGraphReconciler_TearDown(t *testing.T) {
	s := scheme.Scheme
	_ = flowv1.AddToScheme(s)
	_ = appsv1.AddToScheme(s)
	_ = corev1.AddToScheme(s)
	_ = rbacv1.AddToScheme(s)

	ns := "test-ns"
	fg := &flowv1.FoundryGraph{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "flow-graph",
			Namespace: ns,
		},
	}

	deploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "cartographer-flow-graph",
			Namespace: ns,
		},
	}
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "cartographer-flow-graph",
			Namespace: ns,
		},
	}

	fakeCli := fake.NewClientBuilder().WithScheme(s).WithObjects(deploy, svc).Build()
	rt := NewProxyRoutingTable()
	r := &FoundryGraphReconciler{
		Client:            fakeCli,
		Scheme:            s,
		ProxyRoutingTable: rt,
	}
	r.registerProxyRoute(fg)

	ctx := context.Background()
	if err := r.tearDown(ctx, fg); err != nil {
		t.Fatalf("tearDown returned error: %v", err)
	}

	var d appsv1.Deployment
	if err := fakeCli.Get(ctx, types.NamespacedName{Name: "cartographer-flow-graph", Namespace: ns}, &d); err == nil {
		t.Fatal("expected Deployment to be deleted")
	}

	var srv corev1.Service
	if err := fakeCli.Get(ctx, types.NamespacedName{Name: "cartographer-flow-graph", Namespace: ns}, &srv); err == nil {
		t.Fatal("expected Service to be deleted")
	}

	if _, ok := rt.Lookup("test-ns", "flow-graph"); ok {
		t.Fatal("expected route to be deregistered")
	}
}

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

func TestApplySchemaOnExistingWipeBlockedSentinel(t *testing.T) {
	fg := &flowv1.FoundryGraph{ObjectMeta: metav1.ObjectMeta{Name: "flow-graph", Namespace: "test-ns"}}

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
	fg := &flowv1.FoundryGraph{ObjectMeta: metav1.ObjectMeta{Name: "flow-graph", Namespace: "test-ns"}}

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
	fg := &flowv1.FoundryGraph{ObjectMeta: metav1.ObjectMeta{Name: "flow-graph", Namespace: "test-ns"}}

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
	fg := &flowv1.FoundryGraph{ObjectMeta: metav1.ObjectMeta{Name: "flow-graph", Namespace: "test-ns"}}

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
// interleaving and that WipeGraph precedes ApplySchema.
func TestApplySchemaOnExistingDestructiveOrdering(t *testing.T) {
	fg := &flowv1.FoundryGraph{ObjectMeta: metav1.ObjectMeta{Name: "flow-graph", Namespace: "test-ns"}}

	var order []string
	dialer := func(ctx context.Context, endpoint string) (CartographerClient, error) {
		return &mockCartographerClient{
			healthCheckFn: func(ctx context.Context, req *flowv1gen.HealthCheckRequest) (*flowv1gen.HealthCheckResponse, error) {
				order = append(order, "health")
				return &flowv1gen.HealthCheckResponse{}, nil
			},
			wipeGraphFn: func(ctx context.Context, _ *flowv1gen.WipeGraphRequest) (*flowv1gen.WipeGraphResponse, error) {
				order = append(order, "wipe")
				return &flowv1gen.WipeGraphResponse{}, nil
			},
			applySchemaFn: func(ctx context.Context, _ *flowv1gen.ApplySchemaRequest) (*flowv1gen.ApplySchemaResponse, error) {
				order = append(order, "apply")
				return &flowv1gen.ApplySchemaResponse{}, nil
			},
		}, nil
	}
	r := &FoundryGraphReconciler{CartographerDialer: dialer}

	if err := r.applySchemaOnExisting(context.Background(), fg, true); err != nil {
		t.Fatalf("applySchemaOnExisting: %v", err)
	}
	want := []string{"health", "wipe", "apply"}
	if len(order) != len(want) {
		t.Fatalf("expected call order %v, got %v", want, order)
	}
	for i := range want {
		if order[i] != want[i] {
			t.Errorf("expected order[%d]=%s, got %s (full: %v)", i, want[i], order[i], order)
		}
	}
}

// mockExportGraphClient wraps the existing mockExportClient for the ApplySchemaOnExisting
// success ordering test helper.

func TestReconcileBlockedPathSetsDestructiveChangeBlocked(t *testing.T) {
	s := scheme.Scheme
	_ = flowv1.AddToScheme(s)
	_ = appsv1.AddToScheme(s)
	_ = corev1.AddToScheme(s)
	_ = rbacv1.AddToScheme(s)

	// Current spec has no entity types; the last-applied annotation records an old
	// spec with an entity type that has now been removed → destructive diff.
	fg := &flowv1.FoundryGraph{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "flow-graph",
			Namespace: "test-ns",
			Annotations: map[string]string{
				lastAppliedSpecAnnotation: `{"entityTypes":[{"name":"Widget"}]}`,
			},
		},
	}

	wipeErr := status.Error(codes.FailedPrecondition, "open transactions exist")
	dialer := func(ctx context.Context, endpoint string) (CartographerClient, error) {
		return &mockCartographerClient{wipeGraphFn: func(context.Context, *flowv1gen.WipeGraphRequest) (*flowv1gen.WipeGraphResponse, error) {
			return nil, wipeErr
		}}, nil
	}

	fakeCli := fake.NewClientBuilder().WithScheme(s).WithObjects(fg).WithStatusSubresource(fg).Build()
	r := &FoundryGraphReconciler{
		Client:             fakeCli,
		Scheme:             s,
		CartographerPort:   50051,
		ProxyRoutingTable:  NewProxyRoutingTable(),
		CartographerDialer: dialer,
	}

	ctx := context.Background()
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: "flow-graph", Namespace: "test-ns"}}); err == nil {
		t.Fatal("expected Reconcile to return an error for a blocked destructive change")
	}

	var got flowv1.FoundryGraph
	if err := fakeCli.Get(ctx, types.NamespacedName{Name: "flow-graph", Namespace: "test-ns"}, &got); err != nil {
		t.Fatalf("get FoundryGraph: %v", err)
	}
	cond := meta.FindStatusCondition(got.Status.Conditions, "DestructiveChangeBlocked")
	if cond == nil {
		t.Fatal("expected DestructiveChangeBlocked condition to be set")
	}
	if cond.Status != metav1.ConditionTrue {
		t.Errorf("expected DestructiveChangeBlocked=True, got %v", cond.Status)
	}
	if cond.Reason != "WipeGraphFailed" {
		t.Errorf("expected reason WipeGraphFailed, got %q", cond.Reason)
	}
}

// TestReconcileNonDestructiveFailureSetsReconcileFailed asserts the non-blocked
// Failure-condition path: a transient ApplySchema failure on the existing pod (SPEC R6
// change flow) funnels into setFailedCondition, producing Ready=False with reason
// ReconcileFailed (rather than the blocked condition, which is reserved for the
// WipeGraph open-transaction class).
func TestReconcileNonDestructiveFailureSetsReconcileFailed(t *testing.T) {
	s := scheme.Scheme
	_ = flowv1.AddToScheme(s)
	_ = appsv1.AddToScheme(s)
	_ = corev1.AddToScheme(s)
	_ = rbacv1.AddToScheme(s)

	// Old spec has an entity type; new spec adds a property only → non-destructive diff.
	fg := &flowv1.FoundryGraph{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "flow-graph",
			Namespace: "test-ns",
			Annotations: map[string]string{
				lastAppliedSpecAnnotation: `{"entityTypes":[{"name":"Widget","properties":[{"name":"a"}]}]}`,
			},
		},
		Spec: flowv1.FoundryGraphSpec{
			EntityTypes: []flowv1.EntityTypeSpec{{
				Name: "Widget",
				Properties: []flowv1.PropertySpec{
					{Name: "a"},
					{Name: "b"},
				},
			}},
		},
	}

	// ApplySchema on the existing pod fails with a transient (INTERNAL) error.
	dialer := func(ctx context.Context, endpoint string) (CartographerClient, error) {
		return &mockCartographerClient{
			applySchemaFn: func(context.Context, *flowv1gen.ApplySchemaRequest) (*flowv1gen.ApplySchemaResponse, error) {
				return nil, status.Error(codes.Internal, "transient apply failure")
			},
		}, nil
	}

	fakeCli := fake.NewClientBuilder().WithScheme(s).WithObjects(fg).WithStatusSubresource(fg).Build()
	r := &FoundryGraphReconciler{
		Client:             fakeCli,
		Scheme:             s,
		CartographerPort:   50051,
		ProxyRoutingTable:  NewProxyRoutingTable(),
		CartographerDialer: dialer,
	}

	ctx := context.Background()
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: "flow-graph", Namespace: "test-ns"}}); err == nil {
		t.Fatal("expected Reconcile to return an error for a failed non-destructive change")
	}

	var got flowv1.FoundryGraph
	if err := fakeCli.Get(ctx, types.NamespacedName{Name: "flow-graph", Namespace: "test-ns"}, &got); err != nil {
		t.Fatalf("get FoundryGraph: %v", err)
	}
	ready := meta.FindStatusCondition(got.Status.Conditions, "Ready")
	if ready == nil {
		t.Fatal("expected Ready condition to be set")
	}
	if ready.Status != metav1.ConditionFalse {
		t.Errorf("expected Ready=False, got %v", ready.Status)
	}
	if ready.Reason != "ReconcileFailed" {
		t.Errorf("expected reason ReconcileFailed, got %q", ready.Reason)
	}
}

// TestReconcileDestructiveNonBlockedFailureSetsReconcileFailed drives the destructive,
// non-blocked failure path: WipeGraph returns an ordinary (non-open-transactions) error,
// which must NOT set DestructiveChangeBlocked but must set Ready=False/ReconcileFailed.
func TestReconcileDestructiveNonBlockedFailureSetsReconcileFailed(t *testing.T) {
	s := scheme.Scheme
	_ = flowv1.AddToScheme(s)
	_ = appsv1.AddToScheme(s)
	_ = corev1.AddToScheme(s)
	_ = rbacv1.AddToScheme(s)

	// Destructive diff: old spec has an entity type that new spec removes.
	fg := &flowv1.FoundryGraph{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "flow-graph",
			Namespace: "test-ns",
			Annotations: map[string]string{
				lastAppliedSpecAnnotation: `{"entityTypes":[{"name":"Widget"}]}`,
			},
		},
	}

	// WipeGraph fails with INTERNAL — destructive but NOT blocked.
	dialer := func(ctx context.Context, cfg string) (CartographerClient, error) {
		return &mockCartographerClient{
			wipeGraphFn: func(context.Context, *flowv1gen.WipeGraphRequest) (*flowv1gen.WipeGraphResponse, error) {
				return nil, status.Error(codes.Internal, "wipe failed partway")
			},
		}, nil
	}

	fakeCli := fake.NewClientBuilder().WithScheme(s).WithObjects(fg).WithStatusSubresource(fg).Build()
	r := &FoundryGraphReconciler{
		Client:             fakeCli,
		Scheme:             s,
		CartographerPort:   50051,
		ProxyRoutingTable:  NewProxyRoutingTable(),
		CartographerDialer: dialer,
	}

	ctx := context.Background()
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: "flow-graph", Namespace: "test-ns"}}); err == nil {
		t.Fatal("expected Reconcile to return an error for a non-blocked WipeGraph failure")
	}

	var got flowv1.FoundryGraph
	if err := fakeCli.Get(ctx, types.NamespacedName{Name: "flow-graph", Namespace: "test-ns"}, &got); err != nil {
		t.Fatalf("get FoundryGraph: %v", err)
	}
	if blocked := meta.FindStatusCondition(got.Status.Conditions, "DestructiveChangeBlocked"); blocked != nil {
		t.Errorf("expected no DestructiveChangeBlocked condition for non-blocked error, got %v", blocked)
	}
	ready := meta.FindStatusCondition(got.Status.Conditions, "Ready")
	if ready == nil || ready.Status != metav1.ConditionFalse || ready.Reason != "ReconcileFailed" {
		t.Errorf("expected Ready=False/ReconcileFailed, got %v", ready)
	}
}

// TestReconcileDeletionFinalizerRemoval drives the deletion reconcile branch: a
// FoundryGraph carrying the finalizer and a DeletionTimestamp is torn down, its finalizer
// is removed, and the object is updated — no error is returned.
func TestReconcileDeletionFinalizesRemoval(t *testing.T) {
	s := scheme.Scheme
	_ = flowv1.AddToScheme(s)
	_ = appsv1.AddToScheme(s)
	_ = corev1.AddToScheme(s)
	_ = rbacv1.AddToScheme(s)

	now := metav1.Now()
	fg := &flowv1.FoundryGraph{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "flow-graph",
			Namespace:         "test-ns",
			DeletionTimestamp: &now,
			Finalizers:        []string{finalizerName},
		},
	}
	// A deployment exists so the tear-down actually removes something.
	deploy := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "cartographer-flow-graph", Namespace: "test-ns"}}

	fakeCli := fake.NewClientBuilder().WithScheme(s).WithObjects(fg, deploy).WithStatusSubresource(fg).Build()
	r := &FoundryGraphReconciler{
		Client:            fakeCli,
		Scheme:            s,
		ProxyRoutingTable: NewProxyRoutingTable(),
	}

	ctx := context.Background()
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: "flow-graph", Namespace: "test-ns"}}); err != nil {
		t.Fatalf("Reconcile on deletion returned error: %v", err)
	}

	// Tear-down must have deleted the Deployment.
	var d appsv1.Deployment
	if err := fakeCli.Get(ctx, types.NamespacedName{Name: "cartographer-flow-graph", Namespace: "test-ns"}, &d); err == nil {
		t.Error("expected Deployment to be deleted during tear-down")
	}
}

// TestReconcileInfraFailureSetsReconcileFailed drives the infra-failure branch: a
// reconcileSecrets failure (operator-signing Secret missing) funnels into
// setFailedCondition with Ready=False/ReconcileFailed.
func TestReconcileInfraFailureSetsReconcileFailed(t *testing.T) {
	s := scheme.Scheme
	_ = flowv1.AddToScheme(s)
	_ = appsv1.AddToScheme(s)
	_ = corev1.AddToScheme(s)
	_ = rbacv1.AddToScheme(s)

	fg := &flowv1.FoundryGraph{ObjectMeta: metav1.ObjectMeta{Name: "flow-graph", Namespace: "test-ns"}}
	// Operator-namespace signing Secret is intentionally absent → reconcileSecrets fails.
	fakeCli := fake.NewClientBuilder().WithScheme(s).WithObjects(fg).WithStatusSubresource(fg).Build()
	r := &FoundryGraphReconciler{
		Client:            fakeCli,
		Scheme:            s,
		OperatorNamespace: "operator-system",
		CartographerPort:  50051,
		ProxyRoutingTable: NewProxyRoutingTable(),
		CartographerDialer: func(ctx context.Context, cfg string) (CartographerClient, error) {
			return &mockCartographerClient{}, nil
		},
	}

	ctx := context.Background()
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: "flow-graph", Namespace: "test-ns"}}); err == nil {
		t.Fatal("expected Reconcile to return an error on infra failure")
	}

	var got flowv1.FoundryGraph
	if err := fakeCli.Get(ctx, types.NamespacedName{Name: "flow-graph", Namespace: "test-ns"}, &got); err != nil {
		t.Fatalf("get FoundryGraph: %v", err)
	}
	ready := meta.FindStatusCondition(got.Status.Conditions, "Ready")
	if ready == nil || ready.Status != metav1.ConditionFalse || ready.Reason != "ReconcileFailed" {
		t.Errorf("expected Ready=False/ReconcileFailed, got %v", ready)
	}
}

// fullSuccessDialer always returns a healthy mock client for the post-readiness
// ApplySchema at Reconcile step 10.
func fullSuccessDialer(ctx context.Context, endpoint string) (CartographerClient, error) {
	return &mockCartographerClient{}, nil
}

// TestUpdateStatusPopulatesStorageSize verifies updateStatus publishes the PVC's actual
// capacity as status.storageSize (SPEC R6 step 7: "status.storageSize" reflects the PVC's
// status.capacity.storage). The reconciler's earlier happy-path tests never provision a PVC
// with status.capacity, so the storageSize write path (controller.go) is otherwise untested.
func TestUpdateStatusPopulatesStorageSize(t *testing.T) {
	s := scheme.Scheme
	_ = flowv1.AddToScheme(s)
	_ = corev1.AddToScheme(s)

	ns := "test-ns"
	cap := resource.MustParse("5Gi")
	// Seed a PVC with a bound capacity so updateStatus's storageSize read sees a real
	// allocation diverging from any spec default.
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "data-flow-graph", Namespace: ns},
		Status: corev1.PersistentVolumeClaimStatus{
			Capacity: corev1.ResourceList{corev1.ResourceStorage: cap},
		},
	}
	fg := &flowv1.FoundryGraph{ObjectMeta: metav1.ObjectMeta{Name: "flow-graph", Namespace: ns}}
	fakeCli := fake.NewClientBuilder().WithScheme(s).WithObjects(fg, pvc).WithStatusSubresource(fg).Build()
	r := &FoundryGraphReconciler{Client: fakeCli, Scheme: s, CartographerPort: 50051}

	ctx := context.Background()
	if err := r.updateStatus(ctx, fg, &flowv1.FoundryGraphSpec{}); err != nil {
		t.Fatalf("updateStatus: %v", err)
	}

	var got flowv1.FoundryGraph
	if err := fakeCli.Get(ctx, types.NamespacedName{Name: "flow-graph", Namespace: ns}, &got); err != nil {
		t.Fatalf("get FoundryGraph: %v", err)
	}
	if got.Status.StorageSize == nil {
		t.Fatal("expected status.storageSize to be populated from the PVC's status.capacity.storage")
	}
	if got.Status.StorageSize.Value() != cap.Value() {
		t.Errorf("expected status.storageSize=%v, got %v", cap.Value(), got.Status.StorageSize.Value())
	}
}

// TestReconcileBlockedRecoversToReady covers the blocked→ready transition (item 4) and,
// by driving a fully successful reconcile, the R6 creation lifecycle steps: reconcilePVC,
// reconcileService, waitForReadiness, applySchema (step 10), updateStatus, registerProxyRoute,
// and setReadyCondition (item 1).
func TestReconcileBlockedRecoversToReady(t *testing.T) {
	s := scheme.Scheme
	_ = flowv1.AddToScheme(s)
	_ = appsv1.AddToScheme(s)
	_ = corev1.AddToScheme(s)
	_ = rbacv1.AddToScheme(s)

	ns := "test-ns"
	// Destructive diff: old annotation records a Widget entity type removed from spec.
	fg := &flowv1.FoundryGraph{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "flow-graph",
			Namespace: ns,
			Annotations: map[string]string{
				lastAppliedSpecAnnotation: `{"entityTypes":[{"name":"Widget"}]}`,
			},
		},
	}

	fakeClient := fake.NewClientBuilder().WithScheme(s).WithObjects(fg).WithStatusSubresource(fg).Build()
	ctx := context.Background()
	nn := types.NamespacedName{Name: "flow-graph", Namespace: ns}

	// First reconcile: destructive WipeGraph fails with FAILED_PRECONDITION → blocked.
	blockedR := &FoundryGraphReconciler{
		Client:            fakeClient,
		Scheme:            s,
		ProxyRoutingTable: NewProxyRoutingTable(),
		CartographerDialer: func(ctx context.Context, endpoint string) (CartographerClient, error) {
			return &mockCartographerClient{
				wipeGraphFn: func(context.Context, *flowv1gen.WipeGraphRequest) (*flowv1gen.WipeGraphResponse, error) {
					return nil, status.Error(codes.FailedPrecondition, "open transactions exist")
				},
			}, nil
		},
	}
	if _, err := blockedR.Reconcile(ctx, ctrl.Request{NamespacedName: nn}); err == nil {
		t.Fatal("expected blocked reconcile to return an error")
	}
	var blockedCR flowv1.FoundryGraph
	if err := fakeClient.Get(ctx, nn, &blockedCR); err != nil {
		t.Fatalf("get FoundryGraph after blocked reconcile: %v", err)
	}
	if cond := meta.FindStatusCondition(blockedCR.Status.Conditions, "DestructiveChangeBlocked"); cond == nil || cond.Status != metav1.ConditionTrue {
		t.Fatalf("expected DestructiveChangeBlocked=True after blocked reconcile, got %v", cond)
	}

	// The user retries with a spec that no longer triggers a destructive change: clear the
	// schema-removal so the diff becomes SchemaDiffNone. We model the "recovered" state by
	// writing the current (empty) spec back to the last-applied annotation.
	var updateCR flowv1.FoundryGraph
	if err := fakeClient.Get(ctx, nn, &updateCR); err != nil {
		t.Fatalf("get FoundryGraph: %v", err)
	}
	delete(updateCR.Annotations, lastAppliedSpecAnnotation)
	if err := fakeClient.Update(ctx, &updateCR); err != nil {
		t.Fatalf("update FoundryGraph: %v", err)
	}

	// Pre-provision operator-namespace signing Secrets so reconcileSecrets succeeds.
	osign := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: operatorSigningKeySecretName, Namespace: "operator-ns"}, Data: map[string][]byte{"key": []byte("op"), "private-key": []byte("op-p")}}
	ssign := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: sidecarSigningKeySecretName, Namespace: "operator-ns"}, Data: map[string][]byte{"key": []byte("sd"), "private-key": []byte("sd-p")}}
	if err := fakeClient.Create(ctx, osign); err != nil {
		t.Fatalf("create operator signing Secret: %v", err)
	}
	if err := fakeClient.Create(ctx, ssign); err != nil {
		t.Fatalf("create sidecar signing Secret: %v", err)
	}

	// Pre-provision a ready Deployment so waitForReadiness returns immediately.
	replicas := int32(1)
	readyDeploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "cartographer-flow-graph", Namespace: ns},
		Spec:       appsv1.DeploymentSpec{Replicas: &replicas},
		Status:     appsv1.DeploymentStatus{AvailableReplicas: 1, ReadyReplicas: 1},
	}
	if err := fakeClient.Create(ctx, readyDeploy); err != nil {
		t.Fatalf("create ready Deployment: %v", err)
	}

	// Second reconcile: no schema diff → full success lifecycle → Ready=True.
	successR := &FoundryGraphReconciler{
		Client:             fakeClient,
		Scheme:             s,
		OperatorNamespace:  "operator-ns",
		CartographerPort:   50051,
		CartographerImage:  "cartographer:latest",
		ReadinessTimeout:   time.Second,
		ProxyRoutingTable:  NewProxyRoutingTable(),
		CartographerDialer: fullSuccessDialer,
	}
	if _, err := successR.Reconcile(ctx, ctrl.Request{NamespacedName: nn}); err != nil {
		t.Fatalf("unexpected error on successful reconcile: %v", err)
	}

	var finalCR flowv1.FoundryGraph
	if err := fakeClient.Get(ctx, nn, &finalCR); err != nil {
		t.Fatalf("get FoundryGraph: %v", err)
	}
	if cond := meta.FindStatusCondition(finalCR.Status.Conditions, "DestructiveChangeBlocked"); cond != nil {
		t.Errorf("expected DestructiveChangeBlocked to be cleared on success, got %v", cond)
	}
	ready := meta.FindStatusCondition(finalCR.Status.Conditions, "Ready")
	if ready == nil || ready.Status != metav1.ConditionTrue || ready.Reason != "Reconciled" {
		t.Errorf("expected Ready=True/Reconciled, got %v", ready)
	}
	// updateStatus (step 7) must have written the last-applied-spec annotation.
	if _, ok := finalCR.Annotations[lastAppliedSpecAnnotation]; !ok {
		t.Errorf("expected last-applied-spec annotation written by updateStatus")
	}
	// Step 8: proxy route registered.
	if _, ok := successR.ProxyRoutingTable.Lookup(ns, "flow-graph"); !ok {
		t.Error("expected proxy route to be registered after successful reconcile")
	}
	// Step 4: Service must exist.
	var svc corev1.Service
	if err := fakeClient.Get(ctx, types.NamespacedName{Name: "cartographer-flow-graph", Namespace: ns}, &svc); err != nil {
		t.Errorf("expected Service to exist after successful reconcile: %v", err)
	}
	// Step 3: Deployment must exist.
	var deployment appsv1.Deployment
	if err := fakeClient.Get(ctx, types.NamespacedName{Name: "cartographer-flow-graph", Namespace: ns}, &deployment); err != nil {
		t.Errorf("expected Deployment to exist after successful reconcile: %v", err)
	}
}

// TestReconcileNonDestructiveSuccessReachesReady is the reconcile-level happy-path test
// for a successful non-destructive schema change. It asserts the SPEC R6 ordering
// (HealthCheck → ApplySchema, no WipeGraph) is driven through the Reconcile switch with
// destructive=false, that the change reaches Ready, and that updateStatus persists the
// status block (endpoint/storageSize) via the status subresource.
func TestReconcileNonDestructiveSuccessReachesReady(t *testing.T) {
	s := scheme.Scheme
	_ = flowv1.AddToScheme(s)
	_ = appsv1.AddToScheme(s)
	_ = corev1.AddToScheme(s)
	_ = rbacv1.AddToScheme(s)

	ns := "test-ns"
	// Old spec has Widget with property "a"; new spec adds property "b" → non-destructive
	// diff (additive-only). applySchemaOnExisting must run with destructive=false.
	fg := &flowv1.FoundryGraph{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "flow-graph",
			Namespace: ns,
			Annotations: map[string]string{
				lastAppliedSpecAnnotation: `{"entityTypes":[{"name":"Widget","properties":[{"name":"a"}]}]}`,
			},
		},
		Spec: flowv1.FoundryGraphSpec{
			EntityTypes: []flowv1.EntityTypeSpec{{
				Name: "Widget",
				Properties: []flowv1.PropertySpec{
					{Name: "a"},
					{Name: "b"},
				},
			}},
		},
	}

	// Operator-namespace signing secrets so reconcileSecrets succeeds.
	osign := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: operatorSigningKeySecretName, Namespace: "operator-ns"}, Data: map[string][]byte{"key": []byte("op"), "private-key": []byte("op-p")}}
	ssign := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: sidecarSigningKeySecretName, Namespace: "operator-ns"}, Data: map[string][]byte{"key": []byte("sd"), "private-key": []byte("sd-p")}}
	// A ready Deployment so waitForReadiness returns immediately.
	replicas := int32(1)
	readyDeploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "cartographer-flow-graph", Namespace: ns},
		Spec:       appsv1.DeploymentSpec{Replicas: &replicas},
		Status:     appsv1.DeploymentStatus{AvailableReplicas: 1, ReadyReplicas: 1},
	}

	fakeClient := fake.NewClientBuilder().WithScheme(s).WithObjects(fg, osign, ssign, readyDeploy).WithStatusSubresource(fg).Build()
	ctx := context.Background()
	nn := types.NamespacedName{Name: "flow-graph", Namespace: ns}

	// Record the RPC call order on the mock client; WipeGraph must never be invoked.
	var order []string
	r := &FoundryGraphReconciler{
		Client:            fakeClient,
		Scheme:            s,
		OperatorNamespace: "operator-ns",
		CartographerPort:  50051,
		CartographerImage: "cartographer:latest",
		ReadinessTimeout:  time.Second,
		ProxyRoutingTable: NewProxyRoutingTable(),
		CartographerDialer: func(ctx context.Context, endpoint string) (CartographerClient, error) {
			return &mockCartographerClient{
				healthCheckFn: func(context.Context, *flowv1gen.HealthCheckRequest) (*flowv1gen.HealthCheckResponse, error) {
					order = append(order, "health")
					return &flowv1gen.HealthCheckResponse{}, nil
				},
				applySchemaFn: func(context.Context, *flowv1gen.ApplySchemaRequest) (*flowv1gen.ApplySchemaResponse, error) {
					order = append(order, "apply")
					return &flowv1gen.ApplySchemaResponse{}, nil
				},
				wipeGraphFn: func(context.Context, *flowv1gen.WipeGraphRequest) (*flowv1gen.WipeGraphResponse, error) {
					order = append(order, "wipe")
					return &flowv1gen.WipeGraphResponse{}, nil
				},
			}, nil
		},
	}

	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: nn}); err != nil {
		t.Fatalf("unexpected error on successful non-destructive reconcile: %v", err)
	}

	// SPEC R6 non-destructive ordering: HealthCheck precedes ApplySchema; WipeGraph is
	// never called. ApplySchema runs twice (on the existing pod and again at step 10 on
	// the newly-ready pod), so assert the health→apply prefix and the absence of "wipe".
	if len(order) < 2 || order[0] != "health" || order[1] != "apply" {
		t.Errorf("expected RPC order to start [health, apply, ...], got %v", order)
	}
	for _, call := range order {
		if call == "wipe" {
			t.Fatalf("non-destructive change must never call WipeGraph, got order %v", order)
		}
	}

	var got flowv1.FoundryGraph
	if err := fakeClient.Get(ctx, nn, &got); err != nil {
		t.Fatalf("get FoundryGraph: %v", err)
	}
	ready := meta.FindStatusCondition(got.Status.Conditions, "Ready")
	if ready == nil || ready.Status != metav1.ConditionTrue || ready.Reason != "Reconciled" {
		t.Errorf("expected Ready=True/Reconciled, got %v", ready)
	}
	// updateStatus must persist the status block (endpoint) via the status subresource.
	if got.Status.Endpoint.Host == "" || got.Status.Endpoint.Port != 50051 {
		t.Errorf("expected status.endpoint persisted, got %+v", got.Status.Endpoint)
	}
	// The last-applied-spec annotation must also be written (metadata, main update).
	if _, ok := got.Annotations[lastAppliedSpecAnnotation]; !ok {
		t.Error("expected last-applied-spec annotation written by updateStatus")
	}
}

// TestReconcileStep10ApplySchemaFailure (item 3) drives the final-step ApplySchema failure
// on the newly-ready pod: it funnels into setFailedCondition, producing Ready=False with
// Reason "ReconcileFailed".
func TestReconcileStep10ApplySchemaFailure(t *testing.T) {
	s := scheme.Scheme
	_ = flowv1.AddToScheme(s)
	_ = appsv1.AddToScheme(s)
	_ = corev1.AddToScheme(s)
	_ = rbacv1.AddToScheme(s)

	ns := "test-ns"
	fg := &flowv1.FoundryGraph{ObjectMeta: metav1.ObjectMeta{Name: "flow-graph", Namespace: ns}}

	// Operator-namespace signing secrets so reconcileSecrets succeeds; no schema diff so the
	// reconcile skips applySchemaOnExisting and proceeds to the infra steps.
	replicas := int32(1)
	osign := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: operatorSigningKeySecretName, Namespace: "operator-ns"}, Data: map[string][]byte{"key": []byte("op"), "private-key": []byte("op-p")}}
	ssign := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: sidecarSigningKeySecretName, Namespace: "operator-ns"}, Data: map[string][]byte{"key": []byte("sd"), "private-key": []byte("sd-p")}}
	readyDeploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "cartographer-flow-graph", Namespace: ns},
		Spec:       appsv1.DeploymentSpec{Replicas: &replicas},
		Status:     appsv1.DeploymentStatus{AvailableReplicas: 1, ReadyReplicas: 1},
	}

	fakeClient := fake.NewClientBuilder().WithScheme(s).WithObjects(fg, osign, ssign, readyDeploy).WithStatusSubresource(fg).Build()
	ctx := context.Background()
	nn := types.NamespacedName{Name: "flow-graph", Namespace: ns}

	// The final ApplySchema (step 10) fails with a transient error.
	r := &FoundryGraphReconciler{
		Client:            fakeClient,
		Scheme:            s,
		OperatorNamespace: "operator-ns",
		CartographerPort:  50051,
		CartographerImage: "cartographer:latest",
		ReadinessTimeout:  time.Second,
		ProxyRoutingTable: NewProxyRoutingTable(),
		CartographerDialer: func(ctx context.Context, endpoint string) (CartographerClient, error) {
			return &mockCartographerClient{
				applySchemaFn: func(context.Context, *flowv1gen.ApplySchemaRequest) (*flowv1gen.ApplySchemaResponse, error) {
					return nil, status.Error(codes.Internal, "transient apply failure on new pod")
				},
			}, nil
		},
	}
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: nn}); err == nil {
		t.Fatal("expected Reconcile to return an error when step-10 ApplySchema fails")
	}

	var got flowv1.FoundryGraph
	if err := fakeClient.Get(ctx, nn, &got); err != nil {
		t.Fatalf("get FoundryGraph: %v", err)
	}
	ready := meta.FindStatusCondition(got.Status.Conditions, "Ready")
	if ready == nil || ready.Status != metav1.ConditionFalse || ready.Reason != "ReconcileFailed" {
		t.Errorf("expected Ready=False/ReconcileFailed after step-10 ApplySchema failure, got %v", ready)
	}
}

// TestReconcileBlockedThenFailedClearsBlocked covers item 5: a previously-blocked
// FoundryGraph that is subsequently hit by an unrelated (non-blocked) destructive WriterError
// must not retain DestructiveChangeBlocked — setFailedCondition clears it and sets
// Ready=False/ReconcileFailed. A blocked condition must never persist for a non-open-transaction error.
func TestReconcileBlockedThenFailedClearsBlocked(t *testing.T) {
	s := scheme.Scheme
	_ = flowv1.AddToScheme(s)
	_ = appsv1.AddToScheme(s)
	_ = corev1.AddToScheme(s)
	_ = rbacv1.AddToScheme(s)

	ns := "test-ns"
	// Destructive diff: removed Widget entity drives the WipeGraph path.
	fg := &flowv1.FoundryGraph{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "flow-graph",
			Namespace: ns,
			Annotations: map[string]string{
				lastAppliedSpecAnnotation: `{"entityTypes":[{"name":"Widget"}]}`,
			},
		},
	}
	fakeClient := fake.NewClientBuilder().WithScheme(s).WithObjects(fg).WithStatusSubresource(fg).Build()
	ctx := context.Background()
	nn := types.NamespacedName{Name: "flow-graph", Namespace: ns}

	// First reconcile: blocked (WipeGraph open transactions).
	blockedR := &FoundryGraphReconciler{
		Client:            fakeClient,
		Scheme:            s,
		ProxyRoutingTable: NewProxyRoutingTable(),
		CartographerDialer: func(ctx context.Context, endpoint string) (CartographerClient, error) {
			return &mockCartographerClient{
				wipeGraphFn: func(context.Context, *flowv1gen.WipeGraphRequest) (*flowv1gen.WipeGraphResponse, error) {
					return nil, status.Error(codes.FailedPrecondition, "open transactions exist")
				},
			}, nil
		},
	}
	if _, err := blockedR.Reconcile(ctx, ctrl.Request{NamespacedName: nn}); err == nil {
		t.Fatal("expected blocked reconcile to return an error")
	}

	// Second reconcile: same destructive diff, WipeGraph now fails INTERNAL (non-blocked).
	failedR := &FoundryGraphReconciler{
		Client:            fakeClient,
		Scheme:            s,
		ProxyRoutingTable: NewProxyRoutingTable(),
		CartographerDialer: func(ctx context.Context, endpoint string) (CartographerClient, error) {
			return &mockCartographerClient{
				wipeGraphFn: func(context.Context, *flowv1gen.WipeGraphRequest) (*flowv1gen.WipeGraphResponse, error) {
					return nil, status.Error(codes.Internal, "wipe failed partway")
				},
			}, nil
		},
	}
	if _, err := failedR.Reconcile(ctx, ctrl.Request{NamespacedName: nn}); err == nil {
		t.Fatal("expected failed reconcile to return an error")
	}
	var got flowv1.FoundryGraph
	if err := fakeClient.Get(ctx, nn, &got); err != nil {
		t.Fatalf("get FoundryGraph: %v", err)
	}
	if cond := meta.FindStatusCondition(got.Status.Conditions, "DestructiveChangeBlocked"); cond != nil {
		t.Errorf("expected stale DestructiveChangeBlocked to be cleared on failure, got %v", cond)
	}
	ready := meta.FindStatusCondition(got.Status.Conditions, "Ready")
	if ready == nil || ready.Status != metav1.ConditionFalse || ready.Reason != "ReconcileFailed" {
		t.Errorf("expected Ready=False/ReconcileFailed, got %v", ready)
	}
}

// TestWaitForReadinessTimeout exercises the SPEC R6 readiness-timeout boundary: a
// Deployment that never becomes ready must make waitForReadiness return the timeout error
// (the ctx.Done / time.After loop exits via the deadline). Without ready replicas, the poll
// runs until the ReadinessTimeout is crossed; we assert the error names the timeout, not a
// context cancel.
func TestWaitForReadinessTimeout(t *testing.T) {
	s := scheme.Scheme
	_ = appsv1.AddToScheme(s)
	_ = flowv1.AddToScheme(s)

	// A Deployment whose desired replicas are never ready → the poll cannot succeed.
	replicas := int32(1)
	notReady := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "cartographer-flow-graph", Namespace: "test-ns"},
		Spec:       appsv1.DeploymentSpec{Replicas: &replicas},
		Status:     appsv1.DeploymentStatus{ReadyReplicas: 0},
	}
	fakeCli := fake.NewClientBuilder().WithScheme(s).WithObjects(notReady).Build()
	r := &FoundryGraphReconciler{Client: fakeCli, ReadinessTimeout: 100 * time.Millisecond}

	fg := &flowv1.FoundryGraph{ObjectMeta: metav1.ObjectMeta{Name: "flow-graph", Namespace: "test-ns"}}
	err := r.waitForReadiness(context.Background(), fg)
	if err == nil {
		t.Fatal("expected a readiness timeout error when the pod never becomes ready")
	}
	if apierrors.IsConflict(err) {
		t.Fatalf("expected a timeout error, got a conflict: %v", err)
	}
	if !strings.Contains(err.Error(), "readiness timeout") {
		t.Errorf("expected a readiness-timeout error, got: %v", err)
	}
}

// TestWaitForReadinessCtxCancellation exercises the ctx.Done branch of waitForReadiness: a
// cancellation surfaced mid-poll must return the context error, not the readiness-timeout
// error.
func TestWaitForReadinessCtxCancellation(t *testing.T) {
	s := scheme.Scheme
	_ = appsv1.AddToScheme(s)
	_ = flowv1.AddToScheme(s)

	replicas := int32(1)
	notReady := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "cartographer-flow-graph", Namespace: "test-ns"},
		Spec:       appsv1.DeploymentSpec{Replicas: &replicas},
		Status:     appsv1.DeploymentStatus{ReadyReplicas: 0},
	}
	fakeCli := fake.NewClientBuilder().WithScheme(s).WithObjects(notReady).Build()
	// A context that is cancelled before the poll's 5s sleep elapses → ctx.Done fires.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r := &FoundryGraphReconciler{Client: fakeCli, ReadinessTimeout: time.Minute}
	fg := &flowv1.FoundryGraph{ObjectMeta: metav1.ObjectMeta{Name: "flow-graph", Namespace: "test-ns"}}
	err := r.waitForReadiness(ctx, fg)
	if err == nil {
		t.Fatal("expected a context-cancellation error")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled, got %v", err)
	}
}

// TestAllReplicasReady asserts allReplicasReady's guard branches: nil replicas, zero
// replicas, and partial readiness all report not-ready; equal readiness reports ready.
func TestAllReplicasReady(t *testing.T) {
	if allReplicasReady(&appsv1.Deployment{}) {
		t.Error("expected nil replicas to be not ready")
	}
	zero := int32(0)
	if allReplicasReady(&appsv1.Deployment{Spec: appsv1.DeploymentSpec{Replicas: &zero}}) {
		t.Error("expected zero replicas to be not ready")
	}
	one := int32(1)
	if allReplicasReady(&appsv1.Deployment{Spec: appsv1.DeploymentSpec{Replicas: &one}, Status: appsv1.DeploymentStatus{ReadyReplicas: 0}}) {
		t.Error("expected fewer ready replicas than desired to be not ready")
	}
	if !allReplicasReady(&appsv1.Deployment{Spec: appsv1.DeploymentSpec{Replicas: &one}, Status: appsv1.DeploymentStatus{ReadyReplicas: 1}}) {
		t.Error("expected all-replicas-ready to be ready")
	}
}

type mockCartographerClient struct {
	applySchemaFn func(context.Context, *flowv1gen.ApplySchemaRequest) (*flowv1gen.ApplySchemaResponse, error)
	wipeGraphFn   func(context.Context, *flowv1gen.WipeGraphRequest) (*flowv1gen.WipeGraphResponse, error)
	healthCheckFn func(context.Context, *flowv1gen.HealthCheckRequest) (*flowv1gen.HealthCheckResponse, error)
	exportGraphFn func(context.Context, *flowv1gen.ExportGraphRequest) (flowv1gen.CartographerService_ExportGraphClient, error)
	closeFn       func() error
}

func (m *mockCartographerClient) ApplySchema(ctx context.Context, in *flowv1gen.ApplySchemaRequest, opts ...grpc.CallOption) (*flowv1gen.ApplySchemaResponse, error) {
	if m.applySchemaFn != nil {
		return m.applySchemaFn(ctx, in)
	}
	return &flowv1gen.ApplySchemaResponse{}, nil
}

func (m *mockCartographerClient) WipeGraph(ctx context.Context, in *flowv1gen.WipeGraphRequest, opts ...grpc.CallOption) (*flowv1gen.WipeGraphResponse, error) {
	if m.wipeGraphFn != nil {
		return m.wipeGraphFn(ctx, in)
	}
	return &flowv1gen.WipeGraphResponse{}, nil
}

func (m *mockCartographerClient) HealthCheck(ctx context.Context, in *flowv1gen.HealthCheckRequest, opts ...grpc.CallOption) (*flowv1gen.HealthCheckResponse, error) {
	if m.healthCheckFn != nil {
		return m.healthCheckFn(ctx, in)
	}
	return &flowv1gen.HealthCheckResponse{}, nil
}

func (m *mockCartographerClient) ExportGraph(ctx context.Context, in *flowv1gen.ExportGraphRequest, opts ...grpc.CallOption) (flowv1gen.CartographerService_ExportGraphClient, error) {
	if m.exportGraphFn != nil {
		return m.exportGraphFn(ctx, in)
	}
	return &mockExportGraphClient{}, nil
}

func (m *mockCartographerClient) Close() error {
	if m.closeFn != nil {
		return m.closeFn()
	}
	return nil
}

type mockExportGraphClient struct{}

func (mockExportGraphClient) Recv() (*flowv1gen.ExportGraphResponse, error) { return nil, io.EOF }
func (mockExportGraphClient) Context() context.Context                      { return context.Background() }
func (mockExportGraphClient) Header() (metadata.MD, error)                  { return nil, nil }
func (mockExportGraphClient) Trailer() metadata.MD                          { return nil }
func (mockExportGraphClient) CloseSend() error                              { return nil }
func (mockExportGraphClient) SendMsg(any) error                             { return nil }
func (mockExportGraphClient) RecvMsg(any) error                             { return nil }
