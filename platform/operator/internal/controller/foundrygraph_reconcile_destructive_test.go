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
	"reflect"
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
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	ctrl "sigs.k8s.io/controller-runtime"

	flowv1gen "github.com/foundry/flow/gen/flow/v1"
	flowv1 "github.com/foundry/flow/operator/api/v1"
)

// TestReconcileDestructiveSuccessReachesReady is the reconcile-level end-to-end test for
// the SPEC R6 destructive spec-change SUCCESS flow (item 9): a destructive diff (removed
// entity type) must run HealthCheck → WipeGraph → ApplySchema on the existing pod, then
// redeploy (infra), wait for the new pod's readiness, re-apply the schema on the new pod
// (step 10), and reach Ready=True with no DestructiveChangeBlocked condition.
func TestReconcileDestructiveSuccessReachesReady(t *testing.T) {
	s := scheme.Scheme
	_ = flowv1.AddToScheme(s)
	_ = appsv1.AddToScheme(s)
	_ = corev1.AddToScheme(s)
	_ = rbacv1.AddToScheme(s)

	ns := testNS
	// Destructive diff: the last-applied annotation records a Widget entity type that the
	// current spec removes.
	fg := &flowv1.FoundryGraph{
		ObjectMeta: metav1.ObjectMeta{
			Name:      defaultGraphName,
			Namespace: ns,
			Annotations: map[string]string{
				lastAppliedSpecAnnotation: `{"entityTypes":[{"name":"Widget"}]}`,
			},
		},
	}

	// Operator-namespace signing secrets so reconcileSecrets succeeds.
	osign := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: operatorSigningKeySecretName, Namespace: "operator-ns"}, Data: map[string][]byte{"key": []byte("op"), "private-key": []byte("op-p")}}
	ssign := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: sidecarSigningKeySecretName, Namespace: "operator-ns"}, Data: map[string][]byte{"key": []byte("sd"), "private-key": []byte("sd-p")}}
	// A ready Deployment so waitForReadiness returns immediately after the redeploy.
	replicas := int32(1)
	readyDeploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: cartographerSvcName, Namespace: ns},
		Spec:       appsv1.DeploymentSpec{Replicas: &replicas},
		Status:     appsv1.DeploymentStatus{AvailableReplicas: 1, ReadyReplicas: 1, UpdatedReplicas: 1},
	}

	fakeClient := fake.NewClientBuilder().WithScheme(s).WithObjects(fg, osign, ssign, readyDeploy).WithStatusSubresource(fg).Build()
	ctx := context.Background()
	nn := types.NamespacedName{Name: defaultGraphName, Namespace: ns}

	// Record the RPC call order across both dials (existing pod + new pod at step 10).
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
				wipeGraphFn: func(context.Context, *flowv1gen.WipeGraphRequest) (*flowv1gen.WipeGraphResponse, error) {
					order = append(order, rpcWipe)
					return &flowv1gen.WipeGraphResponse{}, nil
				},
				applySchemaFn: func(context.Context, *flowv1gen.ApplySchemaRequest) (*flowv1gen.ApplySchemaResponse, error) {
					order = append(order, rpcApply)
					return &flowv1gen.ApplySchemaResponse{}, nil
				},
			}, nil
		},
	}

	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: nn}); err != nil {
		t.Fatalf("unexpected error on successful destructive reconcile: %v", err)
	}

	// SPEC R6 destructive ordering: on the existing pod HealthCheck → WipeGraph →
	// ApplySchema, then (after redeployment and new-pod readiness) the schema is
	// re-applied at step 10 with HealthCheck → ApplySchema. Exactly one WipeGraph, and it
	// precedes both ApplySchema calls.
	want := []string{rpcHealth, rpcWipe, rpcApply, rpcHealth, rpcApply}
	if len(order) != len(want) {
		t.Fatalf("expected RPC order %v, got %v", want, order)
	}
	for i := range want {
		if order[i] != want[i] {
			t.Errorf("expected order[%d]=%s, got %s (full: %v)", i, want[i], order[i], order)
		}
	}

	var got flowv1.FoundryGraph
	if err := fakeClient.Get(ctx, nn, &got); err != nil {
		t.Fatalf("get FoundryGraph: %v", err)
	}
	if cond := meta.FindStatusCondition(got.Status.Conditions, "DestructiveChangeBlocked"); cond != nil {
		t.Errorf("expected no DestructiveChangeBlocked on a successful destructive change, got %v", cond)
	}
	ready := meta.FindStatusCondition(got.Status.Conditions, "Ready")
	if ready == nil || ready.Status != metav1.ConditionTrue || ready.Reason != reasonReconciled {
		t.Errorf("expected Ready=True/Reconciled, got %v", ready)
	}
}

