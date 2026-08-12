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
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
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
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	ctrl "sigs.k8s.io/controller-runtime"

	flowv1gen "github.com/foundry/flow/gen/flow/v1"
	flowv1 "github.com/foundry/flow/operator/api/v1"
)

// Shared test constants for the controller package's foundrygraph tests (goconst: the
// repeated literals are hoisted to a single source of truth).
const (
	testNS              = "test-ns"
	cartographerSvcName = "cartographer-flow-graph"
	operatorTestNS      = "operator-system"
	graphTestNS         = "graph-ns"
	rpcApply            = "apply"
	rpcHealth           = "health"
	rpcWipe             = "wipe"
	// keyReaderRoleName is the verification-key Role/RoleBinding name rendered for the
	// conventional flow-graph name ("cartographer-<fg-name>-key-reader").
	keyReaderRoleName = "cartographer-flow-graph-key-reader"
	// remoteAuthRoleName is the remote-auth Role/RoleBinding name rendered for the
	// conventional flow-graph name ("cartographer-<fg-name>-remote-auth").
	remoteAuthRoleName = "cartographer-flow-graph-remote-auth"
	// transactionTimeoutDefault is the SPEC R5 TRANSACTION_TIMEOUT fallback ("30m").
	transactionTimeoutDefault = "30m"
)

