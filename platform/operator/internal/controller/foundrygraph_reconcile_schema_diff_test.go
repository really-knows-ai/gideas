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

// TestReconcileSchemaDiffUnreachablePodFallsThroughToInfra pins item 10: a pending schema
// diff (destructive or non-destructive) while the Cartographer pod is unreachable (dial
// failure — the observable state of a missing Deployment) must NOT wedge the reconcile at
// the schema-diff branch. The reconcile falls through to the infra steps, so
// reconcileDeployment runs (recreating the missing Deployment) and the reconcile proceeds
// to the readiness gate instead of returning the dial error. This is the SPEC R6
// reconcile-to-desired-state contract (the 10m periodic resync is the independent safety
// net): a transient condition must never become a permanent backoff loop.
func TestReconcileSchemaDiffUnreachablePodFallsThroughToInfra(t *testing.T) {
	s := scheme.Scheme
	_ = flowv1.AddToScheme(s)
	_ = appsv1.AddToScheme(s)
	_ = corev1.AddToScheme(s)
	_ = rbacv1.AddToScheme(s)

	ns := testNS
	// Operator-namespace signing secrets so reconcileSecrets succeeds once the reconcile
	// falls through to the infra steps.
	osign := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: operatorSigningKeySecretName, Namespace: "operator-ns"}, Data: map[string][]byte{"key": []byte("op"), "private-key": []byte("op-p")}}
	ssign := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: sidecarSigningKeySecretName, Namespace: "operator-ns"}, Data: map[string][]byte{"key": []byte("sd"), "private-key": []byte("sd-p")}}

	cases := []struct {
		name string
		fg   *flowv1.FoundryGraph
	}{
		{
			name: "destructive diff",
			fg: &flowv1.FoundryGraph{
				ObjectMeta: metav1.ObjectMeta{
					Name:      defaultGraphName,
					Namespace: ns,
					Annotations: map[string]string{
						lastAppliedSpecAnnotation: `{"entityTypes":[{"name":"Widget"}]}`,
					},
				},
			},
		},
		{
			name: "non-destructive diff",
			fg: &flowv1.FoundryGraph{
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
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// The Deployment is MISSING (deleted) — the existing pod is unreachable, so
			// the dialer fails. reconcileDeployment must still run and recreate it.
			fakeClient := fake.NewClientBuilder().WithScheme(s).WithObjects(tc.fg, osign, ssign).WithStatusSubresource(tc.fg).Build()
			r := &FoundryGraphReconciler{
				Client:            fakeClient,
				Scheme:            s,
				OperatorNamespace: "operator-ns",
				CartographerPort:  50051,
				CartographerImage: "cartographer:latest",
				ReadinessTimeout:  time.Second,
				ProxyRoutingTable: NewProxyRoutingTable(),
				CartographerDialer: func(ctx context.Context, endpoint string) (CartographerClient, error) {
					return nil, errors.New("dial failed: cartographer unreachable")
				},
			}

			ctx := context.Background()
			nn := types.NamespacedName{Name: defaultGraphName, Namespace: ns}
			_, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: nn})
			if err == nil {
				t.Fatal("expected the reconcile to return an error (readiness requeue)")
			}
			// The schema-diff branch must NOT wedge the reconcile: the error must not be
			// the dial failure — the reconcile proceeded past it, through the infra steps,
			// to the readiness gate (the created Deployment has no ready status yet).
			if strings.Contains(err.Error(), "dial existing cartographer") {
				t.Errorf("expected the reconcile to proceed past the unreachable-pod dial failure, got: %v", err)
			}
			if !strings.Contains(err.Error(), "readiness") {
				t.Errorf("expected the reconcile to reach the readiness gate, got: %v", err)
			}

			// reconcileDeployment ran: the missing Deployment was recreated.
			var d appsv1.Deployment
			if err := fakeClient.Get(ctx, types.NamespacedName{Name: cartographerSvcName, Namespace: ns}, &d); err != nil {
				t.Errorf("expected the Deployment to be (re)created despite the unreachable pod, got: %v", err)
			}
			// Infra reconciliation is reachable: the PVC was provisioned too.
			var pvc corev1.PersistentVolumeClaim
			if err := fakeClient.Get(ctx, types.NamespacedName{Name: "data-" + defaultGraphName, Namespace: ns}, &pvc); err != nil {
				t.Errorf("expected the PVC to be provisioned, got: %v", err)
			}
		})
	}
}