// TestReconcileDestructiveCrashBetweenWipeApplyAndAnnotationPersist pins the
// destructive-change crash window (SPEC R6): the last-applied-spec annotation must be
// persisted BEFORE the destructive ApplySchema takes effect on the existing pod, so a
// reconcile that returns between the existing-pod WipeGraph+ApplySchema and the
// updateStatus annotation persist (a crash) cannot re-detect the destructive diff on
// the next reconcile and re-run WipeGraph — silently wiping graph data written under
// the newly-applied schema in the interim. Without the fix the annotation still records
// the old spec after the crash, reconcile 2 re-detects the destructive diff, and
// WipeGraph runs a second time over the converged graph.
func TestReconcileDestructiveCrashBetweenWipeApplyAndAnnotationPersist(t *testing.T) {
	s := scheme.Scheme
	_ = flowv1.AddToScheme(s)
	_ = appsv1.AddToScheme(s)
	_ = corev1.AddToScheme(s)
	_ = rbacv1.AddToScheme(s)

	ns := testNS
	// Destructive diff: the last-applied annotation records a Widget entity type that
	// the current (empty) spec removes.
	fg := &flowv1.FoundryGraph{
		ObjectMeta: metav1.ObjectMeta{
			Name:      defaultGraphName,
			Namespace: ns,
			Annotations: map[string]string{
				lastAppliedSpecAnnotation: `{"entityTypes":[{"name":"Widget"}]}`,
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

	// The first ApplySchema (existing pod) succeeds; the second (step 10, after the
	// "new" pod's readiness) fails — the reconcile returns an error AFTER the
	// existing-pod wipe+apply but BEFORE updateStatus's annotation persist, exactly the
	// crash window under test.
	applyCalls := 0
	wipeCalls := 0
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
				wipeGraphFn: func(context.Context, *flowv1gen.WipeGraphRequest) (*flowv1gen.WipeGraphResponse, error) {
					wipeCalls++
					return &flowv1gen.WipeGraphResponse{}, nil
				},
				applySchemaFn: func(context.Context, *flowv1gen.ApplySchemaRequest) (*flowv1gen.ApplySchemaResponse, error) {
					applyCalls++
					if applyCalls == 2 {
						// Simulate the crash: reconcile 1 never reaches updateStatus.
						return nil, status.Error(codes.Internal, "crash after existing-pod apply")
					}
					return &flowv1gen.ApplySchemaResponse{}, nil
				},
			}, nil
		},
	}

	// Reconcile 1: HealthCheck → WipeGraph → ApplySchema on the existing pod, then a
	// "crash" (error return) before the annotation persist in updateStatus.
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: nn}); err == nil {
		t.Fatal("expected reconcile 1 to return an error (simulated crash after the existing-pod wipe+apply)")
	}
	if wipeCalls != 1 {
		t.Fatalf("expected exactly 1 WipeGraph on reconcile 1, got %d", wipeCalls)
	}

	// The annotation must ALREADY record the new spec: it was persisted before the
	// destructive ApplySchema took effect, so the crash cannot leave the old spec in
	// place for the next reconcile to diff against.
	var crashed flowv1.FoundryGraph
	if err := fakeClient.Get(ctx, nn, &crashed); err != nil {
		t.Fatalf("get FoundryGraph after reconcile 1: %v", err)
	}
	ann, ok := crashed.Annotations[lastAppliedSpecAnnotation]
	if !ok {
		t.Fatal("expected the last-applied-spec annotation to be persisted before the destructive apply (crash window closed)")
	}
	var recorded flowv1.FoundryGraphSpec
	if err := json.Unmarshal([]byte(ann), &recorded); err != nil {
		t.Fatalf("unmarshal annotation: %v", err)
	}
	if !specSemanticallyEqual(&recorded, &crashed.Spec) {
		t.Errorf("expected the annotation to record the new spec before the crash, got %s", ann)
	}

	// Reconcile 2 (the restart after the crash): the annotation now equals the current
	// spec, so no destructive diff is detected and WipeGraph must NOT run again — graph
	// data written under the newly-applied schema in the interim is preserved.
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: nn}); err != nil {
		t.Fatalf("reconcile 2: %v", err)
	}
	if wipeCalls != 1 {
		t.Errorf("expected WipeGraph exactly once across both reconciles (no re-wipe after the crash), got %d", wipeCalls)
	}
}

