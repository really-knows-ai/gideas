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
	"strings"
	"testing"
	"time"

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

	flowv1 "github.com/foundry/flow/operator/api/v1"
)

// TestReconcileSingletonConflictSetsConditionAndDoesNotProvision pins SPEC R1 singleton
// enforcement (item 1): a second FoundryGraph in a namespace must set the
// FoundryGraphConflict condition and must NOT be provisioned (no PVC, Deployment, or
// Service, and no status.endpoint), while the earliest-created resource remains the
// provisioned owner.
func TestReconcileSingletonConflictSetsConditionAndDoesNotProvision(t *testing.T) {
	s := scheme.Scheme
	_ = flowv1.AddToScheme(s)
	_ = appsv1.AddToScheme(s)
	_ = corev1.AddToScheme(s)
	_ = rbacv1.AddToScheme(s)

	ns := testNS
	// The owner: earliest-created FoundryGraph in the namespace.
	owner := &flowv1.FoundryGraph{
		ObjectMeta: metav1.ObjectMeta{
			Name:              defaultGraphName,
			Namespace:         ns,
			CreationTimestamp: metav1.Time{Time: time.Now().Add(-time.Hour)},
		},
	}
	// The conflict: a later-created second FoundryGraph.
	second := &flowv1.FoundryGraph{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "second-graph",
			Namespace:         ns,
			CreationTimestamp: metav1.Time{Time: time.Now()},
		},
	}
	fakeCli := fake.NewClientBuilder().WithScheme(s).WithObjects(owner, second).WithStatusSubresource(owner, second).Build()
	r := &FoundryGraphReconciler{Client: fakeCli, Scheme: s, ProxyRoutingTable: NewProxyRoutingTable()}

	ctx := context.Background()
	// Reconciling the conflicting resource must set FoundryGraphConflict and provision
	// nothing — no error (no requeue-with-backoff; resolution is user action), but a
	// RequeueAfter on the same 10m cadence as the Ready path so owner promotion is
	// re-evaluated promptly after the namespace owner is deleted (SPEC R1).
	result, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: "second-graph", Namespace: ns}})
	if err != nil {
		t.Fatalf("expected no error for a singleton conflict (no backoff requeue), got: %v", err)
	}
	if result.RequeueAfter != 10*time.Minute {
		t.Errorf("expected the conflict result to requeue after 10m (owner-promotion re-evaluation cadence), got %+v", result)
	}

	var got flowv1.FoundryGraph
	if err := fakeCli.Get(ctx, types.NamespacedName{Name: "second-graph", Namespace: ns}, &got); err != nil {
		t.Fatalf("get second FoundryGraph: %v", err)
	}
	conflict := meta.FindStatusCondition(got.Status.Conditions, "FoundryGraphConflict")
	if conflict == nil || conflict.Status != metav1.ConditionTrue {
		t.Fatalf("expected FoundryGraphConflict=True on the second FoundryGraph, got %v", conflict)
	}
	if got.Status.Endpoint.Host != "" || got.Status.Endpoint.Port != 0 {
		t.Errorf("expected no status.endpoint on the conflicting FoundryGraph, got %+v", got.Status.Endpoint)
	}
	// No provisioning: no PVC, no Deployment, no Service for the second resource.
	var pvc corev1.PersistentVolumeClaim
	if err := fakeCli.Get(ctx, types.NamespacedName{Name: "data-second-graph", Namespace: ns}, &pvc); err == nil {
		t.Error("expected no PVC for the conflicting FoundryGraph")
	}
	var d appsv1.Deployment
	if err := fakeCli.Get(ctx, types.NamespacedName{Name: "cartographer-second-graph", Namespace: ns}, &d); err == nil {
		t.Error("expected no Deployment for the conflicting FoundryGraph")
	}
	var svc corev1.Service
	if err := fakeCli.Get(ctx, types.NamespacedName{Name: "cartographer-second-graph", Namespace: ns}, &svc); err == nil {
		t.Error("expected no Service for the conflicting FoundryGraph")
	}
}