// TestReconcileDuplicateTypeNameFailsPostProvisioning drives the SPEC R1 duplicate-name
// failure mode end to end: duplicate type names (like duplicate property names) are
// rejected at schema application time (INVALID_ARGUMENT) — NOT by an operator-side
// pre-provisioning check. The Operator provisions the Cartographer (PVC, Deployment,
// Service) and ApplySchema fails, surfacing via the ReconcileFailed status condition and
// a requeue-with-backoff error.
func TestReconcileDuplicateTypeNameFailsPostProvisioning(t *testing.T) {
	s := scheme.Scheme
	_ = flowv1.AddToScheme(s)
	_ = appsv1.AddToScheme(s)
	_ = corev1.AddToScheme(s)
	_ = rbacv1.AddToScheme(s)

	ns := testNS
	fg := &flowv1.FoundryGraph{
		ObjectMeta: metav1.ObjectMeta{Name: defaultGraphName, Namespace: ns},
		Spec: flowv1.FoundryGraphSpec{
			EntityTypes: []flowv1.EntityTypeSpec{
				{Name: "Widget"},
				{Name: "Widget"}, // duplicate type name — must fail at ApplySchema, post-provisioning
			},
		},
	}

	// Operator-namespace signing secrets so reconcileSecrets succeeds.
	osign := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: operatorSigningKeySecretName, Namespace: "operator-ns"}, Data: map[string][]byte{"key": []byte("op"), "private-key": []byte("op-p")}}
	ssign := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: sidecarSigningKeySecretName, Namespace: "operator-ns"}, Data: map[string][]byte{"key": []byte("sd"), "private-key": []byte("sd-p")}}
	// A ready Deployment so waitForReadiness returns immediately and the reconcile
	// reaches the step-10 ApplySchema.
	replicas := int32(1)
	readyDeploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: cartographerSvcName, Namespace: ns},
		Spec:       appsv1.DeploymentSpec{Replicas: &replicas},
		Status:     appsv1.DeploymentStatus{AvailableReplicas: 1, ReadyReplicas: 1, UpdatedReplicas: 1},
	}

	applyCalled := false
	fakeCli := fake.NewClientBuilder().WithScheme(s).WithObjects(fg, osign, ssign, readyDeploy).WithStatusSubresource(fg).Build()
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
				applySchemaFn: func(context.Context, *flowv1gen.ApplySchemaRequest) (*flowv1gen.ApplySchemaResponse, error) {
					applyCalled = true
					// SPEC R1: duplicate type names are rejected at schema application time.
					return nil, status.Error(codes.InvalidArgument, "duplicate entity type name Widget")
				},
			}, nil
		},
	}

	ctx := context.Background()
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: defaultGraphName, Namespace: ns}}); err == nil {
		t.Fatal("expected Reconcile to return an error (requeue with backoff) when ApplySchema rejects the duplicate type name")
	}

	// The duplicate type name must have reached the Cartographer — provisioning happened
	// first (post-provisioning failure mode), so ApplySchema was actually invoked.
	if !applyCalled {
		t.Error("expected ApplySchema to be invoked with the duplicate-type spec (post-provisioning failure mode)")
	}

	var got flowv1.FoundryGraph
	if err := fakeCli.Get(ctx, types.NamespacedName{Name: defaultGraphName, Namespace: ns}, &got); err != nil {
		t.Fatalf("get FoundryGraph: %v", err)
	}
	ready := meta.FindStatusCondition(got.Status.Conditions, "Ready")
	if ready == nil {
		t.Fatal("expected a Ready condition to be set for the invalid spec")
	}
	if ready.Status != metav1.ConditionFalse || ready.Reason != reasonReconcileFailed {
		t.Errorf("expected Ready=False/ReconcileFailed for the rejected schema, got %v", ready)
	}

	// Provisioning must have occurred before the failure (the Cartographer exists and
	// ApplySchema failed against it) — proving the operator-side pre-provisioning
	// duplicate-type rejection is gone.
	var pvc corev1.PersistentVolumeClaim
	if err := fakeCli.Get(ctx, types.NamespacedName{Name: "data-flow-graph", Namespace: ns}, &pvc); err != nil {
		t.Errorf("expected the PVC to be provisioned before the schema-apply failure, got: %v", err)
	}
}

