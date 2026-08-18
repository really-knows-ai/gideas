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
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	ctrl "sigs.k8s.io/controller-runtime"

	flowv1gen "github.com/foundry/flow/gen/flow/v1"
	flowv1 "github.com/foundry/flow/operator/api/v1"
)

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
			Name:      defaultGraphName,
			Namespace: testNS,
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
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: defaultGraphName, Namespace: testNS}}); err == nil {
		t.Fatal("expected Reconcile to return an error for a blocked destructive change")
	}

	var got flowv1.FoundryGraph
	if err := fakeCli.Get(ctx, types.NamespacedName{Name: defaultGraphName, Namespace: testNS}, &got); err != nil {
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
			Name:      defaultGraphName,
			Namespace: testNS,
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
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: defaultGraphName, Namespace: testNS}}); err == nil {
		t.Fatal("expected Reconcile to return an error for a failed non-destructive change")
	}

	var got flowv1.FoundryGraph
	if err := fakeCli.Get(ctx, types.NamespacedName{Name: defaultGraphName, Namespace: testNS}, &got); err != nil {
		t.Fatalf("get FoundryGraph: %v", err)
	}
	ready := meta.FindStatusCondition(got.Status.Conditions, "Ready")
	if ready == nil {
		t.Fatal("expected Ready condition to be set")
	}
	if ready.Status != metav1.ConditionFalse {
		t.Errorf("expected Ready=False, got %v", ready.Status)
	}
	if ready.Reason != reasonReconcileFailed {
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
			Name:      defaultGraphName,
			Namespace: testNS,
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
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: defaultGraphName, Namespace: testNS}}); err == nil {
		t.Fatal("expected Reconcile to return an error for a non-blocked WipeGraph failure")
	}

	var got flowv1.FoundryGraph
	if err := fakeCli.Get(ctx, types.NamespacedName{Name: defaultGraphName, Namespace: testNS}, &got); err != nil {
		t.Fatalf("get FoundryGraph: %v", err)
	}
	if blocked := meta.FindStatusCondition(got.Status.Conditions, "DestructiveChangeBlocked"); blocked != nil {
		t.Errorf("expected no DestructiveChangeBlocked condition for non-blocked error, got %v", blocked)
	}
	ready := meta.FindStatusCondition(got.Status.Conditions, "Ready")
	if ready == nil || ready.Status != metav1.ConditionFalse || ready.Reason != reasonReconcileFailed {
		t.Errorf("expected Ready=False/ReconcileFailed, got %v", ready)
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

	fg := &flowv1.FoundryGraph{ObjectMeta: metav1.ObjectMeta{Name: defaultGraphName, Namespace: testNS}}
	// Operator-namespace signing Secret is intentionally absent → reconcileSecrets fails.
	fakeCli := fake.NewClientBuilder().WithScheme(s).WithObjects(fg).WithStatusSubresource(fg).Build()
	r := &FoundryGraphReconciler{
		Client:            fakeCli,
		Scheme:            s,
		OperatorNamespace: operatorTestNS,
		CartographerPort:  50051,
		ProxyRoutingTable: NewProxyRoutingTable(),
		CartographerDialer: func(ctx context.Context, cfg string) (CartographerClient, error) {
			return &mockCartographerClient{}, nil
		},
	}

	ctx := context.Background()
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: defaultGraphName, Namespace: testNS}}); err == nil {
		t.Fatal("expected Reconcile to return an error on infra failure")
	}

	var got flowv1.FoundryGraph
	if err := fakeCli.Get(ctx, types.NamespacedName{Name: defaultGraphName, Namespace: testNS}, &got); err != nil {
		t.Fatalf("get FoundryGraph: %v", err)
	}
	ready := meta.FindStatusCondition(got.Status.Conditions, "Ready")
	if ready == nil || ready.Status != metav1.ConditionFalse || ready.Reason != reasonReconcileFailed {
		t.Errorf("expected Ready=False/ReconcileFailed, got %v", ready)
	}
}

