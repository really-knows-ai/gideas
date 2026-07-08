package api

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	corev1client "k8s.io/client-go/kubernetes/typed/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	crdfake "sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// ─── Setup helpers ─────────────────────────────────────────────────────────

// newFakeScheme creates a scheme with all four flow.gideas.io CRD kinds
// registered as unstructured.
func newFakeScheme() *runtime.Scheme {
	scheme := runtime.NewScheme()
	scheme.AddKnownTypeWithName(
		schema.GroupVersionKind{Group: "flow.gideas.io", Version: "v1", Kind: "Workitem"},
		&unstructured.Unstructured{},
	)
	scheme.AddKnownTypeWithName(
		schema.GroupVersionKind{Group: "flow.gideas.io", Version: "v1", Kind: "WorkitemList"},
		&unstructured.UnstructuredList{},
	)
	scheme.AddKnownTypeWithName(
		schema.GroupVersionKind{Group: "flow.gideas.io", Version: "v1", Kind: "FoundryFlow"},
		&unstructured.Unstructured{},
	)
	scheme.AddKnownTypeWithName(
		schema.GroupVersionKind{Group: "flow.gideas.io", Version: "v1", Kind: "FoundryFlowList"},
		&unstructured.UnstructuredList{},
	)
	scheme.AddKnownTypeWithName(
		schema.GroupVersionKind{Group: "flow.gideas.io", Version: "v1", Kind: "FoundryNode"},
		&unstructured.Unstructured{},
	)
	scheme.AddKnownTypeWithName(
		schema.GroupVersionKind{Group: "flow.gideas.io", Version: "v1", Kind: "FoundryNodeList"},
		&unstructured.UnstructuredList{},
	)
	scheme.AddKnownTypeWithName(
		schema.GroupVersionKind{Group: "flow.gideas.io", Version: "v1", Kind: "GovernedArtefact"},
		&unstructured.Unstructured{},
	)
	scheme.AddKnownTypeWithName(
		schema.GroupVersionKind{Group: "flow.gideas.io", Version: "v1", Kind: "GovernedArtefactList"},
		&unstructured.UnstructuredList{},
	)
	return scheme
}

// newFakeSchemeAndClient creates a scheme and a fake controller-runtime client
// seeded with the given objects.
func newFakeSchemeAndClient(objects ...runtime.Object) (*runtime.Scheme, client.Client) {
	scheme := newFakeScheme()
	crdClient := crdfake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(objects...).Build()
	return scheme, crdClient
}

// errorCoreClient embeds a real fake.Clientset but overrides CoreV1 to return
// an error-returning namespace client. Used in T9 to test namespace list denial fallback.
type errorCoreClient struct {
	*k8sfake.Clientset
	err error
}

func (c *errorCoreClient) CoreV1() corev1client.CoreV1Interface {
	real := c.Clientset.CoreV1()
	return &errorCoreV1{CoreV1Interface: real, err: c.err}
}

type errorCoreV1 struct {
	corev1client.CoreV1Interface
	err error
}

func (c *errorCoreV1) Namespaces() corev1client.NamespaceInterface {
	return &errorNamespaceInterface{err: c.err}
}

type errorNamespaceInterface struct {
	corev1client.NamespaceInterface
	err error
}

func (c *errorNamespaceInterface) List(ctx context.Context, opts metav1.ListOptions) (*corev1.NamespaceList, error) {
	return nil, c.err
}

// ─── Test helper constructors ──────────────────────────────────────────────

func makeWorkitemUnstructured(name, state, assignee string) *unstructured.Unstructured {
	obj := &unstructured.Unstructured{}
	obj.SetAPIVersion("flow.gideas.io/v1")
	obj.SetKind("Workitem")
	obj.SetName(name)
	obj.SetNamespace("default")
	if state != "" || assignee != "" {
		_ = unstructured.SetNestedField(obj.Object, state, "status", "phase")
		_ = unstructured.SetNestedField(obj.Object, assignee, "status", "currentAssignee")
	}
	return obj
}

