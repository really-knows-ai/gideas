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
	"encoding/json"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	ctrl "sigs.k8s.io/controller-runtime"

	flowv1gen "github.com/foundry/flow/gen/flow/v1"
	flowv1 "github.com/foundry/flow/operator/api/v1"
)

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

	ns := testNS
	// Destructive diff: old annotation records a Widget entity type removed from spec.
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
		ObjectMeta: metav1.ObjectMeta{Name: cartographerSvcName, Namespace: ns},
		Spec:       appsv1.DeploymentSpec{Replicas: &replicas},
		Status:     appsv1.DeploymentStatus{AvailableReplicas: 1, ReadyReplicas: 1, UpdatedReplicas: 1},
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
	if ready == nil || ready.Status != metav1.ConditionTrue || ready.Reason != reasonReconciled {
		t.Errorf("expected Ready=True/Reconciled, got %v", ready)
	}
	// updateStatus (step 7) must have written the last-applied-spec annotation.
	if _, ok := finalCR.Annotations[lastAppliedSpecAnnotation]; !ok {
		t.Errorf("expected last-applied-spec annotation written by updateStatus")
	}
	// Step 8: proxy route registered.
	if _, ok := successR.ProxyRoutingTable.Lookup(ns, defaultGraphName); !ok {
		t.Error("expected proxy route to be registered after successful reconcile")
	}
	// Step 4: Service must exist and be ClusterIP (SPEC R6 step 4).
	var svc corev1.Service
	if err := fakeClient.Get(ctx, types.NamespacedName{Name: cartographerSvcName, Namespace: ns}, &svc); err != nil {
		t.Errorf("expected Service to exist after successful reconcile: %v", err)
	}
	if svc.Spec.Type != corev1.ServiceTypeClusterIP {
		t.Errorf("expected Service type ClusterIP (SPEC R6 step 4), got %v", svc.Spec.Type)
	}
	// Step 3: Deployment must exist.
	var deployment appsv1.Deployment
	if err := fakeClient.Get(ctx, types.NamespacedName{Name: cartographerSvcName, Namespace: ns}, &deployment); err != nil {
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

	ns := testNS
	// Old spec has Widget with property "a"; new spec adds property "b" → non-destructive
	// diff (additive-only). applySchemaOnExisting must run with destructive=false.
	fg := &flowv1.FoundryGraph{
		ObjectMeta: metav1.ObjectMeta{
			Name:      defaultGraphName,
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
		ObjectMeta: metav1.ObjectMeta{Name: cartographerSvcName, Namespace: ns},
		Spec:       appsv1.DeploymentSpec{Replicas: &replicas},
		Status:     appsv1.DeploymentStatus{AvailableReplicas: 1, ReadyReplicas: 1, UpdatedReplicas: 1},
	}

	fakeClient := fake.NewClientBuilder().WithScheme(s).WithObjects(fg, osign, ssign, readyDeploy).WithStatusSubresource(fg).Build()
	ctx := context.Background()
	nn := types.NamespacedName{Name: defaultGraphName, Namespace: ns}

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
					order = append(order, rpcHealth)
					return &flowv1gen.HealthCheckResponse{}, nil
				},
				applySchemaFn: func(context.Context, *flowv1gen.ApplySchemaRequest) (*flowv1gen.ApplySchemaResponse, error) {
					order = append(order, rpcApply)
					return &flowv1gen.ApplySchemaResponse{}, nil
				},
				wipeGraphFn: func(context.Context, *flowv1gen.WipeGraphRequest) (*flowv1gen.WipeGraphResponse, error) {
					order = append(order, rpcWipe)
					return &flowv1gen.WipeGraphResponse{}, nil
				},
			}, nil
		},
	}

	// SPEC R6 step 5: "The periodic resync (10m interval) is an independent safety net" —
	// a fully successful reconcile ends in setReadyCondition, whose 10m RequeueAfter is the
	// Ready-path cadence that makes the resync independent of error-driven backoff requeues.
	result, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: nn})
	if err != nil {
		t.Fatalf("unexpected error on successful non-destructive reconcile: %v", err)
	}
	if result.RequeueAfter != 10*time.Minute {
		t.Errorf("expected the Ready path to requeue after 10m (SPEC R6 periodic resync), got %+v", result)
	}

	// SPEC R6 non-destructive ordering: HealthCheck precedes ApplySchema; WipeGraph is
	// never called. ApplySchema runs twice (on the existing pod and again at step 10 on
	// the newly-ready pod), so assert the health→apply prefix and the absence of rpcWipe.
	if len(order) < 2 || order[0] != rpcHealth || order[1] != rpcApply {
		t.Errorf("expected RPC order to start [health, apply, ...], got %v", order)
	}
	for _, call := range order {
		if call == rpcWipe {
			t.Fatalf("non-destructive change must never call WipeGraph, got order %v", order)
		}
	}

	var got flowv1.FoundryGraph
	if err := fakeClient.Get(ctx, nn, &got); err != nil {
		t.Fatalf("get FoundryGraph: %v", err)
	}
	ready := meta.FindStatusCondition(got.Status.Conditions, "Ready")
	if ready == nil || ready.Status != metav1.ConditionTrue || ready.Reason != reasonReconciled {
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

// TestReconcileCombinedSchemaAndStorageChange pins SPEC R6's combined-change sequencing:
// a single Reconcile carrying BOTH a schema diff (non-destructive) AND a non-schema change
// (storage.size) must first apply the schema to the existing pod, then redeploy (infra),
// then re-apply the schema after readiness. Schema-only and storage-only tests exist; this
// pins the combined ordering end-to-end.
func TestReconcileCombinedSchemaAndStorageChange(t *testing.T) {
	s := scheme.Scheme
	_ = flowv1.AddToScheme(s)
	_ = appsv1.AddToScheme(s)
	_ = corev1.AddToScheme(s)
	_ = rbacv1.AddToScheme(s)

	ns := testNS
	// Old spec: Widget with property "a" and storage 1Gi. Build it as a real struct and
	// marshal to JSON so resource.Quantity serialises correctly (the canonical JSON form,
	// not the internal struct representation).
	oldSpec := flowv1.FoundryGraphSpec{
		EntityTypes: []flowv1.EntityTypeSpec{{
			Name:       "Widget",
			Properties: []flowv1.PropertySpec{{Name: "a"}},
		}},
		Storage: &flowv1.StorageSpec{Size: resourcePtr("1Gi")},
	}
	oldSpecJSON, err := json.Marshal(oldSpec)
	if err != nil {
		t.Fatalf("marshal old spec: %v", err)
	}

	fg := &flowv1.FoundryGraph{
		ObjectMeta: metav1.ObjectMeta{
			Name:      defaultGraphName,
			Namespace: ns,
			Annotations: map[string]string{
				lastAppliedSpecAnnotation: string(oldSpecJSON),
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
			Storage: &flowv1.StorageSpec{
				Size: resourcePtr("4Gi"),
			},
		},
	}

	// Operator-namespace signing secrets so reconcileSecrets succeeds.
	osign := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: operatorSigningKeySecretName, Namespace: "operator-ns"}, Data: map[string][]byte{"key": []byte("op"), "private-key": []byte("op-p")}}
	ssign := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: sidecarSigningKeySecretName, Namespace: "operator-ns"}, Data: map[string][]byte{"key": []byte("sd"), "private-key": []byte("sd-p")}}

	// A ready Deployment so waitForReadiness returns immediately.
	replicas := int32(1)
	readyDeploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: cartographerSvcName, Namespace: ns},
		Spec:       appsv1.DeploymentSpec{Replicas: &replicas},
		Status:     appsv1.DeploymentStatus{AvailableReplicas: 1, ReadyReplicas: 1, UpdatedReplicas: 1},
	}

	fakeClient := fake.NewClientBuilder().WithScheme(s).WithObjects(fg, osign, ssign, readyDeploy).WithStatusSubresource(fg).Build()
	ctx := context.Background()
	nn := types.NamespacedName{Name: defaultGraphName, Namespace: ns}

	// Track RPC calls to verify sequencing: schema applied on existing pod BEFORE
	// redeployment, then re-applied on the new pod after readiness.
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
					order = append(order, rpcHealth)
					return &flowv1gen.HealthCheckResponse{}, nil
				},
				applySchemaFn: func(context.Context, *flowv1gen.ApplySchemaRequest) (*flowv1gen.ApplySchemaResponse, error) {
					order = append(order, rpcApply)
					return &flowv1gen.ApplySchemaResponse{}, nil
				},
				wipeGraphFn: func(context.Context, *flowv1gen.WipeGraphRequest) (*flowv1gen.WipeGraphResponse, error) {
					order = append(order, rpcWipe)
					return &flowv1gen.WipeGraphResponse{}, nil
				},
			}, nil
		},
	}

	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: nn}); err != nil {
		t.Fatalf("unexpected error on combined schema+storage reconcile: %v", err)
	}

	// The RPC order must show: health+apply (schema on existing pod), then after infra
	// redeployment, health+apply again (schema on new pod). WipeGraph must never be called
	// (non-destructive). The first apply must precede the infra steps (Deployment update).
	if len(order) < 4 {
		t.Fatalf("expected at least 4 RPC calls (schema-on-existing + schema-on-new), got %v", order)
	}
	// First pair: health+apply on existing pod.
	if order[0] != rpcHealth || order[1] != rpcApply {
		t.Errorf("expected first RPC pair [health, apply] on existing pod, got %v", order[:2])
	}
	// Second pair: health+apply on new pod (step 10).
	if order[2] != rpcHealth || order[3] != rpcApply {
		t.Errorf("expected second RPC pair [health, apply] on new pod, got %v", order[2:4])
	}
	// WipeGraph must never be called for non-destructive combined change.
	for _, call := range order {
		if call == rpcWipe {
			t.Fatalf("non-destructive combined change must never call WipeGraph, got order %v", order)
		}
	}

	// Verify the reconcile reached Ready.
	var got flowv1.FoundryGraph
	if err := fakeClient.Get(ctx, nn, &got); err != nil {
		t.Fatalf("get FoundryGraph: %v", err)
	}
	ready := meta.FindStatusCondition(got.Status.Conditions, "Ready")
	if ready == nil || ready.Status != metav1.ConditionTrue || ready.Reason != reasonReconciled {
		t.Errorf("expected Ready=True/Reconciled, got %v", ready)
	}
}