func TestFoundryGraphReconciler_CartographerServiceName(t *testing.T) {
	r := &FoundryGraphReconciler{}
	fg := &flowv1.FoundryGraph{}
	fg.Name = defaultGraphName
	fg.Namespace = testNS

	name := r.cartographerServiceName(fg)
	if name != cartographerSvcName {
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
	fg.Name = defaultGraphName
	fg.Namespace = testNS

	r.registerProxyRoute(fg)

	endpoint, ok := rt.Lookup(testNS, defaultGraphName)
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

	ns := testNS
	fg := &flowv1.FoundryGraph{
		ObjectMeta: metav1.ObjectMeta{
			Name:      defaultGraphName,
			Namespace: ns,
		},
	}

	// Seed every resource class tearDown deletes (SPEC R6 deletion flow: Deployment,
	// Service, ServiceAccount, both Roles, both RoleBindings, and the PVC) so each
	// deletion branch executes its real delete path rather than only the IsNotFound path.
	deploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      cartographerSvcName,
			Namespace: ns,
		},
	}
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      cartographerSvcName,
			Namespace: ns,
		},
	}
	sa := &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      cartographerSvcName,
			Namespace: ns,
		},
	}
	keyRole := &rbacv1.Role{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "cartographer-flow-graph-key-reader",
			Namespace: ns,
		},
	}
	keyRB := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "cartographer-flow-graph-key-reader",
			Namespace: ns,
		},
	}
	remoteRole := &rbacv1.Role{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "cartographer-flow-graph-remote-auth",
			Namespace: ns,
		},
	}
	remoteRB := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "cartographer-flow-graph-remote-auth",
			Namespace: ns,
		},
	}
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "data-flow-graph",
			Namespace: ns,
		},
	}

	fakeCli := fake.NewClientBuilder().WithScheme(s).WithObjects(deploy, svc, sa, keyRole, keyRB, remoteRole, remoteRB, pvc).Build()
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

	// Every seeded resource class must be deleted.
	checks := []struct {
		name string
		obj  client.Object
	}{
		{"Deployment", deploy},
		{"Service", svc},
		{"ServiceAccount", sa},
		{"key-reader Role", keyRole},
		{"key-reader RoleBinding", keyRB},
		{"remote-auth Role", remoteRole},
		{"remote-auth RoleBinding", remoteRB},
		{"PersistentVolumeClaim", pvc},
	}
	for _, c := range checks {
		key := client.ObjectKeyFromObject(c.obj)
		obj := c.obj.DeepCopyObject().(client.Object)
		if err := fakeCli.Get(ctx, key, obj); err == nil {
			t.Errorf("expected %s to be deleted", c.name)
		} else if !apierrors.IsNotFound(err) {
			t.Errorf("get %s after tearDown: %v", c.name, err)
		}
	}

	if _, ok := rt.Lookup(testNS, defaultGraphName); ok {
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
			Name:              defaultGraphName,
			Namespace:         testNS,
			DeletionTimestamp: &now,
			Finalizers:        []string{finalizerName},
		},
	}
	// A deployment exists so the tear-down actually removes something.
	deploy := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: cartographerSvcName, Namespace: testNS}}

	fakeCli := fake.NewClientBuilder().WithScheme(s).WithObjects(fg, deploy).WithStatusSubresource(fg).Build()
	r := &FoundryGraphReconciler{
		Client:            fakeCli,
		Scheme:            s,
		ProxyRoutingTable: NewProxyRoutingTable(),
	}

	ctx := context.Background()
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: defaultGraphName, Namespace: testNS}}); err != nil {
		t.Fatalf("Reconcile on deletion returned error: %v", err)
	}

	// Tear-down must have deleted the Deployment.
	var d appsv1.Deployment
	if err := fakeCli.Get(ctx, types.NamespacedName{Name: cartographerSvcName, Namespace: testNS}, &d); err == nil {
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

// fullSuccessDialer always returns a healthy mock client for the post-readiness
// ApplySchema at Reconcile step 10.
func fullSuccessDialer(ctx context.Context, endpoint string) (CartographerClient, error) {
	return &mockCartographerClient{}, nil
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

// TestUpdateStatusPopulatesStorageSize verifies updateStatus publishes the PVC's actual
// capacity as status.storageSize (SPEC R6 step 7: "status.storageSize" reflects the PVC's
// status.capacity.storage). The reconciler's earlier happy-path tests never provision a PVC
// with status.capacity, so the storageSize write path (controller.go) is otherwise untested.
func TestUpdateStatusPopulatesStorageSize(t *testing.T) {
	s := scheme.Scheme
	_ = flowv1.AddToScheme(s)
	_ = corev1.AddToScheme(s)

	ns := testNS
	cap := resource.MustParse("5Gi")
	// Seed a PVC with a bound capacity so updateStatus's storageSize read sees a real
	// allocation diverging from any spec default.
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "data-flow-graph", Namespace: ns},
		Status: corev1.PersistentVolumeClaimStatus{
			Capacity: corev1.ResourceList{corev1.ResourceStorage: cap},
		},
	}
	fg := &flowv1.FoundryGraph{ObjectMeta: metav1.ObjectMeta{Name: defaultGraphName, Namespace: ns}}
	fakeCli := fake.NewClientBuilder().WithScheme(s).WithObjects(fg, pvc).WithStatusSubresource(fg).Build()
	r := &FoundryGraphReconciler{Client: fakeCli, Scheme: s, CartographerPort: 50051}

	ctx := context.Background()
	if err := r.updateStatus(ctx, fg, &flowv1.FoundryGraphSpec{}); err != nil {
		t.Fatalf("updateStatus: %v", err)
	}

	var got flowv1.FoundryGraph
	if err := fakeCli.Get(ctx, types.NamespacedName{Name: defaultGraphName, Namespace: ns}, &got); err != nil {
		t.Fatalf("get FoundryGraph: %v", err)
	}
	if got.Status.StorageSize == nil {
		t.Fatal("expected status.storageSize to be populated from the PVC's status.capacity.storage")
	}
	if got.Status.StorageSize.Value() != cap.Value() {
		t.Errorf("expected status.storageSize=%v, got %v", cap.Value(), got.Status.StorageSize.Value())
	}
}

