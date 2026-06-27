package controller

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/go-logr/logr"
	"google.golang.org/grpc"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	flowv1gen "github.com/gideas/flow/gen/flow/v1"
	flowv1 "github.com/gideas/flow/operator/api/v1"
)

// mockLibrarianClient implements flowv1gen.LibrarianServiceClient for testing.
// It embeds unimplemented server stubs and overrides only the methods needed
// for LawGroup controller tests.  All other methods return unimplemented errors,
// which is fine since the controller never calls them.
type mockLibrarianClient struct {
	mu            sync.Mutex
	SyncedGroups  []string // names of groups that were synced
	DeletedGroups []string // names of groups that were deleted
	SyncError     error    // error to return from SyncLawGroup
	DeleteError   error    // error to return from DeleteLawGroup
}

// SyncLawGroup satisfies the client interface by recording the call.
func (m *mockLibrarianClient) SyncLawGroup(_ context.Context, req *flowv1gen.SyncLawGroupRequest, _ ...grpc.CallOption) (*flowv1gen.SyncLawGroupResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.SyncError != nil {
		return nil, m.SyncError
	}
	name := req.GetGroup().GetName()
	m.SyncedGroups = append(m.SyncedGroups, name)
	return &flowv1gen.SyncLawGroupResponse{Acknowledged: true}, nil
}

// DeleteLawGroup satisfies the client interface by recording the call.
func (m *mockLibrarianClient) DeleteLawGroup(_ context.Context, req *flowv1gen.DeleteLawGroupRequest, _ ...grpc.CallOption) (*flowv1gen.DeleteLawGroupResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.DeleteError != nil {
		return nil, m.DeleteError
	}
	m.DeletedGroups = append(m.DeletedGroups, req.GetGroupName())
	return &flowv1gen.DeleteLawGroupResponse{Acknowledged: true}, nil
}

// Remaining methods satisfy the rest of the LibrarianServiceClient interface.
// They should never be called by the LawGroup controller.

func (m *mockLibrarianClient) QueryLaws(_ context.Context, _ *flowv1gen.QueryLawsRequest, _ ...grpc.CallOption) (*flowv1gen.QueryLawsResponse, error) {
	return nil, fmt.Errorf("unexpected call: QueryLaws")
}

func (m *mockLibrarianClient) Cite(_ context.Context, _ *flowv1gen.CiteRequest, _ ...grpc.CallOption) (*flowv1gen.CiteResponse, error) {
	return nil, fmt.Errorf("unexpected call: Cite")
}

func (m *mockLibrarianClient) RecordFinding(_ context.Context, _ *flowv1gen.RecordFindingRequest, _ ...grpc.CallOption) (*flowv1gen.RecordFindingResponse, error) {
	return nil, fmt.Errorf("unexpected call: RecordFinding")
}

func (m *mockLibrarianClient) GetLaw(_ context.Context, _ *flowv1gen.GetLawRequest, _ ...grpc.CallOption) (*flowv1gen.GetLawResponse, error) {
	return nil, fmt.Errorf("unexpected call: GetLaw")
}

func (m *mockLibrarianClient) WriteLaw(_ context.Context, _ *flowv1gen.WriteLawRequest, _ ...grpc.CallOption) (*flowv1gen.WriteLawResponse, error) {
	return nil, fmt.Errorf("unexpected call: WriteLaw")
}

func (m *mockLibrarianClient) RetireLaw(_ context.Context, _ *flowv1gen.RetireLawRequest, _ ...grpc.CallOption) (*flowv1gen.RetireLawResponse, error) {
	return nil, fmt.Errorf("unexpected call: RetireLaw")
}

func (m *mockLibrarianClient) ReplicateLaws(_ context.Context, _ *flowv1gen.ReplicateLawsRequest, _ ...grpc.CallOption) (*flowv1gen.ReplicateLawsResponse, error) {
	return nil, fmt.Errorf("unexpected call: ReplicateLaws")
}