// TestReconcileNoFoundryGraphDoesNotProvision pins SPEC R1's no-provisioning branch: "If
// no FoundryGraph exists in the namespace, the Operator does not provision a Cartographer."
// A Reconcile request naming a FoundryGraph that does not exist — the nil-found path,
// matching how the controller's Reconcile is invoked for a nonexistent name — must return
// without error (client.IgnoreNotFound) and never reach the create path: no Cartographer
// PVC, Deployment, or Service is provisioned, the Cartographer is never dialed, and no
// proxy route is registered.
func TestReconcileNoFoundryGraphDoesNotProvision(t *testing.T) {
	s := scheme.Scheme
	_ = flowv1.AddToScheme(s)
	_ = appsv1.AddToScheme(s)
	_ = corev1.AddToScheme(s)
	_ = rbacv1.AddToScheme(s)

	// No FoundryGraph exists in the namespace.
	fakeClient := fake.NewClientBuilder().WithScheme(s).Build()
	dialCalled := false
	r := &FoundryGraphReconciler{
		Client:            fakeClient,
		Scheme:            s,
		OperatorNamespace: "operator-ns",
		CartographerPort:  50051,
		CartographerImage: "cartographer:latest",
		ReadinessTimeout:  time.Second,
		ProxyRoutingTable: NewProxyRoutingTable(),
		CartographerDialer: func(ctx context.Context, endpoint string) (CartographerClient, error) {
			dialCalled = true
			return &mockCartographerClient{}, nil
		},
	}

	ctx := context.Background()
	nn := types.NamespacedName{Name: defaultGraphName, Namespace: testNS}
	result, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: nn})
	if err != nil {
		t.Fatalf("expected a reconcile for a nonexistent FoundryGraph to return without error (client.IgnoreNotFound), got: %v", err)
	}
	if result != (ctrl.Result{}) {
		t.Errorf("expected no requeue for a nonexistent FoundryGraph, got %+v", result)
	}

	// SPEC R1: no Cartographer is provisioned — no PVC, Deployment, or Service.
	var pvc corev1.PersistentVolumeClaim
	if err := fakeClient.Get(ctx, types.NamespacedName{Name: "data-" + defaultGraphName, Namespace: testNS}, &pvc); err == nil {
		t.Error("expected no Cartographer PVC when no FoundryGraph exists")
	}
	var d appsv1.Deployment
	if err := fakeClient.Get(ctx, types.NamespacedName{Name: cartographerSvcName, Namespace: testNS}, &d); err == nil {
		t.Error("expected no Cartographer Deployment when no FoundryGraph exists")
	}
	var svc corev1.Service
	if err := fakeClient.Get(ctx, types.NamespacedName{Name: cartographerSvcName, Namespace: testNS}, &svc); err == nil {
		t.Error("expected no Cartographer Service when no FoundryGraph exists")
	}
	// The create path is never reached: the Cartographer is never dialed and no proxy
	// route is registered.
	if dialCalled {
		t.Error("expected the Cartographer never to be dialed when no FoundryGraph exists")
	}
	if _, ok := r.ProxyRoutingTable.Lookup(testNS, defaultGraphName); ok {
		t.Error("expected no proxy route registered when no FoundryGraph exists")
	}
}

// TestReconcileSingletonOwnerPromotion pins the ownership transition (SPEC R1): after the
// earliest-created FoundryGraph is deleted, the remaining resource becomes the namespace
// owner and is provisioned to Ready, with the stale FoundryGraphConflict condition
// cleared.
func TestReconcileSingletonOwnerPromotion(t *testing.T) {
	s := scheme.Scheme
	_ = flowv1.AddToScheme(s)
	_ = appsv1.AddToScheme(s)
	_ = corev1.AddToScheme(s)
	_ = rbacv1.AddToScheme(s)

	ns := testNS
	// The sole remaining FoundryGraph carries a stale FoundryGraphConflict condition from
	// when the (now deleted) owner still existed.
	fg := &flowv1.FoundryGraph{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "second-graph",
			Namespace: ns,
		},
		Status: flowv1.FoundryGraphStatus{
			Conditions: []metav1.Condition{{
				Type:   "FoundryGraphConflict",
				Status: metav1.ConditionTrue,
			}},
		},
	}

	// Operator-namespace signing secrets so reconcileSecrets succeeds.
	osign := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: operatorSigningKeySecretName, Namespace: "operator-ns"}, Data: map[string][]byte{"key": []byte("op"), "private-key": []byte("op-p")}}
	ssign := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: sidecarSigningKeySecretName, Namespace: "operator-ns"}, Data: map[string][]byte{"key": []byte("sd"), "private-key": []byte("sd-p")}}
	// A ready Deployment so waitForReadiness returns immediately.
	replicas := int32(1)
	readyDeploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "cartographer-second-graph", Namespace: ns},
		Spec:       appsv1.DeploymentSpec{Replicas: &replicas},
		Status:     appsv1.DeploymentStatus{AvailableReplicas: 1, ReadyReplicas: 1, UpdatedReplicas: 1},
	}

	fakeClient := fake.NewClientBuilder().WithScheme(s).WithObjects(fg, osign, ssign, readyDeploy).WithStatusSubresource(fg).Build()
	ctx := context.Background()
	nn := types.NamespacedName{Name: "second-graph", Namespace: ns}

	r := &FoundryGraphReconciler{
		Client:             fakeClient,
		Scheme:             s,
		OperatorNamespace:  "operator-ns",
		CartographerPort:   50051,
		CartographerImage:  "cartographer:latest",
		ReadinessTimeout:   time.Second,
		ProxyRoutingTable:  NewProxyRoutingTable(),
		CartographerDialer: fullSuccessDialer,
	}
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: nn}); err != nil {
		t.Fatalf("unexpected error on promoted-owner reconcile: %v", err)
	}

	var got flowv1.FoundryGraph
	if err := fakeClient.Get(ctx, nn, &got); err != nil {
		t.Fatalf("get FoundryGraph: %v", err)
	}
	if cond := meta.FindStatusCondition(got.Status.Conditions, "FoundryGraphConflict"); cond != nil {
		t.Errorf("expected stale FoundryGraphConflict to be cleared on promotion, got %v", cond)
	}
	ready := meta.FindStatusCondition(got.Status.Conditions, "Ready")
	if ready == nil || ready.Status != metav1.ConditionTrue || ready.Reason != reasonReconciled {
		t.Errorf("expected Ready=True/Reconciled after promotion, got %v", ready)
	}
	// The promoted resource is now provisioned (SPEC R1: the owner triggers provisioning).
	var d appsv1.Deployment
	if err := fakeClient.Get(ctx, types.NamespacedName{Name: "cartographer-second-graph", Namespace: ns}, &d); err != nil {
		t.Errorf("expected the promoted FoundryGraph to be provisioned with a Deployment, got: %v", err)
	}
}

