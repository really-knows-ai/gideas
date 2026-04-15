package controller

import (
	"context"
	"testing"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	federationv1 "github.com/gideas/flow/federation/api/v1"
)

const (
	testNamespace     = "test-ns"
	reasonInvalidSpec = "InvalidSpec"
)

// newTestReconciler creates a FederationMemberReconciler backed by a fake
// K8s client pre-loaded with the given objects.
func newTestReconciler(t *testing.T, objs ...client.Object) (*FederationMemberReconciler, client.Client) {
	t.Helper()

	scheme := federationv1.NewTestScheme()
	builder := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&federationv1.FederationMember{})
	if len(objs) > 0 {
		builder = builder.WithObjects(objs...)
	}
	k8sClient := builder.Build()

	r := &FederationMemberReconciler{
		Client: k8sClient,
		Scheme: scheme,
	}
	return r, k8sClient
}

// mustReconcile calls Reconcile for the given name in testNamespace.
func mustReconcile(t *testing.T, r *FederationMemberReconciler, name string) ctrl.Result {
	t.Helper()
	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: testNamespace, Name: name},
	})
	if err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}
	return result
}

// fetchMember retrieves the FederationMember after reconciliation for assertions.
func fetchMember(t *testing.T, k8sClient client.Client, name string) *federationv1.FederationMember {
	t.Helper()
	key := types.NamespacedName{Namespace: testNamespace, Name: name}
	var member federationv1.FederationMember
	if err := k8sClient.Get(context.Background(), key, &member); err != nil {
		t.Fatalf("failed to fetch FederationMember %s/%s: %v", testNamespace, name, err)
	}
	return &member
}

// readyCondition returns the Ready condition from a FederationMember, or nil.
func readyCondition(member *federationv1.FederationMember) *metav1.Condition {
	return meta.FindStatusCondition(member.Status.Conditions, "Ready")
}

// --- Tests ---

func TestValidMember_ReadyTrue(t *testing.T) {
	member := &federationv1.FederationMember{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "valid-member",
			Namespace: testNamespace,
		},
		Spec: federationv1.FederationMemberSpec{
			FlowIdentity:    "flow-alpha",
			EmbassyEndpoint: "flow-alpha-embassy:50059",
			StateRefs:       []string{"state-qld"},
			PublisherRoles: []federationv1.PublisherRoleSpec{
				{Scope: "education", Level: "state"},
			},
		},
	}

	r, k8sClient := newTestReconciler(t, member)
	mustReconcile(t, r, "valid-member")

	got := fetchMember(t, k8sClient, "valid-member")
	cond := readyCondition(got)
	if cond == nil {
		t.Fatal("expected Ready condition, got nil")
	}
	if cond.Status != metav1.ConditionTrue {
		t.Errorf("Ready condition status = %q, want %q", cond.Status, metav1.ConditionTrue)
	}
	if cond.Reason != "Valid" {
		t.Errorf("Ready condition reason = %q, want %q", cond.Reason, "Valid")
	}
}

func TestMissingFlowIdentity_ReadyFalse(t *testing.T) {
	member := &federationv1.FederationMember{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "no-identity",
			Namespace: testNamespace,
		},
		Spec: federationv1.FederationMemberSpec{
			FlowIdentity:    "", // missing
			EmbassyEndpoint: "flow-embassy:50059",
		},
	}

	r, k8sClient := newTestReconciler(t, member)
	mustReconcile(t, r, "no-identity")

	got := fetchMember(t, k8sClient, "no-identity")
	cond := readyCondition(got)
	if cond == nil {
		t.Fatal("expected Ready condition, got nil")
	}
	if cond.Status != metav1.ConditionFalse {
		t.Errorf("Ready condition status = %q, want %q", cond.Status, metav1.ConditionFalse)
	}
	if cond.Reason != reasonInvalidSpec {
		t.Errorf("Ready condition reason = %q, want %q", cond.Reason, reasonInvalidSpec)
	}
}

func TestMissingEmbassyEndpoint_ReadyFalse(t *testing.T) {
	member := &federationv1.FederationMember{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "no-endpoint",
			Namespace: testNamespace,
		},
		Spec: federationv1.FederationMemberSpec{
			FlowIdentity:    "flow-alpha",
			EmbassyEndpoint: "", // missing
		},
	}

	r, k8sClient := newTestReconciler(t, member)
	mustReconcile(t, r, "no-endpoint")

	got := fetchMember(t, k8sClient, "no-endpoint")
	cond := readyCondition(got)
	if cond == nil {
		t.Fatal("expected Ready condition, got nil")
	}
	if cond.Status != metav1.ConditionFalse {
		t.Errorf("Ready condition status = %q, want %q", cond.Status, metav1.ConditionFalse)
	}
	if cond.Reason != reasonInvalidSpec {
		t.Errorf("Ready condition reason = %q, want %q", cond.Reason, reasonInvalidSpec)
	}
}