// TestReadinessRateLimiterPinsSpecParameters pins the SPEC R6 step-5 backoff parameters
// (exponential backoff with "initial delay ~5s, doubling per attempt, capped at 5m"):
// the first failure waits 5s, each subsequent failure doubles the wait, and the wait is
// capped at 5m. This is the rate limiter the FoundryGraph controller is configured with
// in SetupWithManager, replacing controller-runtime's default (5ms initial, 1000s cap).
func TestReadinessRateLimiterPinsSpecParameters(t *testing.T) {
	limiter := readinessRateLimiter()
	req := reconcile.Request{NamespacedName: types.NamespacedName{Name: defaultGraphName, Namespace: testNS}}

	// Initial delay ~5s.
	if got := limiter.When(req); got != 5*time.Second {
		t.Errorf("expected initial backoff of 5s (SPEC R6 step 5), got %v", got)
	}
	// Doubling per attempt: 10s, then 20s, then 40s.
	for _, want := range []time.Duration{10 * time.Second, 20 * time.Second, 40 * time.Second} {
		if got := limiter.When(req); got != want {
			t.Errorf("expected backoff %v (doubling per attempt), got %v", want, got)
		}
	}
	// Capped at 5m: keep doubling (80s, 160s, 320s → 5m20s) until the cap holds, then
	// assert every further attempt waits exactly 5m — the SPEC R6 step-5 cap, not
	// controller-runtime's 1000s default.
	got := limiter.When(req)
	for got < 5*time.Minute {
		want := 2 * got
		got = limiter.When(req)
		if got > 5*time.Minute {
			t.Errorf("expected backoff capped at 5m (SPEC R6 step 5), got %v", got)
			break
		}
		if want < 5*time.Minute && got != want {
			t.Errorf("expected backoff %v (doubling per attempt), got %v", want, got)
		}
	}
	for range 20 {
		if got := limiter.When(req); got != 5*time.Minute {
			t.Errorf("expected backoff capped at 5m (SPEC R6 step 5), got %v", got)
		}
	}
	// A distinct item must restart from the 5s base delay (per-item limiting).
	other := reconcile.Request{NamespacedName: types.NamespacedName{Name: "other-graph", Namespace: testNS}}
	if got := limiter.When(other); got != 5*time.Second {
		t.Errorf("expected a new item to start at the 5s base delay, got %v", got)
	}
}

// TestUpdateStatusPvcGetErrors distinguishes the two PVC Get outcomes in updateStatus
// (item 1): an IsNotFound (no PVC provisioned yet) leaves storageSize absent while reconcile
// still succeeds; any other Get error (RBAC/apiserver/transient) must surface to the requeue
// path instead of being silently swallowed.
func TestUpdateStatusPvcGetErrors(t *testing.T) {
	s := scheme.Scheme
	_ = flowv1.AddToScheme(s)
	_ = corev1.AddToScheme(s)
	ctx := context.Background()
	ns := testNS

	t.Run("pvc not found leaves storageSize absent", func(t *testing.T) {
		fg := &flowv1.FoundryGraph{ObjectMeta: metav1.ObjectMeta{Name: defaultGraphName, Namespace: ns}}
		fakeCli := fake.NewClientBuilder().WithScheme(s).WithObjects(fg).WithStatusSubresource(fg).Build()
		r := &FoundryGraphReconciler{Client: fakeCli, Scheme: s, CartographerPort: 50051}

		if err := r.updateStatus(ctx, fg, &flowv1.FoundryGraphSpec{}); err != nil {
			t.Fatalf("updateStatus must succeed when the PVC is not found, got: %v", err)
		}
		var got flowv1.FoundryGraph
		if err := fakeCli.Get(ctx, types.NamespacedName{Name: defaultGraphName, Namespace: ns}, &got); err != nil {
			t.Fatalf("get FoundryGraph: %v", err)
		}
		if got.Status.StorageSize != nil {
			t.Error("expected status.storageSize to remain absent when the PVC is not found")
		}
	})

	t.Run("pvc get error surfaces to the caller", func(t *testing.T) {
		fg := &flowv1.FoundryGraph{ObjectMeta: metav1.ObjectMeta{Name: defaultGraphName, Namespace: ns}}
		interceptorFuncs := interceptor.Funcs{
			Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
				if _, ok := obj.(*corev1.PersistentVolumeClaim); ok {
					return errors.New("apiserver unavailable")
				}
				return nil
			},
		}
		fakeCli := fake.NewClientBuilder().WithScheme(s).WithObjects(fg).WithStatusSubresource(fg).WithInterceptorFuncs(interceptorFuncs).Build()
		r := &FoundryGraphReconciler{Client: fakeCli, Scheme: s, CartographerPort: 50051}

		if err := r.updateStatus(ctx, fg, &flowv1.FoundryGraphSpec{}); err == nil {
			t.Fatal("expected updateStatus to surface the PVC Get error, not swallow it")
		} else if !strings.Contains(err.Error(), "read pvc") {
			t.Errorf("expected the PVC read error to be surfaced with context, got: %v", err)
		}
	})
}