// TestReconcileInfraGatingErrorBranches drives the gating-path error branches of
// reconcilePVC, reconcileRBAC, reconcileDeployment, and reconcileService (item 8): each
// provisioning step's failure must funnel into setFailedCondition, producing
// Ready=False/ReconcileFailed and a non-nil error so controller-runtime re-queues with
// backoff (SPEC R6 provisioning steps 1, 2, 3, 4).
func TestReconcileInfraGatingErrorBranches(t *testing.T) {
	s := scheme.Scheme
	_ = flowv1.AddToScheme(s)
	_ = appsv1.AddToScheme(s)
	_ = corev1.AddToScheme(s)
	_ = rbacv1.AddToScheme(s)

	ns := testNS
	// Operator-namespace signing secrets so reconcileSecrets (which runs after PVC and
	// before RBAC/Deployment/Service) succeeds.
	osign := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: operatorSigningKeySecretName, Namespace: "operator-ns"}, Data: map[string][]byte{"key": []byte("op"), "private-key": []byte("op-p")}}
	ssign := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: sidecarSigningKeySecretName, Namespace: "operator-ns"}, Data: map[string][]byte{"key": []byte("sd"), "private-key": []byte("sd-p")}}

	cases := []struct {
		name     string
		failType client.Object // the resource type whose Create must fail
		errMsg   string        // substring expected in the wrapped reconcile error
	}{
		{"reconcilePVC", &corev1.PersistentVolumeClaim{}, "reconcile PVC"},
		{"reconcileRBAC", &corev1.ServiceAccount{}, "reconcile ServiceAccount"},
		{"reconcileDeployment", &appsv1.Deployment{}, "reconcile Deployment"},
		{"reconcileService", &corev1.Service{}, "reconcile Service"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fg := &flowv1.FoundryGraph{ObjectMeta: metav1.ObjectMeta{Name: defaultGraphName, Namespace: ns}}
			interceptorFuncs := interceptor.Funcs{
				Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
					if reflect.TypeOf(obj) == reflect.TypeOf(tc.failType) {
						return errors.New("injected create failure")
					}
					return nil
				},
			}
			fakeCli := fake.NewClientBuilder().WithScheme(s).WithObjects(fg, osign, ssign).WithStatusSubresource(fg).WithInterceptorFuncs(interceptorFuncs).Build()
			r := &FoundryGraphReconciler{
				Client:            fakeCli,
				Scheme:            s,
				OperatorNamespace: "operator-ns",
				ProxyRoutingTable: NewProxyRoutingTable(),
				CartographerDialer: func(ctx context.Context, cfg string) (CartographerClient, error) {
					return &mockCartographerClient{}, nil
				},
			}

			ctx := context.Background()
			_, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: defaultGraphName, Namespace: ns}})
			if err == nil {
				t.Fatalf("expected Reconcile to return an error when %s fails", tc.name)
			}
			if !strings.Contains(err.Error(), tc.errMsg) {
				t.Errorf("expected the reconcile error to surface %q, got: %v", tc.errMsg, err)
			}

			var got flowv1.FoundryGraph
			if err := fakeCli.Get(ctx, types.NamespacedName{Name: defaultGraphName, Namespace: ns}, &got); err != nil {
				t.Fatalf("get FoundryGraph: %v", err)
			}
			ready := meta.FindStatusCondition(got.Status.Conditions, "Ready")
			if ready == nil || ready.Status != metav1.ConditionFalse || ready.Reason != reasonReconcileFailed {
				t.Errorf("expected Ready=False/ReconcileFailed after %s failure, got %v", tc.name, ready)
			}
		})
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

	ns := testNS
	// Destructive diff: removed Widget entity drives the WipeGraph path.
	fg := &flowv1.FoundryGraph{
		ObjectMeta: metav1.ObjectMeta{
			Name:      defaultGraphName,
			Namespace: ns,
			Annotations: map[string]string{
				lastAppliedSpecAnnotation: `{"entityTypes":[{"name":"Widget"}]}`,
			},
		},
	}
	fakeClient := fake.NewClientBuilder().WithScheme(s).WithObjects(fg).WithStatusSubresource(fg).Build()
	ctx := context.Background()
	nn := types.NamespacedName{Name: defaultGraphName, Namespace: ns}

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
	if ready == nil || ready.Status != metav1.ConditionFalse || ready.Reason != reasonReconcileFailed {
		t.Errorf("expected Ready=False/ReconcileFailed, got %v", ready)
	}
}