// TestReconcileDuplicatePropertyNameFailsPostProvisioning mirrors the duplicate-type-name
// test for duplicate property names (SPEC R1: "Duplicate name values within a properties
// list ... are rejected at schema application time (INVALID_ARGUMENT)", post-provisioning
// — the API server accepts the CR because the CRD-level validation covers only maxLength
// and the Cypher-identifier regex). The Operator provisions the Cartographer and
// ApplySchema fails, surfacing via the ReconcileFailed status condition and a
// requeue-with-backoff error.
func TestReconcileDuplicatePropertyNameFailsPostProvisioning(t *testing.T) {
	s := scheme.Scheme
	_ = flowv1.AddToScheme(s)
	_ = appsv1.AddToScheme(s)
	_ = corev1.AddToScheme(s)
	_ = rbacv1.AddToScheme(s)

	ns := testNS
	fg := &flowv1.FoundryGraph{
		ObjectMeta: metav1.ObjectMeta{Name: defaultGraphName, Namespace: ns},
		Spec: flowv1.FoundryGraphSpec{
			EntityTypes: []flowv1.EntityTypeSpec{{
				Name: "Widget",
				Properties: []flowv1.PropertySpec{
					{Name: "sku", Type: "string"},
					{Name: "sku", Type: "string"}, // duplicate property name — must fail at ApplySchema, post-provisioning
				},
			}},
		},
	}

	// Operator-namespace signing secrets so reconcileSecrets succeeds.
	osign := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: operatorSigningKeySecretName, Namespace: "operator-ns"}, Data: map[string][]byte{"key": []byte("op"), "private-key": []byte("op-p")}}
	ssign := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: sidecarSigningKeySecretName, Namespace: "operator-ns"}, Data: map[string][]byte{"key": []byte("sd"), "private-key": []byte("sd-p")}}
	// A ready Deployment so waitForReadiness returns immediately and the reconcile
	// reaches the step-10 ApplySchema.
	replicas := int32(1)
	readyDeploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: cartographerSvcName, Namespace: ns},
		Spec:       appsv1.DeploymentSpec{Replicas: &replicas},
		Status:     appsv1.DeploymentStatus{AvailableReplicas: 1, ReadyReplicas: 1, UpdatedReplicas: 1},
	}

	applyCalled := false
	fakeCli := fake.NewClientBuilder().WithScheme(s).WithObjects(fg, osign, ssign, readyDeploy).WithStatusSubresource(fg).Build()
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
				applySchemaFn: func(context.Context, *flowv1gen.ApplySchemaRequest) (*flowv1gen.ApplySchemaResponse, error) {
					applyCalled = true
					// SPEC R1: duplicate property names are rejected at schema application time.
					return nil, status.Error(codes.InvalidArgument, "duplicate property name sku")
				},
			}, nil
		},
	}

	ctx := context.Background()
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: defaultGraphName, Namespace: ns}}); err == nil {
		t.Fatal("expected Reconcile to return an error (requeue with backoff) when ApplySchema rejects the duplicate property name")
	}

	// The duplicate property name must have reached the Cartographer — provisioning
	// happened first (post-provisioning failure mode), so ApplySchema was invoked.
	if !applyCalled {
		t.Error("expected ApplySchema to be invoked with the duplicate-property spec (post-provisioning failure mode)")
	}

	var got flowv1.FoundryGraph
	if err := fakeCli.Get(ctx, types.NamespacedName{Name: defaultGraphName, Namespace: ns}, &got); err != nil {
		t.Fatalf("get FoundryGraph: %v", err)
	}
	ready := meta.FindStatusCondition(got.Status.Conditions, "Ready")
	if ready == nil {
		t.Fatal("expected a Ready condition to be set for the invalid spec")
	}
	if ready.Status != metav1.ConditionFalse || ready.Reason != reasonReconcileFailed {
		t.Errorf("expected Ready=False/ReconcileFailed for the rejected schema, got %v", ready)
	}

	// Provisioning must have occurred before the failure.
	var pvc corev1.PersistentVolumeClaim
	if err := fakeCli.Get(ctx, types.NamespacedName{Name: "data-flow-graph", Namespace: ns}, &pvc); err != nil {
		t.Errorf("expected the PVC to be provisioned before the schema-apply failure, got: %v", err)
	}
}