// TestEnforceSingletonListError covers the r.List error branch of enforceSingleton
// (SPEC R1 singleton enforcement): a list failure (RBAC/apiserver/transient) must be
// surfaced as an error (funnelling into setFailedCondition in Reconcile) rather than
// silently treating the namespace as empty, which would let a duplicate FoundryGraph be
// provisioned.
func TestEnforceSingletonListError(t *testing.T) {
	s := scheme.Scheme
	_ = flowv1.AddToScheme(s)

	fg := &flowv1.FoundryGraph{ObjectMeta: metav1.ObjectMeta{Name: defaultGraphName, Namespace: testNS}}
	interceptorFuncs := interceptor.Funcs{
		List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
			return errors.New("apiserver unavailable")
		},
	}
	fakeCli := fake.NewClientBuilder().WithScheme(s).WithObjects(fg).WithInterceptorFuncs(interceptorFuncs).Build()
	r := &FoundryGraphReconciler{Client: fakeCli, Scheme: s}

	conflict, err := r.enforceSingleton(context.Background(), fg)
	if err == nil {
		t.Fatal("expected enforceSingleton to surface the List error")
	}
	if conflict {
		t.Error("expected conflict=false when the list itself failed")
	}
	if !strings.Contains(err.Error(), "list FoundryGraphs for singleton enforcement") {
		t.Errorf("expected the list error to be wrapped with context, got: %v", err)
	}
}

// TestEnforceSingletonEqualTimestampNameTiebreak covers the equal-CreationTimestamp
// name-tiebreak branch of enforceSingleton: when two FoundryGraphs share a creation
// timestamp (created together, or the zero-timestamp fake-client case), the owner is the
// lexicographically-earlier name and the other resource is the conflict.
func TestEnforceSingletonEqualTimestampNameTiebreak(t *testing.T) {
	s := scheme.Scheme
	_ = flowv1.AddToScheme(s)

	// Both resources share the same creation timestamp; "a-graph" sorts before
	// "z-graph" and must be the owner.
	ts := metav1.Now()
	a := &flowv1.FoundryGraph{ObjectMeta: metav1.ObjectMeta{Name: "a-graph", Namespace: testNS, CreationTimestamp: ts}}
	z := &flowv1.FoundryGraph{ObjectMeta: metav1.ObjectMeta{Name: "z-graph", Namespace: testNS, CreationTimestamp: ts}}

	t.Run("later name is the conflict", func(t *testing.T) {
		fakeCli := fake.NewClientBuilder().WithScheme(s).WithObjects(a, z).Build()
		r := &FoundryGraphReconciler{Client: fakeCli, Scheme: s}
		conflict, err := r.enforceSingleton(context.Background(), z)
		if err != nil {
			t.Fatalf("enforceSingleton: %v", err)
		}
		if !conflict {
			t.Error("expected the lexicographically-later name to be the conflict on equal timestamps")
		}
	})

	t.Run("earlier name is the owner", func(t *testing.T) {
		fakeCli := fake.NewClientBuilder().WithScheme(s).WithObjects(a, z).Build()
		r := &FoundryGraphReconciler{Client: fakeCli, Scheme: s}
		conflict, err := r.enforceSingleton(context.Background(), a)
		if err != nil {
			t.Fatalf("enforceSingleton: %v", err)
		}
		if conflict {
			t.Error("expected the lexicographically-earlier name to be the owner, not a conflict")
		}
	})
}