// TestReconcileMidReconcileEditKeepsAnnotationAndAppliedSchemaInSync pins the step-10
// snapshot contract end to end: a user spec edit landing between the reconcile-start
// snapshot and the step-10 re-fetch must NOT be applied at step 10. The reconcile
// applies the reconcile-start spec, records exactly that spec in the last-applied-spec
// annotation (so annotation == applied schema — the next reconcile sees no phantom
// diff), and leaves the edited spec for the next reconcile's diff, which re-evaluates
// the destructive decision (WipeGraph) against it. Before the fix, step 10 applied the
// re-fetched (edited) spec while the annotation recorded the start spec — the next
// reconcile re-detected a diff for an already-applied change and, for destructive
// edits, re-ran WipeGraph over a converged graph (wiping graph data written since the
// first apply).
func TestReconcileMidReconcileEditKeepsAnnotationAndAppliedSchemaInSync(t *testing.T) {
	s := scheme.Scheme
	_ = flowv1.AddToScheme(s)
	_ = appsv1.AddToScheme(s)
	_ = corev1.AddToScheme(s)
	_ = rbacv1.AddToScheme(s)

	ns := testNS
	// Last-applied annotation records Widget with property a; the reconcile-start spec
	// adds property b → a non-destructive diff.
	startSpec := flowv1.FoundryGraphSpec{
		EntityTypes: []flowv1.EntityTypeSpec{{
			Name: "Widget",
			Properties: []flowv1.PropertySpec{
				{Name: "a"},
				{Name: "b"},
			},
		}},
	}
	// The mid-reconcile user edit removes property b → destructive relative to the
	// start spec (would require WipeGraph if the operator converged to it in the same
	// reconcile the diff was computed against).
	editedSpec := flowv1.FoundryGraphSpec{
		EntityTypes: []flowv1.EntityTypeSpec{{
			Name:       "Widget",
			Properties: []flowv1.PropertySpec{{Name: "a"}},
		}},
	}

	fg := &flowv1.FoundryGraph{
		ObjectMeta: metav1.ObjectMeta{
			Name:       defaultGraphName,
			Namespace:  ns,
			Finalizers: []string{finalizerName},
			Annotations: map[string]string{
				lastAppliedSpecAnnotation: `{"entityTypes":[{"name":"Widget","properties":[{"name":"a"}]}]}`,
			},
		},
		Spec: startSpec,
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

	// The 2nd FoundryGraph Get of a reconcile is the step-10 re-fetch inside
	// applySchema. Serve the mid-reconcile edit there AND persist it to the store so
	// reconcile 2's initial Get (and diff) sees it — a faithful simulation of the
	// user's apiserver edit. Non-FoundryGraph Gets delegate to the store.
	fgGets := 0
	interceptorFuncs := interceptor.Funcs{
		Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			if _, ok := obj.(*flowv1.FoundryGraph); !ok {
				return c.Get(ctx, key, obj, opts...)
			}
			fgGets++
			if err := c.Get(ctx, key, obj, opts...); err != nil {
				return err
			}
			if fgGets == 2 {
				edit := obj.(*flowv1.FoundryGraph)
				edit.Spec = *editedSpec.DeepCopy()
				if err := c.Update(ctx, obj); err != nil {
					return err
				}
			}
			return nil
		},
	}
	fakeCli := fake.NewClientBuilder().WithScheme(s).WithObjects(fg, osign, ssign, readyDeploy).WithStatusSubresource(fg).WithInterceptorFuncs(interceptorFuncs).Build()

	var applied []*flowv1gen.Schema
	wipeCalls := 0
	r := &FoundryGraphReconciler{
		Client:            fakeCli,
		Scheme:            s,
		OperatorNamespace: "operator-ns",
		CartographerPort:  50051,
		CartographerImage: "cartographer:latest",
		ReadinessTimeout:  time.Second,
		ProxyRoutingTable: NewProxyRoutingTable(),
		CartographerDialer: func(ctx context.Context, endpoint string) (CartographerClient, error) {
			return &mockCartographerClient{
				applySchemaFn: func(ctx context.Context, req *flowv1gen.ApplySchemaRequest) (*flowv1gen.ApplySchemaResponse, error) {
					applied = append(applied, req.Schema)
					return &flowv1gen.ApplySchemaResponse{}, nil
				},
				wipeGraphFn: func(ctx context.Context, _ *flowv1gen.WipeGraphRequest) (*flowv1gen.WipeGraphResponse, error) {
					wipeCalls++
					return &flowv1gen.WipeGraphResponse{}, nil
				},
			}, nil
		},
	}

	ctx := context.Background()
	nn := types.NamespacedName{Name: defaultGraphName, Namespace: ns}

	// Reconcile 1: the annotation→start diff is non-destructive (adds property b), so
	// no WipeGraph. The step-10 re-fetch lands on the mid-reconcile edit, but the
	// applied schema and the last-applied annotation must BOTH record the
	// reconcile-start spec — the annotation may not lag the schema actually applied.
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: nn}); err != nil {
		t.Fatalf("reconcile 1: %v", err)
	}
	if wipeCalls != 0 {
		t.Errorf("expected no WipeGraph on reconcile 1 (non-destructive diff), got %d", wipeCalls)
	}
	if len(applied) != 2 {
		t.Fatalf("expected 2 ApplySchema calls on reconcile 1 (existing pod + step 10), got %d", len(applied))
	}
	wantStart := (&FoundryGraphReconciler{}).schemaFromCRD(&startSpec)
	if !reflect.DeepEqual(applied[len(applied)-1], wantStart) {
		t.Errorf("expected the step-10 ApplySchema to receive the reconcile-start spec, got %+v", applied[len(applied)-1])
	}
	if notWant := (&FoundryGraphReconciler{}).schemaFromCRD(&editedSpec); reflect.DeepEqual(applied[len(applied)-1], notWant) {
		t.Error("expected the step-10 ApplySchema NOT to apply the mid-reconcile edited spec")
	}

	var got flowv1.FoundryGraph
	if err := fakeCli.Get(ctx, nn, &got); err != nil {
		t.Fatalf("get FoundryGraph after reconcile 1: %v", err)
	}
	ann, ok := got.Annotations[lastAppliedSpecAnnotation]
	if !ok {
		t.Fatal("expected the last-applied-spec annotation after reconcile 1")
	}
	var recorded flowv1.FoundryGraphSpec
	if err := json.Unmarshal([]byte(ann), &recorded); err != nil {
		t.Fatalf("unmarshal annotation: %v", err)
	}
	if !specSemanticallyEqual(&recorded, &startSpec) {
		t.Errorf("expected the annotation to record the reconcile-start spec (== the applied schema), got %s", ann)
	}
	if specSemanticallyEqual(&recorded, &editedSpec) {
		t.Error("expected the annotation NOT to record the mid-reconcile edited spec")
	}

	// Reconcile 2: the persisted mid-reconcile edit (removes property b) is now the
	// current spec, so the annotation→current diff is destructive and is applied
	// exactly once — the first, legitimate application of the edited spec, with the
	// WipeGraph decision made against the edited spec (not re-discovered for an
	// already-applied change).
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: nn}); err != nil {
		t.Fatalf("reconcile 2: %v", err)
	}
	if wipeCalls != 1 {
		t.Errorf("expected exactly 1 WipeGraph across both reconciles (the destructive edit, applied once), got %d", wipeCalls)
	}
	if len(applied) != 4 {
		t.Fatalf("expected 4 ApplySchema calls across both reconciles, got %d", len(applied))
	}
	wantEdited := (&FoundryGraphReconciler{}).schemaFromCRD(&editedSpec)
	if !reflect.DeepEqual(applied[len(applied)-1], wantEdited) {
		t.Errorf("expected reconcile 2 to converge to the edited spec, got %+v", applied[len(applied)-1])
	}
}