func (m *mockLibrarianClient) ApplyLifecycleAction(_ context.Context, _ *flowv1gen.ApplyLifecycleActionRequest, _ ...grpc.CallOption) (*flowv1gen.ApplyLifecycleActionResponse, error) {
	return nil, fmt.Errorf("unexpected call: ApplyLifecycleAction")
}

func (m *mockLibrarianClient) CreateDisputeRecord(_ context.Context, _ *flowv1gen.CreateDisputeRecordRequest, _ ...grpc.CallOption) (*flowv1gen.CreateDisputeRecordResponse, error) {
	return nil, fmt.Errorf("unexpected call: CreateDisputeRecord")
}

func (m *mockLibrarianClient) RetireDisputeRecord(_ context.Context, _ *flowv1gen.RetireDisputeRecordRequest, _ ...grpc.CallOption) (*flowv1gen.RetireDisputeRecordResponse, error) {
	return nil, fmt.Errorf("unexpected call: RetireDisputeRecord")
}

func (m *mockLibrarianClient) GetActiveDisputes(_ context.Context, _ *flowv1gen.GetActiveDisputesRequest, _ ...grpc.CallOption) (*flowv1gen.GetActiveDisputesResponse, error) {
	return nil, fmt.Errorf("unexpected call: GetActiveDisputes")
}

func (m *mockLibrarianClient) SearchSimilarLaws(_ context.Context, _ *flowv1gen.SearchSimilarLawsRequest, _ ...grpc.CallOption) (*flowv1gen.SearchSimilarLawsResponse, error) {
	return nil, fmt.Errorf("unexpected call: SearchSimilarLaws")
}

func (m *mockLibrarianClient) GetLawGroup(_ context.Context, _ *flowv1gen.GetLawGroupRequest, _ ...grpc.CallOption) (*flowv1gen.GetLawGroupResponse, error) {
	return nil, fmt.Errorf("unexpected call: GetLawGroup")
}

func (m *mockLibrarianClient) ListLawGroups(_ context.Context, _ *flowv1gen.ListLawGroupsRequest, _ ...grpc.CallOption) (*flowv1gen.ListLawGroupsResponse, error) {
	return nil, fmt.Errorf("unexpected call: ListLawGroups")
}

func newScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := flowv1.AddToScheme(s); err != nil {
		t.Fatalf("add flowv1 scheme: %v", err)
	}
	return s
}

func TestLawGroupReconciler_CreateSyncsToLibrarian(t *testing.T) {
	t.Parallel()

	s := newScheme(t)
	mock := &mockLibrarianClient{}

	lg := &flowv1.LawGroup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "security",
			Namespace: "default",
		},
		Spec: flowv1.LawGroupSpec{
			Mode:   "law-by-law",
			Passes: 3,
		},
	}

	client := fake.NewClientBuilder().
		WithScheme(s).
		WithStatusSubresource(&flowv1.LawGroup{}).
		WithObjects(lg).
		Build()

	r := &LawGroupReconciler{
		Client:    client,
		Scheme:    s,
		Librarian: mock,
	}

	req := ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "security", Namespace: "default"},
	}

	result, err := r.Reconcile(context.Background(), req)
	if err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}
	if result.RequeueAfter != 0 {
		t.Fatal("expected no requeue on successful sync")
	}

	if len(mock.SyncedGroups) != 1 || mock.SyncedGroups[0] != "security" {
		t.Fatalf("expected SyncLawGroup called with 'security', got %v", mock.SyncedGroups)
	}
}