// TestUpdateStatusUpdateAndStatusErrors covers the two remaining updateStatus failure
// branches (reconcile step 7): the main r.Update error (the last-applied-spec annotation
// write) and the r.Status().Update error (the endpoint/storageSize status-block write).
// The sibling Get-error branches are pinned by TestUpdateStatusPvcGetErrors.
func TestUpdateStatusUpdateAndStatusErrors(t *testing.T) {
	s := scheme.Scheme
	_ = flowv1.AddToScheme(s)
	_ = corev1.AddToScheme(s)
	ctx := context.Background()
	ns := testNS

	t.Run("main update error surfaces to the caller", func(t *testing.T) {
		fg := &flowv1.FoundryGraph{ObjectMeta: metav1.ObjectMeta{Name: defaultGraphName, Namespace: ns}}
		interceptorFuncs := interceptor.Funcs{
			Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
				if _, ok := obj.(*flowv1.FoundryGraph); ok {
					return errors.New("apiserver unavailable")
				}
				return nil
			},
		}
		fakeCli := fake.NewClientBuilder().WithScheme(s).WithObjects(fg).WithStatusSubresource(fg).WithInterceptorFuncs(interceptorFuncs).Build()
		r := &FoundryGraphReconciler{Client: fakeCli, Scheme: s, CartographerPort: 50051}

		if err := r.updateStatus(ctx, fg, &flowv1.FoundryGraphSpec{}); err == nil {
			t.Fatal("expected updateStatus to surface the main Update (annotation write) error")
		} else if !strings.Contains(err.Error(), "update FoundryGraph") {
			t.Errorf("expected the main Update error to be surfaced with context, got: %v", err)
		}
	})

	t.Run("status update error surfaces to the caller", func(t *testing.T) {
		fg := &flowv1.FoundryGraph{ObjectMeta: metav1.ObjectMeta{Name: defaultGraphName, Namespace: ns}}
		interceptorFuncs := interceptor.Funcs{
			SubResourceUpdate: func(ctx context.Context, c client.Client, subResourceName string, obj client.Object, opts ...client.SubResourceUpdateOption) error {
				return errors.New("apiserver unavailable")
			},
		}
		fakeCli := fake.NewClientBuilder().WithScheme(s).WithObjects(fg).WithStatusSubresource(fg).WithInterceptorFuncs(interceptorFuncs).Build()
		r := &FoundryGraphReconciler{Client: fakeCli, Scheme: s, CartographerPort: 50051}

		if err := r.updateStatus(ctx, fg, &flowv1.FoundryGraphSpec{}); err == nil {
			t.Fatal("expected updateStatus to surface the Status().Update (status block) error")
		} else if !strings.Contains(err.Error(), "update FoundryGraph status") {
			t.Errorf("expected the Status().Update error to be surfaced with context, got: %v", err)
		}
	})
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