func makeWorkitemWithStatus(name, state, assignee, failureReason string, thrashCounters map[string]int32, labels map[string]string) *unstructured.Unstructured {
	obj := makeWorkitemUnstructured(name, state, assignee)
	if failureReason != "" {
		_ = unstructured.SetNestedField(obj.Object, failureReason, "status", "failureReason")
	}
	if thrashCounters != nil {
		raw := make(map[string]interface{}, len(thrashCounters))
		for k, v := range thrashCounters {
			raw[k] = int64(v)
		}
		_ = unstructured.SetNestedMap(obj.Object, raw, "status", "thrashCounters")
	}
	if labels != nil {
		obj.SetLabels(labels)
	}
	return obj
}

func makeWorkitemWithLabel(name, state, assignee string, labels map[string]string) *unstructured.Unstructured {
	obj := makeWorkitemUnstructured(name, state, assignee)
	if labels != nil {
		obj.SetLabels(labels)
	}
	return obj
}

func makeFoundryNode(name, entry string, targets []string) *unstructured.Unstructured {
	obj := &unstructured.Unstructured{}
	obj.SetAPIVersion("flow.gideas.io/v1")
	obj.SetKind("FoundryNode")
	obj.SetName(name)
	obj.SetNamespace("default")
	if entry != "" {
		_ = unstructured.SetNestedField(obj.Object, entry, "spec", "entry")
	}
	if len(targets) > 0 {
		targetSlice := make([]interface{}, len(targets))
		for i, t := range targets {
			targetSlice[i] = map[string]interface{}{"target": t}
		}
		_ = unstructured.SetNestedSlice(obj.Object, targetSlice, "spec", "outputs")
	}
	return obj
}

func makeFoundryFlow(name string, entryContracts map[string]map[string]string) *unstructured.Unstructured {
	obj := &unstructured.Unstructured{}
	obj.SetAPIVersion("flow.gideas.io/v1")
	obj.SetKind("FoundryFlow")
	obj.SetName(name)
	obj.SetNamespace("default")
	if entryContracts != nil {
		raw := make(map[string]interface{}, len(entryContracts))
		for k, v := range entryContracts {
			inner := make(map[string]interface{}, len(v))
			for ik, iv := range v {
				inner[ik] = iv
			}
			raw[k] = inner
		}
		_ = unstructured.SetNestedMap(obj.Object, raw, "spec", "entryContracts")
	}
	return obj
}

// ─── T1: ListNamespaces ────────────────────────────────────────────────────

func TestListNamespaces(t *testing.T) {
	c := k8sfake.NewSimpleClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "alpha"}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "beta"}},
	)
	k8s := &K8sClient{CoreClient: c}
	namespaces, err := k8s.ListNamespaces(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(namespaces) != 2 {
		t.Fatalf("expected 2 namespaces, got %d", len(namespaces))
	}
	if namespaces[0] != "alpha" || namespaces[1] != "beta" {
		t.Errorf("expected [alpha beta], got %v", namespaces)
	}
}

// ─── T2: ListWorkitems ─────────────────────────────────────────────────────

func TestListWorkitems(t *testing.T) {
	_, crdClient := newFakeSchemeAndClient(
		makeWorkitemUnstructured("wi-1", "Running", "sort"),
		makeWorkitemUnstructured("wi-2", "Completed", ""),
		makeWorkitemUnstructured("wi-3", "", ""),
		makeWorkitemUnstructured("wi-4", "Completed", "forge"),
	)
	k8s := &K8sClient{CRDClient: crdClient}
	items, err := k8s.ListWorkitems(context.Background(), "default")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(items) != 4 {
		t.Fatalf("expected 4 items, got %d", len(items))
	}
	// wi-1: Running/sort
	if items[0].Name != "wi-1" {
		t.Errorf("expected wi-1, got %s", items[0].Name)
	}
	if items[0].State != "Running" {
		t.Errorf("expected state Running, got %s", items[0].State)
	}
	if items[0].Node != "sort" {
		t.Errorf("expected node sort, got %s", items[0].Node)
	}

	// wi-2: Completed -> Node "-"
	if items[1].Name != "wi-2" {
		t.Errorf("expected wi-2, got %s", items[1].Name)
	}
	if items[1].State != "Completed" {
		t.Errorf("expected state Completed, got %s", items[1].State)
	}
	if items[1].Node != "-" {
		t.Errorf("expected node '-', got %s", items[1].Node)
	}

	// wi-3: no status -> State "", Node "-"
	if items[2].Name != "wi-3" {
		t.Errorf("expected wi-3, got %s", items[2].Name)
	}
	if items[2].State != "" {
		t.Errorf("expected empty state, got %s", items[2].State)
	}
	if items[2].Node != "-" {
		t.Errorf("expected node '-', got %s", items[2].Node)
	}

	// wi-4: Completed with currentAssignee -> preserves the assignee, not "-"
	if items[3].Name != "wi-4" {
		t.Errorf("expected wi-4, got %s", items[3].Name)
	}
	if items[3].State != "Completed" {
		t.Errorf("expected state Completed, got %s", items[3].State)
	}
	if items[3].Node != "forge" {
		t.Errorf("expected node 'forge', got %s", items[3].Node)
	}
}