func TestInvalidPublisherRoleLevel_ReadyFalse(t *testing.T) {
	member := &federationv1.FederationMember{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "bad-level",
			Namespace: testNamespace,
		},
		Spec: federationv1.FederationMemberSpec{
			FlowIdentity:    "flow-alpha",
			EmbassyEndpoint: "flow-alpha-embassy:50059",
			PublisherRoles: []federationv1.PublisherRoleSpec{
				{Scope: "education", Level: "galaxy"}, // invalid
			},
		},
	}

	r, k8sClient := newTestReconciler(t, member)
	mustReconcile(t, r, "bad-level")

	got := fetchMember(t, k8sClient, "bad-level")
	cond := readyCondition(got)
	if cond == nil {
		t.Fatal("expected Ready condition, got nil")
	}
	if cond.Status != metav1.ConditionFalse {
		t.Errorf("Ready condition status = %q, want %q", cond.Status, metav1.ConditionFalse)
	}
	if cond.Reason != reasonInvalidSpec {
		t.Errorf("Ready condition reason = %q, want %q", cond.Reason, reasonInvalidSpec)
	}
}

func TestJoinedAt_SetOnFirstReconcile(t *testing.T) {
	member := &federationv1.FederationMember{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "join-time",
			Namespace: testNamespace,
		},
		Spec: federationv1.FederationMemberSpec{
			FlowIdentity:    "flow-alpha",
			EmbassyEndpoint: "flow-alpha-embassy:50059",
		},
	}

	r, k8sClient := newTestReconciler(t, member)
	mustReconcile(t, r, "join-time")

	got := fetchMember(t, k8sClient, "join-time")
	if got.Status.JoinedAt == nil {
		t.Fatal("expected JoinedAt to be set on first reconcile, got nil")
	}
	if got.Status.JoinedAt.IsZero() {
		t.Error("JoinedAt is zero time")
	}
}

func TestJoinedAt_PreservedOnSubsequentReconcile(t *testing.T) {
	member := &federationv1.FederationMember{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "preserve-join",
			Namespace: testNamespace,
		},
		Spec: federationv1.FederationMemberSpec{
			FlowIdentity:    "flow-alpha",
			EmbassyEndpoint: "flow-alpha-embassy:50059",
		},
	}

	r, k8sClient := newTestReconciler(t, member)

	// First reconcile -- sets JoinedAt.
	mustReconcile(t, r, "preserve-join")
	first := fetchMember(t, k8sClient, "preserve-join")
	if first.Status.JoinedAt == nil {
		t.Fatal("expected JoinedAt to be set on first reconcile")
	}
	firstTime := first.Status.JoinedAt.Time

	// Second reconcile -- JoinedAt must be preserved.
	mustReconcile(t, r, "preserve-join")
	second := fetchMember(t, k8sClient, "preserve-join")
	if second.Status.JoinedAt == nil {
		t.Fatal("JoinedAt is nil after second reconcile")
	}
	if !second.Status.JoinedAt.Time.Equal(firstTime) {
		t.Errorf("JoinedAt changed: first=%v, second=%v", firstTime, second.Status.JoinedAt.Time)
	}
}

func TestDeletedMember_NoError(t *testing.T) {
	// Reconcile a member that doesn't exist -- should be a no-op.
	r, _ := newTestReconciler(t)
	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: testNamespace, Name: "gone"},
	})
	if err != nil {
		t.Fatalf("Reconcile returned error for deleted member: %v", err)
	}
	if result.RequeueAfter != 0 {
		t.Error("expected no requeue for deleted member")
	}
}

func TestEmptyPublisherRoleScope_ReadyFalse(t *testing.T) {
	member := &federationv1.FederationMember{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "empty-scope",
			Namespace: testNamespace,
		},
		Spec: federationv1.FederationMemberSpec{
			FlowIdentity:    "flow-alpha",
			EmbassyEndpoint: "flow-alpha-embassy:50059",
			PublisherRoles: []federationv1.PublisherRoleSpec{
				{Scope: "", Level: "state"}, // empty scope
			},
		},
	}

	r, k8sClient := newTestReconciler(t, member)
	mustReconcile(t, r, "empty-scope")

	got := fetchMember(t, k8sClient, "empty-scope")
	cond := readyCondition(got)
	if cond == nil {
		t.Fatal("expected Ready condition, got nil")
	}
	if cond.Status != metav1.ConditionFalse {
		t.Errorf("Ready condition status = %q, want %q", cond.Status, metav1.ConditionFalse)
	}
}

func TestValidMemberNoRolesNoStates_ReadyTrue(t *testing.T) {
	// A member with no publisher roles and no state refs is valid.
	member := &federationv1.FederationMember{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "minimal-member",
			Namespace: testNamespace,
		},
		Spec: federationv1.FederationMemberSpec{
			FlowIdentity:    "flow-gamma",
			EmbassyEndpoint: "flow-gamma-embassy:50059",
		},
	}

	r, k8sClient := newTestReconciler(t, member)
	mustReconcile(t, r, "minimal-member")

	got := fetchMember(t, k8sClient, "minimal-member")
	cond := readyCondition(got)
	if cond == nil {
		t.Fatal("expected Ready condition, got nil")
	}
	if cond.Status != metav1.ConditionTrue {
		t.Errorf("Ready condition status = %q, want %q", cond.Status, metav1.ConditionTrue)
	}
}