// resourcePtr returns a pointer to a resource.Quantity parsed from the given string.
func resourcePtr(s string) *resource.Quantity {
	q := resource.MustParse(s)
	return &q
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
		ObjectMeta: metav1.ObjectMeta{Name: cartographerSvcName, Namespace: testNS},
		Spec:       appsv1.DeploymentSpec{Replicas: &replicas},
		Status:     appsv1.DeploymentStatus{ReadyReplicas: 0},
	}
	fakeCli := fake.NewClientBuilder().WithScheme(s).WithObjects(notReady).Build()
	r := &FoundryGraphReconciler{Client: fakeCli, ReadinessTimeout: 100 * time.Millisecond}

	fg := &flowv1.FoundryGraph{ObjectMeta: metav1.ObjectMeta{Name: defaultGraphName, Namespace: testNS}}
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
		ObjectMeta: metav1.ObjectMeta{Name: cartographerSvcName, Namespace: testNS},
		Spec:       appsv1.DeploymentSpec{Replicas: &replicas},
		Status:     appsv1.DeploymentStatus{ReadyReplicas: 0},
	}
	fakeCli := fake.NewClientBuilder().WithScheme(s).WithObjects(notReady).Build()
	// A context that is cancelled before the poll's 5s sleep elapses → ctx.Done fires.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r := &FoundryGraphReconciler{Client: fakeCli, ReadinessTimeout: time.Minute}
	fg := &flowv1.FoundryGraph{ObjectMeta: metav1.ObjectMeta{Name: defaultGraphName, Namespace: testNS}}
	err := r.waitForReadiness(ctx, fg)
	if err == nil {
		t.Fatal("expected a context-cancellation error")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled, got %v", err)
	}
}

// TestAllReplicasReady asserts allReplicasReady's guard branches: nil replicas, zero
// replicas, partial readiness, and ready-but-not-updated (the old ReplicaSet still
// serving during a rollout) all report not-ready; equal readiness AND updated-replicas
// reports ready. The UpdatedReplicas requirement is what guarantees the ready pod is the
// NEW pod before the schema re-apply dials the Service (SPEC R6).
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
	// The old pod is ready but the new pod (matching the updated template) is not yet
	// running: ReadyReplicas alone must NOT satisfy the check.
	if allReplicasReady(&appsv1.Deployment{Spec: appsv1.DeploymentSpec{Replicas: &one}, Status: appsv1.DeploymentStatus{ReadyReplicas: 1, UpdatedReplicas: 0}}) {
		t.Error("expected a ready-but-not-updated replica (old ReplicaSet) to be not ready")
	}
	if !allReplicasReady(&appsv1.Deployment{Spec: appsv1.DeploymentSpec{Replicas: &one}, Status: appsv1.DeploymentStatus{ReadyReplicas: 1, UpdatedReplicas: 1}}) {
		t.Error("expected all-replicas-ready with the current template to be ready")
	}
}