// TestReconcileStep10DialFailureRequeues pins the SPEC R6 step-6 transient-error requeue
// classification for the step-10 applySchema dial failure: once the new pod has passed
// readiness, an unreachable Cartographer is an ordinary failure — unlike the schema-diff
// branch (applySchemaOnExisting), there is no missing-Deployment fall-through — so
// Reconcile must surface the error (setFailedCondition → Ready=False/ReconcileFailed) and
// controller-runtime re-queues with the step-5 exponential backoff.
func TestReconcileStep10DialFailureRequeues(t *testing.T) {
	s := scheme.Scheme
	_ = flowv1.AddToScheme(s)
	_ = appsv1.AddToScheme(s)
	_ = corev1.AddToScheme(s)
	_ = rbacv1.AddToScheme(s)

	ns := testNS
	fg := &flowv1.FoundryGraph{ObjectMeta: metav1.ObjectMeta{Name: defaultGraphName, Namespace: ns}}

	// Operator-namespace signing secrets so reconcileSecrets succeeds; no schema diff so the
	// reconcile skips applySchemaOnExisting and proceeds to the infra steps.
	replicas := int32(1)
	osign := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: operatorSigningKeySecretName, Namespace: "operator-ns"}, Data: map[string][]byte{"key": []byte("op"), "private-key": []byte("op-p")}}
	ssign := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: sidecarSigningKeySecretName, Namespace: "operator-ns"}, Data: map[string][]byte{"key": []byte("sd"), "private-key": []byte("sd-p")}}
	readyDeploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: cartographerSvcName, Namespace: ns},
		Spec:       appsv1.DeploymentSpec{Replicas: &replicas},
		Status:     appsv1.DeploymentStatus{AvailableReplicas: 1, ReadyReplicas: 1, UpdatedReplicas: 1},
	}

	fakeClient := fake.NewClientBuilder().WithScheme(s).WithObjects(fg, osign, ssign, readyDeploy).WithStatusSubresource(fg).Build()
	ctx := context.Background()
	nn := types.NamespacedName{Name: defaultGraphName, Namespace: ns}

	// The step-10 dial fails (the newly-ready pod cannot be reached).
	r := &FoundryGraphReconciler{
		Client:            fakeClient,
		Scheme:            s,
		OperatorNamespace: "operator-ns",
		CartographerPort:  50051,
		CartographerImage: "cartographer:latest",
		ReadinessTimeout:  time.Second,
		ProxyRoutingTable: NewProxyRoutingTable(),
		CartographerDialer: func(ctx context.Context, endpoint string) (CartographerClient, error) {
			return nil, errors.New("dial failed: new pod unreachable")
		},
	}
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: nn}); err == nil {
		t.Fatal("expected Reconcile to return an error when the step-10 dial fails (requeue with backoff)")
	}

	var got flowv1.FoundryGraph
	if err := fakeClient.Get(ctx, nn, &got); err != nil {
		t.Fatalf("get FoundryGraph: %v", err)
	}
	ready := meta.FindStatusCondition(got.Status.Conditions, "Ready")
	if ready == nil || ready.Status != metav1.ConditionFalse || ready.Reason != reasonReconcileFailed {
		t.Errorf("expected Ready=False/ReconcileFailed after step-10 dial failure, got %v", ready)
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

	ns := testNS
	fg := &flowv1.FoundryGraph{ObjectMeta: metav1.ObjectMeta{Name: defaultGraphName, Namespace: ns}}

	// Operator-namespace signing secrets so reconcileSecrets succeeds; no schema diff so the
	// reconcile skips applySchemaOnExisting and proceeds to the infra steps.
	replicas := int32(1)
	osign := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: operatorSigningKeySecretName, Namespace: "operator-ns"}, Data: map[string][]byte{"key": []byte("op"), "private-key": []byte("op-p")}}
	ssign := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: sidecarSigningKeySecretName, Namespace: "operator-ns"}, Data: map[string][]byte{"key": []byte("sd"), "private-key": []byte("sd-p")}}
	readyDeploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: cartographerSvcName, Namespace: ns},
		Spec:       appsv1.DeploymentSpec{Replicas: &replicas},
		Status:     appsv1.DeploymentStatus{AvailableReplicas: 1, ReadyReplicas: 1, UpdatedReplicas: 1},
	}

	fakeClient := fake.NewClientBuilder().WithScheme(s).WithObjects(fg, osign, ssign, readyDeploy).WithStatusSubresource(fg).Build()
	ctx := context.Background()
	nn := types.NamespacedName{Name: defaultGraphName, Namespace: ns}

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
	if ready == nil || ready.Status != metav1.ConditionFalse || ready.Reason != reasonReconcileFailed {
		t.Errorf("expected Ready=False/ReconcileFailed after step-10 ApplySchema failure, got %v", ready)
	}
}