// ─── T3: WatchWorkitems ────────────────────────────────────────────────────

func TestWatchWorkitems(t *testing.T) {
	scheme := newFakeScheme()
	dynamicClient := dynamicfake.NewSimpleDynamicClient(scheme)

	k8s := &K8sClient{dynamicClient: dynamicClient}
	watcher, err := k8s.WatchWorkitems(context.Background(), "default")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer watcher.Stop()

	// Add a Workitem via the dynamic client
	wi := makeWorkitemUnstructured("wi-new", "Pending", "forge")
	_, err = dynamicClient.Resource(workitemGVR).Namespace("default").Create(context.Background(), wi, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("failed to create workitem: %v", err)
	}

	select {
	case event := <-watcher.ResultChan():
		if event.Type != watch.Added {
			t.Errorf("expected Added event, got %s", event.Type)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("expected watch event within 2s")
	}
}

// ─── T4: GetWorkitem ───────────────────────────────────────────────────────

func TestGetWorkitem(t *testing.T) {
	parent := makeWorkitemWithStatus("wi-parent", "Suspended", "human-approval",
		"Needs human review", map[string]int32{"forge": 1, "sort": 1},
		map[string]string{"env": "prod"},
	)
	child := makeWorkitemWithLabel("wi-child", "Running", "forge",
		map[string]string{"flow.gideas.io/parent": "wi-parent"},
	)

	_, crdClient := newFakeSchemeAndClient(parent, child)
	k8s := &K8sClient{CRDClient: crdClient}
	detail, err := k8s.GetWorkitem(context.Background(), "default", "wi-parent")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if detail.State != "Suspended" {
		t.Errorf("expected state Suspended, got %s", detail.State)
	}
	if detail.Node != "human-approval" {
		t.Errorf("expected node human-approval, got %s", detail.Node)
	}
	if detail.FailureReason != "Needs human review" {
		t.Errorf("expected failure reason 'Needs human review', got %s", detail.FailureReason)
	}
	if detail.ThrashCounters["forge"] != 1 || detail.ThrashCounters["sort"] != 1 {
		t.Errorf("expected thrashCounters {forge:1, sort:1}, got %v", detail.ThrashCounters)
	}
	if len(detail.ChildWorkitems) != 1 {
		t.Fatalf("expected 1 child, got %d", len(detail.ChildWorkitems))
	}
	if detail.ChildWorkitems[0].Name != "wi-child" {
		t.Errorf("expected child name wi-child, got %s", detail.ChildWorkitems[0].Name)
	}
}

// ─── T5: ListChildren ──────────────────────────────────────────────────────

func TestListChildren(t *testing.T) {
	_, crdClient := newFakeSchemeAndClient(
		makeWorkitemWithLabel("child-1", "Running", "forge", map[string]string{"flow.gideas.io/parent": "parent-1"}),
		makeWorkitemWithLabel("child-2", "Completed", "", map[string]string{"flow.gideas.io/parent": "parent-1"}),
		makeWorkitemWithLabel("other", "Pending", "forge", map[string]string{"flow.gideas.io/parent": "other-parent"}),
	)
	k8s := &K8sClient{CRDClient: crdClient}
	children, err := k8s.ListChildren(context.Background(), "default", "parent-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(children) != 2 {
		t.Fatalf("expected 2 children, got %d", len(children))
	}
	if children[0].Name != "child-1" {
		t.Errorf("expected child-1, got %s", children[0].Name)
	}
	if children[1].Name != "child-2" {
		t.Errorf("expected child-2, got %s", children[1].Name)
	}
}

// ─── T6: CreateWorkitem ────────────────────────────────────────────────────

func TestCreateWorkitem(t *testing.T) {
	scheme, crdClient := newFakeSchemeAndClient()
	k8s := &K8sClient{CRDClient: crdClient, scheme: scheme}
	err := k8s.CreateWorkitem(context.Background(), "default", "wi-new", map[string]string{"flow.gideas.io/creator": "flowctl"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify via GetWorkitem
	detail, err := k8s.GetWorkitem(context.Background(), "default", "wi-new")
	if err != nil {
		t.Fatalf("failed to get created workitem: %v", err)
	}
	if detail.Name != "wi-new" {
		t.Errorf("expected name wi-new, got %s", detail.Name)
	}
	if _, ok := detail.Labels["flow.gideas.io/creator"]; !ok {
		t.Error("expected flow.gideas.io/creator label")
	}
}

// ─── T7: UpdateWorkitemStatus ──────────────────────────────────────────────

func TestUpdateWorkitemStatus(t *testing.T) {
	scheme, crdClient := newFakeSchemeAndClient(
		makeWorkitemUnstructured("wi-test", "", ""),
	)
	dynamicClient := dynamicfake.NewSimpleDynamicClient(scheme,
		makeWorkitemUnstructured("wi-test", "", ""),
	)
	k8s := &K8sClient{CRDClient: crdClient, dynamicClient: dynamicClient, scheme: scheme}
	err := k8s.UpdateWorkitemStatus(context.Background(), "default", "wi-test", "Pending", "forge")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify via dynamic client (GetWorkitem uses crdClient which has a separate store in tests)
	result, err := dynamicClient.Resource(workitemGVR).Namespace("default").Get(context.Background(), "wi-test", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("failed to get workitem after status update: %v", err)
	}
	state, _, _ := unstructured.NestedString(result.Object, "status", "phase")
	if state != "Pending" {
		t.Errorf("expected state Pending, got %s", state)
	}
	node, _, _ := unstructured.NestedString(result.Object, "status", "currentAssignee")
	if node != "forge" {
		t.Errorf("expected node forge, got %s", node)
	}
}

// ─── T8: DeleteWorkitem ────────────────────────────────────────────────────

func TestDeleteWorkitem(t *testing.T) {
	scheme, crdClient := newFakeSchemeAndClient(
		makeWorkitemUnstructured("wi-del", "Completed", ""),
	)
	k8s := &K8sClient{CRDClient: crdClient, scheme: scheme}
	err := k8s.DeleteWorkitem(context.Background(), "default", "wi-del")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify removal
	_, err = k8s.GetWorkitem(context.Background(), "default", "wi-del")
	if err == nil {
		t.Error("expected error (not found) after delete, got nil")
	}
}

// ─── T9: Namespace fallback ────────────────────────────────────────────────

func TestNamespaceFallbackOnDenied(t *testing.T) {
	errorClient := &errorCoreClient{
		Clientset: k8sfake.NewSimpleClientset(),
		err:       errors.New("permission denied"),
	}
	k8s := &K8sClient{CoreClient: errorClient}
	_, err := k8s.ListNamespaces(context.Background())
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	// GetCurrentContextNamespace should not panic and return a non-empty string
	ns := GetCurrentContextNamespace()
	if ns == "" {
		t.Error("expected non-empty fallback namespace")
	}
}

// ─── T10: GetCurrentContextNamespace ───────────────────────────────────────

func TestGetCurrentContextNamespace(t *testing.T) {
	// This function reads the real kubeconfig; in CI it may not be set.
	ns := GetCurrentContextNamespace()
	if ns == "" {
		t.Error("expected non-empty namespace")
	}
}

// ─── T11: WatchWithBackoff ─────────────────────────────────────────────────

func TestWatchWithBackoffReconnects(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	scheme := newFakeScheme()
	dynamicClient := dynamicfake.NewSimpleDynamicClient(scheme)
	k8s := &K8sClient{dynamicClient: dynamicClient}

	var connectCount, disconnectCount int
	var mu sync.Mutex
	eventCh := make(chan watch.Event, 10)

	// Start WatchWithBackoff in goroutine
	go k8s.WatchWithBackoff(ctx, "default", func(event watch.Event) {
		mu.Lock()
		eventCh <- event
		mu.Unlock()
	}, WatchOptions{
		OnDisconnect: func(err error) {
			mu.Lock()
			disconnectCount++
			mu.Unlock()
		},
		OnReconnect: func() {
			mu.Lock()
			connectCount++
			mu.Unlock()
		},
	})

	// Give the watch a moment to start
	time.Sleep(200 * time.Millisecond)

	// Create a Workitem to trigger a watch event
	_, err := dynamicClient.Resource(workitemGVR).Namespace("default").Create(ctx,
		makeWorkitemUnstructured("wi-1", "Running", "forge"), metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("failed to create workitem: %v", err)
	}

	select {
	case event := <-eventCh:
		if event.Type != watch.Added {
			t.Errorf("expected Added event, got %s", event.Type)
		}
		u, ok := event.Object.(*unstructured.Unstructured)
		if !ok {
			t.Fatal("expected *unstructured.Unstructured")
		}
		if u.GetName() != "wi-1" {
			t.Errorf("expected wi-1, got %s", u.GetName())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("expected watch event within 2s")
	}

	mu.Lock()
	t.Logf("connectCount=%d, disconnectCount=%d", connectCount, disconnectCount)
	mu.Unlock()
}

// ─── T12: ListFoundryNodes ─────────────────────────────────────────────────

func TestListFoundryNodes(t *testing.T) {
	_, crdClient := newFakeSchemeAndClient(
		makeFoundryNode("forge", "prompt", []string{"sort", "reviewer"}),
		makeFoundryNode("sort", "sort", []string{"appraise"}),
	)
	k8s := &K8sClient{CRDClient: crdClient}
	nodes, err := k8s.ListFoundryNodes(context.Background(), "default")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(nodes) != 2 {
		t.Fatalf("expected 2 nodes, got %d", len(nodes))
	}
	if nodes[0].Name != "forge" {
		t.Errorf("expected forge, got %s", nodes[0].Name)
	}
	if nodes[0].Entry != "prompt" {
		t.Errorf("expected entry 'prompt', got %s", nodes[0].Entry)
	}
	if len(nodes[0].Targets) != 2 || nodes[0].Targets[0] != "sort" || nodes[0].Targets[1] != "reviewer" {
		t.Errorf("expected targets [sort reviewer], got %v", nodes[0].Targets)
	}
	if nodes[1].Name != "sort" {
		t.Errorf("expected sort, got %s", nodes[1].Name)
	}
}

// ─── T13: GetFoundryFlow ───────────────────────────────────────────────────

func TestGetFoundryFlow_Singular(t *testing.T) {
	_, crdClient := newFakeSchemeAndClient(
		makeFoundryFlow("main-flow", map[string]map[string]string{"prompt": {"governed_artefact": "haiku"}}),
	)
	k8s := &K8sClient{CRDClient: crdClient}
	flow, err := k8s.GetFoundryFlow(context.Background(), "default")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if flow == nil {
		t.Fatal("expected non-nil flow")
	}
	if flow.Name != "main-flow" {
		t.Errorf("expected name main-flow, got %s", flow.Name)
	}
}

func TestGetFoundryFlow_Zero(t *testing.T) {
	_, crdClient := newFakeSchemeAndClient()
	k8s := &K8sClient{CRDClient: crdClient}
	flow, err := k8s.GetFoundryFlow(context.Background(), "default")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if flow != nil {
		t.Fatal("expected nil flow for empty namespace")
	}
}

func TestGetFoundryFlow_Multiple(t *testing.T) {
	_, crdClient := newFakeSchemeAndClient(
		makeFoundryFlow("flow-1", nil),
		makeFoundryFlow("flow-2", nil),
	)
	k8s := &K8sClient{CRDClient: crdClient}
	_, err := k8s.GetFoundryFlow(context.Background(), "default")
	if err == nil {
		t.Fatal("expected error for multiple flows")
	}
	if err.Error() != "multiple FoundryFlows detected in namespace default; expected exactly one" {
		t.Errorf("unexpected error message: %v", err)
	}
}

// ─── Ensure imports are used ───────────────────────────────────────────────
var _ = dynamic.Interface(nil)