// TestWaitForReadinessRequiresUpdatedReplicas pins the SPEC R6 "new pod passes its
// readiness probe" guarantee (item 3): during a spec-change rollout the old ReplicaSet
// keeps ReadyReplicas>=1 while the new pod is still starting, so a readiness check based
// on ReadyReplicas alone returns immediately and the step-10 ApplySchema dials the
// ClusterIP Service, which may still serve the old pod. waitForReadiness must therefore
// require the Deployment's UpdatedReplicas to cover every desired replica — only then is
// the ready pod the NEW one.
func TestWaitForReadinessRequiresUpdatedReplicas(t *testing.T) {
	s := scheme.Scheme
	_ = appsv1.AddToScheme(s)

	t.Run("old replicaset ready but new pod not updated times out", func(t *testing.T) {
		replicas := int32(1)
		rolling := &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Name: cartographerSvcName, Namespace: testNS},
			Spec:       appsv1.DeploymentSpec{Replicas: &replicas},
			// The old pod is ready (ReadyReplicas=1) but the new pod matching the
			// updated template is not running yet (UpdatedReplicas=0).
			Status: appsv1.DeploymentStatus{ReadyReplicas: 1, UpdatedReplicas: 0},
		}
		fakeCli := fake.NewClientBuilder().WithScheme(s).WithObjects(rolling).Build()
		r := &FoundryGraphReconciler{Client: fakeCli, ReadinessTimeout: 100 * time.Millisecond}
		fg := &flowv1.FoundryGraph{ObjectMeta: metav1.ObjectMeta{Name: defaultGraphName, Namespace: testNS}}
		if err := r.waitForReadiness(context.Background(), fg); err == nil {
			t.Fatal("expected readiness to wait for the new (updated) pod, got immediate success")
		}
	})

	t.Run("succeeds once the new pod is updated and ready", func(t *testing.T) {
		// The Deployment starts in the rolling state (first Get: not ready); the second
		// Get reflects the new pod having passed readiness (UpdatedReplicas=1,
		// ReadyReplicas=1).
		calls := 0
		interceptorFuncs := interceptor.Funcs{
			Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
				calls++
				if calls == 1 {
					return nil // zero-value Deployment: not ready, keep polling
				}
				if d, ok := obj.(*appsv1.Deployment); ok {
					replicas := int32(1)
					d.Spec.Replicas = &replicas
					d.Status = appsv1.DeploymentStatus{ReadyReplicas: 1, UpdatedReplicas: 1}
				}
				return nil
			},
		}
		fakeCli := fake.NewClientBuilder().WithScheme(s).WithInterceptorFuncs(interceptorFuncs).Build()
		r := &FoundryGraphReconciler{Client: fakeCli, ReadinessTimeout: 2 * time.Second}
		fg := &flowv1.FoundryGraph{ObjectMeta: metav1.ObjectMeta{Name: defaultGraphName, Namespace: testNS}}
		if err := r.waitForReadiness(context.Background(), fg); err != nil {
			t.Fatalf("expected readiness once the new pod is updated and ready, got: %v", err)
		}
		if calls < 2 {
			t.Errorf("expected the poll to continue until the new pod is updated, got %d Get calls", calls)
		}
	})
}

// TestWaitForReadinessTransientGetErrorKeepsPolling covers the transient Deployment-Get
// error branch of waitForReadiness (item 8): a momentary Get failure (e.g. the
// Deployment not yet visible after CreateOrUpdate) must not short-circuit the poll — the
// loop keeps polling and succeeds once the Deployment is visible, updated, and ready.
func TestWaitForReadinessTransientGetErrorKeepsPolling(t *testing.T) {
	s := scheme.Scheme
	_ = appsv1.AddToScheme(s)

	calls := 0
	interceptorFuncs := interceptor.Funcs{
		Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			calls++
			if calls == 1 {
				return errors.New("transient: deployment not yet visible")
			}
			if d, ok := obj.(*appsv1.Deployment); ok {
				replicas := int32(1)
				d.Spec.Replicas = &replicas
				d.Status = appsv1.DeploymentStatus{ReadyReplicas: 1, UpdatedReplicas: 1}
			}
			return nil
		},
	}
	fakeCli := fake.NewClientBuilder().WithScheme(s).WithInterceptorFuncs(interceptorFuncs).Build()
	r := &FoundryGraphReconciler{Client: fakeCli, ReadinessTimeout: 2 * time.Second}
	fg := &flowv1.FoundryGraph{ObjectMeta: metav1.ObjectMeta{Name: defaultGraphName, Namespace: testNS}}
	if err := r.waitForReadiness(context.Background(), fg); err != nil {
		t.Fatalf("expected a transient Get error to keep polling until ready, got: %v", err)
	}
	if calls < 2 {
		t.Errorf("expected the poll to continue past the transient Get error, got %d Get calls", calls)
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
	return nil, errors.New("mock CartographerClient.ExportGraph not configured")
}

func (m *mockCartographerClient) Close() error {
	if m.closeFn != nil {
		return m.closeFn()
	}
	return nil
}