func TestLawGroupReconciler_DeleteDeletesFromLibrarian(t *testing.T) {
	t.Parallel()

	s := newScheme(t)
	mock := &mockLibrarianClient{}

	now := metav1.Now()
	lg := &flowv1.LawGroup{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "security",
			Namespace:         "default",
			Finalizers:        []string{"flow.gideas.io/test"},
			DeletionTimestamp: &now,
		},
		Spec: flowv1.LawGroupSpec{
			Mode:   "law-by-law",
			Passes: 3,
		},
	}

	client := fake.NewClientBuilder().
		WithScheme(s).
		WithStatusSubresource(&flowv1.LawGroup{}).
		WithObjects(lg).
		Build()

	r := &LawGroupReconciler{
		Client:    client,
		Scheme:    s,
		Librarian: mock,
	}

	req := ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "security", Namespace: "default"},
	}

	result, err := r.Reconcile(context.Background(), req)
	if err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}
	if result.RequeueAfter != 0 {
		t.Fatal("expected no requeue on successful delete")
	}

	if len(mock.DeletedGroups) != 1 || mock.DeletedGroups[0] != "security" {
		t.Fatalf("expected DeleteLawGroup called with 'security', got %v", mock.DeletedGroups)
	}
}

func TestLawGroupReconciler_LibrarianErrorRequeues(t *testing.T) {
	t.Parallel()

	s := newScheme(t)
	mock := &mockLibrarianClient{
		SyncError: fmt.Errorf("librarian unavailable"),
	}

	lg := &flowv1.LawGroup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "security",
			Namespace: "default",
		},
		Spec: flowv1.LawGroupSpec{
			Mode:   "law-by-law",
			Passes: 3,
		},
	}

	client := fake.NewClientBuilder().
		WithScheme(s).
		WithStatusSubresource(&flowv1.LawGroup{}).
		WithObjects(lg).
		Build()

	ctrl.SetLogger(logr.Discard())
	t.Cleanup(func() { ctrl.SetLogger(logr.Discard()) })

	r := &LawGroupReconciler{
		Client:    client,
		Scheme:    s,
		Librarian: mock,
	}

	req := ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "security", Namespace: "default"},
	}

	result, err := r.Reconcile(context.Background(), req)
	if err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}
	if result.RequeueAfter == 0 {
		t.Fatal("expected requeue on Librarian error")
	}
}

func TestLawGroupReconciler_NilLibrarianRequeues(t *testing.T) {
	t.Parallel()

	s := newScheme(t)

	lg := &flowv1.LawGroup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "security",
			Namespace: "default",
		},
		Spec: flowv1.LawGroupSpec{
			Mode:   "law-by-law",
			Passes: 3,
		},
	}

	client := fake.NewClientBuilder().
		WithScheme(s).
		WithStatusSubresource(&flowv1.LawGroup{}).
		WithObjects(lg).
		Build()

	ctrl.SetLogger(logr.Discard())
	t.Cleanup(func() { ctrl.SetLogger(logr.Discard()) })

	r := &LawGroupReconciler{
		Client:    client,
		Scheme:    s,
		Librarian: nil,
	}

	req := ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "security", Namespace: "default"},
	}

	result, err := r.Reconcile(context.Background(), req)
	if err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}
	if result.RequeueAfter == 0 {
		t.Fatal("expected requeue when Librarian is nil")
	}
}

func TestLawGroupReconciler_MissingObject(t *testing.T) {
	t.Parallel()

	s := newScheme(t)
	mock := &mockLibrarianClient{}

	client := fake.NewClientBuilder().
		WithScheme(s).
		Build()

	ctrl.SetLogger(logr.Discard())
	t.Cleanup(func() { ctrl.SetLogger(logr.Discard()) })

	r := &LawGroupReconciler{
		Client:    client,
		Scheme:    s,
		Librarian: mock,
	}

	req := ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "nonexistent", Namespace: "default"},
	}

	result, err := r.Reconcile(context.Background(), req)
	if err != nil {
		t.Fatalf("Reconcile returned error for missing object: %v", err)
	}
	if result.RequeueAfter != 0 {
		t.Fatal("expected no requeue for missing object")
	}
}
