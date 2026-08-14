package flow

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	corev1client "k8s.io/client-go/kubernetes/typed/core/v1"
	crdfake "sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/foundry/flow/tools/flowctl/internal/api"
)

// ─── Fake scheme helpers ─────────────────────────────────────────────────

// installFakeScheme registers all CRD kinds needed by install tests.
func installFakeScheme() *runtime.Scheme {
	scheme := runtime.NewScheme()
	register := func(kind string) {
		scheme.AddKnownTypeWithName(
			schema.GroupVersionKind{Group: "flow.foundry.io", Version: "v1", Kind: kind},
			&unstructured.Unstructured{},
		)
		scheme.AddKnownTypeWithName(
			schema.GroupVersionKind{Group: "flow.foundry.io", Version: "v1", Kind: kind + "List"},
			&unstructured.UnstructuredList{},
		)
	}
	register("FoundryFlow")
	register("FoundryNode")
	register("GovernedArtefact")
	register("Law")
	register("LawGroup")
	register("Treaty")
	register("Workitem")
	return scheme
}

// newInstallFakeClient creates a K8sClient wired to fake clients for testing.
// The discovery client is pre-configured to return success for flow.foundry.io/v1
// so that CRD checks pass by default.
func newInstallFakeClient(t *testing.T, crdObjects []runtime.Object, coreObjects ...runtime.Object) *api.K8sClient {
	t.Helper()
	scheme := installFakeScheme()
	dyn := dynamicfake.NewSimpleDynamicClient(scheme, crdObjects...)
	core := &readyDiscoveryCoreClient{Clientset: k8sfake.NewSimpleClientset(coreObjects...)}
	crdClient := crdfake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(crdObjects...).Build()
	return &api.K8sClient{
		CoreClient:    core,
		CRDClient:     crdClient,
		DynamicClient: dyn,
	}
}

// readyDiscoveryCoreClient wraps a fake Clientset and overrides Discovery()
// to return success for flow.foundry.io/v1.
type readyDiscoveryCoreClient struct {
	*k8sfake.Clientset
}

func (c *readyDiscoveryCoreClient) Discovery() discovery.DiscoveryInterface {
	return &readyDiscoveryClient{DiscoveryInterface: c.Clientset.Discovery()}
}

type readyDiscoveryClient struct {
	discovery.DiscoveryInterface
}

func (c *readyDiscoveryClient) ServerResourcesForGroupVersion(gv string) (*metav1.APIResourceList, error) {
	if gv == "flow.foundry.io/v1" {
		return &metav1.APIResourceList{
			GroupVersion: gv,
			APIResources: []metav1.APIResource{
				{Name: "foundryflows", Kind: "FoundryFlow", Group: "flow.foundry.io", Version: "v1"},
				{Name: "foundrynodes", Kind: "FoundryNode", Group: "flow.foundry.io", Version: "v1"},
				{Name: "governedartefacts", Kind: "GovernedArtefact", Group: "flow.foundry.io", Version: "v1"},
				{Name: "laws", Kind: "Law", Group: "flow.foundry.io", Version: "v1"},
				{Name: "lawgroups", Kind: "LawGroup", Group: "flow.foundry.io", Version: "v1"},
				{Name: "treaties", Kind: "Treaty", Group: "flow.foundry.io", Version: "v1"},
				{Name: "workitems", Kind: "Workitem", Group: "flow.foundry.io", Version: "v1"},
			},
		}, nil
	}
	return c.DiscoveryInterface.ServerResourcesForGroupVersion(gv)
}

// ─── Test helpers ─────────────────────────────────────────────────────────

// writeTestDir creates a temporary directory with the given manifest and file
// map, returning the directory path. The caller is responsible for cleanup via
// t.TempDir().
func writeTestDir(t *testing.T, manifest *Manifest, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()

	// Write manifest.yaml
	manifestData, err := manifest.Marshal()
	if err != nil {
		t.Fatalf("failed to marshal manifest: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, ManifestFile), manifestData, 0644); err != nil {
		t.Fatalf("failed to write manifest.yaml: %v", err)
	}

	// Write resource files
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0644); err != nil {
			t.Fatalf("failed to write %s: %v", name, err)
		}
	}

	return dir
}

// ─── gitRepoServer ────────────────────────────────────────────────────────

// gitRepoServer creates a local bare git repo with the given manifest and
// file map, commits them, and returns the file:// URL that resolveSource can
// clone. The caller must call Close to clean up the temp directory.
type gitRepoServer struct {
	URL string
	dir string
}

func newGitRepoServer(t *testing.T, ref string, manifest *Manifest, files map[string]string) *gitRepoServer {
	t.Helper()

	// Create a working directory for the source repo
	srcDir := t.TempDir()

	// Write manifest and files
	manifestData, err := manifest.Marshal()
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, ManifestFile), manifestData, 0644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(srcDir, name), []byte(content), 0644); err != nil {
			t.Fatalf("write file %s: %v", name, err)
		}
	}

	// Init git repo, add files, commit
	runGit(t, srcDir, "init")
	runGit(t, srcDir, "add", ".")
	runGit(t, srcDir, "commit", "-m", "init")

	if ref != "" {
		runGit(t, srcDir, "branch", "-m", ref)
	}

	// Create bare clone for the remote
	bareDir := t.TempDir()
	runGit(t, "", "clone", "--bare", srcDir, bareDir)

	s := &gitRepoServer{
		URL: "file://" + bareDir,
		dir: bareDir,
	}
	return s
}

func (s *gitRepoServer) Close() {
	// TempDir handles cleanup via t.TempDir()
}

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s failed: %v\n%s", strings.Join(args, " "), err, string(out))
	}
	return string(out)
}

// ─── Unstructured builders ────────────────────────────────────────────────

func makeObj(apiVersion, kind, name, namespace string) *unstructured.Unstructured {
	obj := &unstructured.Unstructured{}
	obj.SetAPIVersion(apiVersion)
	obj.SetKind(kind)
	obj.SetName(name)
	obj.SetNamespace(namespace)
	return obj
}

func makeFlow(name, namespace string) *unstructured.Unstructured {
	return makeObj("flow.foundry.io/v1", "FoundryFlow", name, namespace)
}

func makeNode(name, namespace, flowName string) *unstructured.Unstructured {
	obj := makeObj("flow.foundry.io/v1", "FoundryNode", name, namespace)
	obj.SetLabels(map[string]string{"flow.foundry.io/flow-name": flowName})
	return obj
}

func makeArtefact(name, namespace string) *unstructured.Unstructured {
	return makeObj("flow.foundry.io/v1", "GovernedArtefact", name, namespace)
}

func makeConfigMap(name, namespace string) *unstructured.Unstructured {
	obj := makeObj("v1", "ConfigMap", name, namespace)
	_ = unstructured.SetNestedField(obj.Object, map[string]interface{}{"key": "value"}, "data")
	return obj
}

func makeWorkitem(name, namespace string) *unstructured.Unstructured {
	return makeObj("flow.foundry.io/v1", "Workitem", name, namespace)
}

// makeSecret returns an unstructured Secret resource (should be filtered out).
func makeSecret(name, namespace string) *unstructured.Unstructured {
	obj := makeObj("v1", "Secret", name, namespace)
	_ = unstructured.SetNestedField(obj.Object, map[string]interface{}{"password": "hunter2"}, "data")
	return obj
}

func makeLaw(name, namespace string) *unstructured.Unstructured {
	return makeObj("flow.foundry.io/v1", "Law", name, namespace)
}

func makeLawGroup(name, namespace string) *unstructured.Unstructured {
	return makeObj("flow.foundry.io/v1", "LawGroup", name, namespace)
}

func makeTreaty(name, namespace string) *unstructured.Unstructured {
	return makeObj("flow.foundry.io/v1", "Treaty", name, namespace)
}

// partialFailDynamicClient wraps a dynamic.Interface and returns a configurable
// error when Create is called for a specific resource name.
type partialFailDynamicClient struct {
	dynamic.Interface
	failResource string // resource name that should fail on Create
}

func (c *partialFailDynamicClient) Resource(gvr schema.GroupVersionResource) dynamic.NamespaceableResourceInterface {
	base := c.Interface.Resource(gvr)
	return &partialFailNamespaceableResource{
		NamespaceableResourceInterface: base,
		failResource:                   c.failResource,
	}
}

type partialFailNamespaceableResource struct {
	dynamic.NamespaceableResourceInterface
	failResource string
}

func (r *partialFailNamespaceableResource) Namespace(ns string) dynamic.ResourceInterface {
	base := r.NamespaceableResourceInterface.Namespace(ns)
	return &partialFailResourceInterface{
		ResourceInterface: base,
		failResource:      r.failResource,
	}
}

type partialFailResourceInterface struct {
	dynamic.ResourceInterface
	failResource string
}

func (r *partialFailResourceInterface) Create(ctx context.Context, obj *unstructured.Unstructured, opts metav1.CreateOptions, subresources ...string) (*unstructured.Unstructured, error) {
	if obj.GetName() == r.failResource {
		return nil, errors.New("simulated create failure")
	}
	return r.ResourceInterface.Create(ctx, obj, opts, subresources...)
}

// --- Core client overrides for testing ---

// stuckCoreClient overrides the namespace Get/Delete to simulate a stuck namespace.
// It also overrides Discovery() to return success for flow.foundry.io/v1 so the
// CRD check passes before the stuck namespace test logic kicks in.
type stuckCoreClient struct {
	*k8sfake.Clientset
	namespaceName string
}

func (c *stuckCoreClient) CoreV1() corev1client.CoreV1Interface {
	return &stuckCoreV1{CoreV1Interface: c.Clientset.CoreV1(), namespaceName: c.namespaceName}
}

func (c *stuckCoreClient) Discovery() discovery.DiscoveryInterface {
	return &readyDiscoveryClient{DiscoveryInterface: c.Clientset.Discovery()}
}

type stuckCoreV1 struct {
	corev1client.CoreV1Interface
	namespaceName string
}

func (c *stuckCoreV1) Namespaces() corev1client.NamespaceInterface {
	return &stuckNamespaceInterface{
		NamespaceInterface: c.CoreV1Interface.Namespaces(),
		namespaceName:      c.namespaceName,
	}
}

type stuckNamespaceInterface struct {
	corev1client.NamespaceInterface
	namespaceName string
}

// Delete does nothing for the stuck namespace (simulates stuck Terminating).
func (c *stuckNamespaceInterface) Delete(ctx context.Context, name string, opts metav1.DeleteOptions) error {
	if name == c.namespaceName {
		return nil
	}
	return c.NamespaceInterface.Delete(ctx, name, opts)
}

// Get always returns a Terminating namespace for the stuck namespace.
func (c *stuckNamespaceInterface) Get(ctx context.Context, name string, opts metav1.GetOptions) (*corev1.Namespace, error) {
	if name == c.namespaceName {
		return &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{Name: name},
			Status:     corev1.NamespaceStatus{Phase: corev1.NamespaceTerminating},
		}, nil
	}
	return c.NamespaceInterface.Get(ctx, name, opts)
}

// crdCheckCoreClient overrides Discovery() to return a discovery client that
// returns NotFound for specific group/version strings.
type crdCheckCoreClient struct {
	*k8sfake.Clientset
	missingGroups map[string]bool
}

func (c *crdCheckCoreClient) Discovery() discovery.DiscoveryInterface {
	return &crdCheckDiscoveryClient{
		DiscoveryInterface: c.Clientset.Discovery(),
		missingGroups:      c.missingGroups,
	}
}

type crdCheckDiscoveryClient struct {
	discovery.DiscoveryInterface
	missingGroups map[string]bool
}

func (c *crdCheckDiscoveryClient) ServerResourcesForGroupVersion(gv string) (*metav1.APIResourceList, error) {
	if c.missingGroups[gv] {
		return nil, apierrors.NewNotFound(schema.GroupResource{Group: gv}, "")
	}
	return c.DiscoveryInterface.ServerResourcesForGroupVersion(gv)
}

// partialFailCoreClient wraps a fake Clientset and a dynamic client, overriding
// nothing — we use a different mechanism for partial failure (pre-seed objects
// so Create returns AlreadyExists is "unchanged", not "failed"), so for actual
// failure we use an errorCoreClient for the namespace.

// ─── T1: Successful install ───────────────────────────────────────────────

const testFlowName = "my-flow"

func TestInstall_Success(t *testing.T) {
	var crds []runtime.Object
	crds = append(crds, makeArtefact("haiku", ""))
	crds = append(crds, makeFlow(testFlowName, ""))
	crds = append(crds, makeNode("forge", "", testFlowName))
	crds = append(crds, makeConfigMap("app-config", ""))

	k8s := newInstallFakeClient(t, crds)

	var stdout, stderr bytes.Buffer
	result, err := InstallFlow(context.Background(), k8s, InstallOptions{
		Source:   writeTestDir(t, &Manifest{Name: testFlowName, Version: "1.0.0", Schemas: []string{"flow.foundry.io/v1"}, Resources: []ManifestResource{{Path: "resources.yaml", Kind: "GovernedArtefact"}}}, map[string]string{"resources.yaml": "apiVersion: flow.foundry.io/v1\nkind: GovernedArtefact\nmetadata:\n  name: haiku\n"}),
		FlowName: testFlowName,
	}, &stdout, &stderr, strings.NewReader(""))
	if err != nil {
		t.Fatalf("InstallFlow: %v", err)
	}
	if result.Created != 1 {
		t.Errorf("Created: got %d, want 1", result.Created)
	}
	if result.Failed != 0 {
		t.Errorf("Failed: got %d, want 0", result.Failed)
	}
}

func TestInstall_SuccessMultipleResources(t *testing.T) {
	// Package with GovernedArtefact + FoundryFlow + FoundryNode + ConfigMap
	dir := writeTestDir(t, &Manifest{
		Name:    "test-flow",
		Version: "1.0.0",
		Schemas: []string{"flow.foundry.io/v1"},
		Resources: []ManifestResource{
			{Path: "artefacts.yaml", Kind: "GovernedArtefact"},
			{Path: "flow.yaml", Kind: "FoundryFlow"},
			{Path: "nodes.yaml", Kind: "FoundryNode"},
			{Path: "configmaps.yaml", Kind: "ConfigMap"},
		},
	}, map[string]string{
		"artefacts.yaml": "apiVersion: flow.foundry.io/v1\nkind: GovernedArtefact\nmetadata:\n  name: haiku\n",
		"flow.yaml":      "apiVersion: flow.foundry.io/v1\nkind: FoundryFlow\nmetadata:\n  name: placeholder\n",
		"nodes.yaml":     "apiVersion: flow.foundry.io/v1\nkind: FoundryNode\nmetadata:\n  name: forge\n  labels:\n    flow.foundry.io/flow-name: placeholder\n",
		"configmaps.yaml": "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: app-config\ndata:\n  key: value\n",
	})

	k8s := newInstallFakeClient(t, nil)
	var stdout, stderr bytes.Buffer
	result, err := InstallFlow(context.Background(), k8s, InstallOptions{
		Source:   dir,
		FlowName: testFlowName,
	}, &stdout, &stderr, strings.NewReader(""))
	if err != nil {
		t.Fatalf("InstallFlow: %v", err)
	}
	if result.Created != 4 {
		t.Errorf("Created: got %d, want 4", result.Created)
	}
	if result.Failed != 0 {
		t.Errorf("Failed: got %d, want 0", result.Failed)
	}

	// Verify namespace was created
	ns, err := k8s.CoreClient.CoreV1().Namespaces().Get(context.Background(), testFlowName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("namespace not found: %v", err)
	}
	if ns.Name != testFlowName {
		t.Errorf("namespace name: got %q, want %q", ns.Name, testFlowName)
	}

	// Verify resource exists in namespace with correct metadata
	dyn := k8s.DynamicClient
	_, err = dyn.Resource(schema.GroupVersionResource{Group: "flow.foundry.io", Version: "v1", Resource: "governedartefacts"}).Namespace(testFlowName).Get(context.Background(), "haiku", metav1.GetOptions{})
	if err != nil {
		t.Errorf("GovernedArtefact not found: %v", err)
	}
}

// ─── T2: Duplicate flow error ─────────────────────────────────────────────

func TestInstall_DuplicateFlowError(t *testing.T) {
	// Pre-create namespace with a FoundryFlow
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: testFlowName}}
	flowObj := makeFlow(testFlowName, testFlowName)
	k8s := newInstallFakeClient(t, []runtime.Object{flowObj}, ns)

	dir := writeTestDir(t, &Manifest{
		Name:    testFlowName,
		Version: "1.0.0",
		Schemas: []string{"flow.foundry.io/v1"},
		Resources: []ManifestResource{
			{Path: "flow.yaml", Kind: "FoundryFlow"},
		},
	}, map[string]string{
		"flow.yaml": "apiVersion: flow.foundry.io/v1\nkind: FoundryFlow\nmetadata:\n  name: placeholder\n",
	})

	var stdout, stderr bytes.Buffer
	_, err := InstallFlow(context.Background(), k8s, InstallOptions{
		Source:   dir,
		FlowName: testFlowName,
	}, &stdout, &stderr, strings.NewReader(""))
	if err == nil {
		t.Fatal("expected error for duplicate flow, got nil")
	}
	if !strings.Contains(err.Error(), "already contains a flow") {
		t.Errorf("error should mention 'already contains a flow', got: %v", err)
	}
}

// ─── T3: --force with namespace deletion ─────────────────────────────────

func TestInstall_ForceWithDeletion(t *testing.T) {
	// Pre-create namespace with a FoundryFlow
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: testFlowName}}
	// Don't pre-seed flowObj in dynamic client — force deletes namespace
	// and we want to verify the flow is installed fresh. With fake clients,
	// the dynamic client store is independent of namespace deletion, so
	// we start with an empty dynamic store and only pre-seed the namespace.
	k8s := newInstallFakeClient(t, nil, ns)

	dir := writeTestDir(t, &Manifest{
		Name:    testFlowName,
		Version: "1.0.0",
		Schemas: []string{"flow.foundry.io/v1"},
		Resources: []ManifestResource{
			{Path: "flow.yaml", Kind: "FoundryFlow"},
		},
	}, map[string]string{
		"flow.yaml": "apiVersion: flow.foundry.io/v1\nkind: FoundryFlow\nmetadata:\n  name: placeholder\n",
	})

	var stdout, stderr bytes.Buffer
	result, err := InstallFlow(context.Background(), k8s, InstallOptions{
		Source:   dir,
		FlowName: testFlowName,
		Force:    true,
		Yes:      true,
	}, &stdout, &stderr, strings.NewReader(""))
	if err != nil {
		t.Fatalf("InstallFlow: %v", err)
	}
	if result.Created != 1 {
		t.Errorf("Created: got %d, want 1", result.Created)
	}
	if result.Failed != 0 {
		t.Errorf("Failed: got %d, want 0", result.Failed)
	}
}

// ─── T4: --force with stuck namespace ─────────────────────────────────────

func TestInstall_ForceStuckNamespace(t *testing.T) {
	// Create a fake client with a stuck namespace
	scheme := installFakeScheme()
	dyn := dynamicfake.NewSimpleDynamicClient(scheme)
	crdClient := crdfake.NewClientBuilder().WithScheme(scheme).Build()
	core := &stuckCoreClient{
		Clientset:     k8sfake.NewSimpleClientset(),
		namespaceName: testFlowName,
	}
	k8s := &api.K8sClient{CoreClient: core, CRDClient: crdClient, DynamicClient: dyn}

	dir := writeTestDir(t, &Manifest{
		Name:    testFlowName,
		Version: "1.0.0",
		Schemas: []string{"flow.foundry.io/v1"},
		Resources: []ManifestResource{
			{Path: "flow.yaml", Kind: "FoundryFlow"},
		},
	}, map[string]string{
		"flow.yaml": "apiVersion: flow.foundry.io/v1\nkind: FoundryFlow\nmetadata:\n  name: placeholder\n",
	})

	var stdout, stderr bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	_, err := InstallFlow(ctx, k8s, InstallOptions{
		Source:        dir,
		FlowName:      testFlowName,
		Force:         true,
		Yes:           true,
		PollInterval:  10 * time.Millisecond,
		PollTimeout:   200 * time.Millisecond,
	}, &stdout, &stderr, strings.NewReader(""))
	if err == nil {
		t.Fatal("expected error for stuck namespace, got nil")
	}
	if !strings.Contains(err.Error(), "stuck terminating") {
		t.Errorf("error should contain 'stuck terminating', got: %v", err)
	}
}

// ─── T5: --dry-run prints YAML only ──────────────────────────────────────

func TestInstall_DryRunPrintsYAML(t *testing.T) {
	dir := writeTestDir(t, &Manifest{
		Name:    testFlowName,
		Version: "1.0.0",
		Schemas: []string{"flow.foundry.io/v1"},
		Resources: []ManifestResource{
			{Path: "flow.yaml", Kind: "FoundryFlow"},
		},
	}, map[string]string{
		"flow.yaml": "apiVersion: flow.foundry.io/v1\nkind: FoundryFlow\nmetadata:\n  name: placeholder\n",
	})

	k8s := newInstallFakeClient(t, nil)
	var stdout, stderr bytes.Buffer
	result, err := InstallFlow(context.Background(), k8s, InstallOptions{
		Source:   dir,
		FlowName: testFlowName,
		DryRun:   true,
	}, &stdout, &stderr, strings.NewReader(""))
	if err != nil {
		t.Fatalf("InstallFlow: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if result.FlowName != testFlowName {
		t.Errorf("FlowName: got %q, want %q", result.FlowName, testFlowName)
	}
	// Verify YAML was printed to stdout with the correct namespace
	output := stdout.String()
	if !strings.Contains(output, "namespace: "+testFlowName) {
		t.Errorf("stdout should contain rewritten namespace, got:\n%s", output)
	}
	if !strings.Contains(output, "name: "+testFlowName) {
		t.Errorf("stdout should contain rewritten name, got:\n%s", output)
	}

	// Verify no namespace was created
	_, err = k8s.CoreClient.CoreV1().Namespaces().Get(context.Background(), testFlowName, metav1.GetOptions{})
	if !apierrors.IsNotFound(err) {
		t.Error("namespace should not exist after dry-run")
	}
}

// ─── T6: --dry-run + --force does NOT delete namespace ────────────────────

func TestInstall_DryRunPrecedenceOverForce(t *testing.T) {
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: testFlowName}}
	flowObj := makeFlow(testFlowName, testFlowName)
	k8s := newInstallFakeClient(t, []runtime.Object{flowObj}, ns)

	dir := writeTestDir(t, &Manifest{
		Name:    testFlowName,
		Version: "1.0.0",
		Schemas: []string{"flow.foundry.io/v1"},
		Resources: []ManifestResource{
			{Path: "flow.yaml", Kind: "FoundryFlow"},
		},
	}, map[string]string{
		"flow.yaml": "apiVersion: flow.foundry.io/v1\nkind: FoundryFlow\nmetadata:\n  name: placeholder\n",
	})

	var stdout, stderr bytes.Buffer
	result, err := InstallFlow(context.Background(), k8s, InstallOptions{
		Source:   dir,
		FlowName: testFlowName,
		DryRun:   true,
		Force:    true,
		Yes:      true,
	}, &stdout, &stderr, strings.NewReader(""))
	if err != nil {
		t.Fatalf("InstallFlow: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}

	// Namespace should still exist (dry-run should prevent deletion)
	ns, err = k8s.CoreClient.CoreV1().Namespaces().Get(context.Background(), testFlowName, metav1.GetOptions{})
	if err != nil {
		t.Errorf("namespace should still exist after dry-run: %v", err)
	}
	if ns.Name != testFlowName {
		t.Errorf("namespace name mismatch: got %q, want %q", ns.Name, testFlowName)
	}

	// YAML should be printed
	output := stdout.String()
	if !strings.Contains(output, "namespace: "+testFlowName) {
		t.Errorf("stdout should contain rewritten namespace, got:\n%s", output)
	}
}

// ─── T7: Missing CRD check ───────────────────────────────────────────────

func TestInstall_MissingCRDs(t *testing.T) {
	dir := writeTestDir(t, &Manifest{
		Name:    testFlowName,
		Version: "1.0.0",
		Schemas: []string{"nonexistent.io/v1"},
		Resources: []ManifestResource{
			{Path: "flow.yaml", Kind: "FoundryFlow"},
		},
	}, map[string]string{
		"flow.yaml": "apiVersion: flow.foundry.io/v1\nkind: FoundryFlow\nmetadata:\n  name: placeholder\n",
	})

	// Use a core client with error-injecting discovery
	core := &crdCheckCoreClient{
		Clientset:     k8sfake.NewSimpleClientset(),
		missingGroups: map[string]bool{"nonexistent.io/v1": true},
	}
	scheme := installFakeScheme()
	dyn := dynamicfake.NewSimpleDynamicClient(scheme)
	crdClient := crdfake.NewClientBuilder().WithScheme(scheme).Build()
	k8s := &api.K8sClient{CoreClient: core, CRDClient: crdClient, DynamicClient: dyn}

	var stdout, stderr bytes.Buffer
	_, err := InstallFlow(context.Background(), k8s, InstallOptions{
		Source:   dir,
		FlowName: testFlowName,
	}, &stdout, &stderr, strings.NewReader(""))
	if err == nil {
		t.Fatal("expected error for missing CRDs, got nil")
	}
	if !strings.Contains(err.Error(), "CRDs not found") {
		t.Errorf("error should contain 'CRDs not found', got: %v", err)
	}
}

// ─── T8: Partial failure reporting ────────────────────────────────────────

func TestInstall_PartialFailure(t *testing.T) {
	dir := writeTestDir(t, &Manifest{
		Name:    testFlowName,
		Version: "1.0.0",
		Schemas: []string{"flow.foundry.io/v1"},
		Resources: []ManifestResource{
			{Path: "artefacts.yaml", Kind: "GovernedArtefact"},
			{Path: "flow.yaml", Kind: "FoundryFlow"},
		},
	}, map[string]string{
		"artefacts.yaml": "apiVersion: flow.foundry.io/v1\nkind: GovernedArtefact\nmetadata:\n  name: haiku\n",
		"flow.yaml":      "apiVersion: flow.foundry.io/v1\nkind: FoundryFlow\nmetadata:\n  name: placeholder\n",
	})

	// Use ready discovery client + wrap dynamic client to fail on "my-flow" FoundryFlow
	scheme := installFakeScheme()
	baseDyn := dynamicfake.NewSimpleDynamicClient(scheme)
	failingDyn := &partialFailDynamicClient{
		Interface:    baseDyn,
		failResource: testFlowName, // the FoundryFlow name after rewriting is testFlowName
	}
	core := &readyDiscoveryCoreClient{Clientset: k8sfake.NewSimpleClientset()}
	crdClient := crdfake.NewClientBuilder().WithScheme(scheme).Build()
	k8s := &api.K8sClient{CoreClient: core, CRDClient: crdClient, DynamicClient: failingDyn}

	var stdout, stderr bytes.Buffer
	result, err := InstallFlow(context.Background(), k8s, InstallOptions{
		Source:   dir,
		FlowName: testFlowName,
	}, &stdout, &stderr, strings.NewReader(""))
	if err == nil {
		t.Fatal("expected partial failure error")
	}
	if !strings.Contains(err.Error(), "partial failure") {
		t.Errorf("error should contain 'partial failure', got: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	// GovernedArtefact should be created, FoundryFlow should fail
	if result.Created != 1 {
		t.Errorf("Created: got %d, want 1 (GovernedArtefact)", result.Created)
	}
	if result.Failed != 1 {
		t.Errorf("Failed: got %d, want 1 (FoundryFlow)", result.Failed)
	}
	if len(result.Errors) != 1 {
		t.Errorf("Errors: got %d, want 1", len(result.Errors))
	}
}

// ─── T9: Malformed tgz rejection ─────────────────────────────────────────

func TestInstall_MalformedTGZ(t *testing.T) {
	// Create a corrupt file (not a valid gzip)
	dir := t.TempDir()
	badPath := filepath.Join(dir, "bad.tgz")
	if err := os.WriteFile(badPath, []byte("not a gzip file"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	k8s := newInstallFakeClient(t, nil)
	var stdout, stderr bytes.Buffer
	_, err := InstallFlow(context.Background(), k8s, InstallOptions{
		Source:   badPath,
		FlowName: testFlowName,
	}, &stdout, &stderr, strings.NewReader(""))
	if err == nil {
		t.Fatal("expected error for malformed tgz, got nil")
	}
	if !strings.Contains(err.Error(), "invalid flow package") {
		t.Errorf("error should contain 'invalid flow package', got: %v", err)
	}
}

// ─── T10: Verify rewritten fields ─────────────────────────────────────────

func TestInstall_RewrittenFields(t *testing.T) {
	dir := writeTestDir(t, &Manifest{
		Name:    testFlowName,
		Version: "1.0.0",
		Schemas: []string{"flow.foundry.io/v1"},
		Resources: []ManifestResource{
			{Path: "flow.yaml", Kind: "FoundryFlow"},
			{Path: "artefacts.yaml", Kind: "GovernedArtefact"},
			{Path: "nodes.yaml", Kind: "FoundryNode"},
		},
	}, map[string]string{
		"flow.yaml":     "apiVersion: flow.foundry.io/v1\nkind: FoundryFlow\nmetadata:\n  name: placeholder\n",
		"artefacts.yaml": "apiVersion: flow.foundry.io/v1\nkind: GovernedArtefact\nmetadata:\n  name: haiku\n",
		"nodes.yaml":   "apiVersion: flow.foundry.io/v1\nkind: FoundryNode\nmetadata:\n  name: forge\n  labels:\n    flow.foundry.io/flow-name: placeholder\n",
	})

	k8s := newInstallFakeClient(t, nil)
	var stdout, stderr bytes.Buffer
	result, err := InstallFlow(context.Background(), k8s, InstallOptions{
		Source:   dir,
		FlowName: testFlowName,
	}, &stdout, &stderr, strings.NewReader(""))
	if err != nil {
		t.Fatalf("InstallFlow: %v", err)
	}
	if result.Failed != 0 {
		t.Fatalf("unexpected failures: %v", result.Errors)
	}

	dyn := k8s.DynamicClient
	gvr := schema.GroupVersionResource{Group: "flow.foundry.io", Version: "v1", Resource: "foundryflows"}

	// FoundryFlow .metadata.name == "my-flow"
	flow, err := dyn.Resource(gvr).Namespace(testFlowName).Get(context.Background(), testFlowName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("Get FoundryFlow: %v", err)
	}
	if flow.GetName() != testFlowName {
		t.Errorf("FoundryFlow name: got %q, want %q", flow.GetName(), testFlowName)
	}
	if flow.GetNamespace() != testFlowName {
		t.Errorf("FoundryFlow namespace: got %q, want %q", flow.GetNamespace(), testFlowName)
	}

	// GovernedArtefact .metadata.name is unchanged
	artGVR := schema.GroupVersionResource{Group: "flow.foundry.io", Version: "v1", Resource: "governedartefacts"}
	art, err := dyn.Resource(artGVR).Namespace(testFlowName).Get(context.Background(), "haiku", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("Get GovernedArtefact: %v", err)
	}
	if art.GetName() != "haiku" {
		t.Errorf("GovernedArtefact name should remain 'haiku', got %q", art.GetName())
	}
	if art.GetNamespace() != testFlowName {
		t.Errorf("GovernedArtefact namespace: got %q, want %q", art.GetNamespace(), testFlowName)
	}

	// FoundryNode should have flow-name label
	nodeGVR := schema.GroupVersionResource{Group: "flow.foundry.io", Version: "v1", Resource: "foundrynodes"}
	node, err := dyn.Resource(nodeGVR).Namespace(testFlowName).Get(context.Background(), "forge", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("Get FoundryNode: %v", err)
	}
	labels := node.GetLabels()
	if labels == nil || labels["flow.foundry.io/flow-name"] != testFlowName {
		t.Errorf("FoundryNode label flow.foundry.io/flow-name: got %q, want %q", labels["flow.foundry.io/flow-name"], testFlowName)
	}
}

// ─── T11: Verify non-rewritten fields ─────────────────────────────────────

func TestInstall_NonRewrittenFields(t *testing.T) {
	dir := writeTestDir(t, &Manifest{
		Name:    testFlowName,
		Version: "1.0.0",
		Schemas: []string{"flow.foundry.io/v1"},
		Resources: []ManifestResource{
			{Path: "nodes.yaml", Kind: "FoundryNode"},
			{Path: "configmaps.yaml", Kind: "ConfigMap"},
		},
	}, map[string]string{
		"nodes.yaml":     "apiVersion: flow.foundry.io/v1\nkind: FoundryNode\nmetadata:\n  name: sort\n  labels:\n    flow.foundry.io/flow-name: placeholder\nspec:\n  entry: sort\n  outputs:\n    - target: appraise\n",
		"configmaps.yaml": "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: app-config\ndata:\n  key: value\n",
	})

	k8s := newInstallFakeClient(t, nil)
	var stdout, stderr bytes.Buffer
	result, err := InstallFlow(context.Background(), k8s, InstallOptions{
		Source:   dir,
		FlowName: testFlowName,
	}, &stdout, &stderr, strings.NewReader(""))
	if err != nil {
		t.Fatalf("InstallFlow: %v", err)
	}
	if result.Failed != 0 {
		t.Fatalf("unexpected failures: %v", result.Errors)
	}

	dyn := k8s.DynamicClient
	nodeGVR := schema.GroupVersionResource{Group: "flow.foundry.io", Version: "v1", Resource: "foundrynodes"}

	// FoundryNode .spec.outputs[0].target should still be "appraise"
	node, err := dyn.Resource(nodeGVR).Namespace(testFlowName).Get(context.Background(), "sort", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("Get FoundryNode: %v", err)
	}
	outputs, found, err := unstructured.NestedSlice(node.Object, "spec", "outputs")
	if err != nil {
		t.Fatalf("NestedSlice: %v", err)
	}
	if !found || len(outputs) == 0 {
		t.Fatal("outputs field not found or empty")
	}
	outputMap, ok := outputs[0].(map[string]interface{})
	if !ok {
		t.Fatalf("outputs[0] is not a map, got %T", outputs[0])
	}
	target, _ := outputMap["target"].(string)
	if target != "appraise" {
		t.Errorf("output target: got %q, want 'appraise'", target)
	}

	// ConfigMap data should be unchanged
	cmGVR := schema.GroupVersionResource{Group: "", Version: "v1", Resource: "configmaps"}
	cm, err := dyn.Resource(cmGVR).Namespace(testFlowName).Get(context.Background(), "app-config", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("Get ConfigMap: %v", err)
	}
	data, ok, _ := unstructured.NestedString(cm.Object, "data", "key")
	if !ok || data != "value" {
		t.Errorf("ConfigMap data: got %q, want 'value'", data)
	}
}

// ─── T12: Directory source ────────────────────────────────────────────────

func TestInstall_DirectorySource(t *testing.T) {
	dir := writeTestDir(t, &Manifest{
		Name:    testFlowName,
		Version: "1.0.0",
		Schemas: []string{"flow.foundry.io/v1"},
		Resources: []ManifestResource{
			{Path: "flow.yaml", Kind: "FoundryFlow"},
		},
	}, map[string]string{
		"flow.yaml": "apiVersion: flow.foundry.io/v1\nkind: FoundryFlow\nmetadata:\n  name: placeholder\n",
	})

	k8s := newInstallFakeClient(t, nil)
	var stdout, stderr bytes.Buffer
	result, err := InstallFlow(context.Background(), k8s, InstallOptions{
		Source:   dir,
		FlowName: testFlowName,
	}, &stdout, &stderr, strings.NewReader(""))
	if err != nil {
		t.Fatalf("InstallFlow: %v", err)
	}
	if result.Created != 1 {
		t.Errorf("Created: got %d, want 1", result.Created)
	}
}

// ─── T13: Directory source without manifest.yaml ─────────────────────────

func TestInstall_DirectorySourceNoManifest(t *testing.T) {
	dir := t.TempDir()
	// Create empty directory (no manifest.yaml)

	k8s := newInstallFakeClient(t, nil)
	var stdout, stderr bytes.Buffer
	_, err := InstallFlow(context.Background(), k8s, InstallOptions{
		Source:   dir,
		FlowName: testFlowName,
	}, &stdout, &stderr, strings.NewReader(""))
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "not a valid flow package") {
		t.Errorf("error should contain 'not a valid flow package', got: %v", err)
	}
}

// ─── T14: Owner/repo shorthand expansion ─────────────────────────────────

func TestInstall_OwnerRepoShorthand(t *testing.T) {
	url, ok := ExpandGitHubShorthand("owner/repo")
	if !ok {
		t.Fatal("expected ok=true for shorthand")
	}
	if url != "https://github.com/owner/repo.git" {
		t.Errorf("URL: got %q, want %q", url, "https://github.com/owner/repo.git")
	}

	// With .git suffix (should strip it to avoid double .git)
	url, ok = ExpandGitHubShorthand("owner/repo.git")
	if !ok {
		t.Fatal("expected ok=true for shorthand with .git")
	}
	if url != "https://github.com/owner/repo.git" {
		t.Errorf("URL with .git: got %q, want %q", url, "https://github.com/owner/repo.git")
	}
}

// T14b: Non-shorthand returns input unchanged
func TestInstall_NonShorthandInput(t *testing.T) {
	// URL with :// should not be expanded
	input := "https://example.com/foo.git"
	url, ok := ExpandGitHubShorthand(input)
	if ok {
		t.Fatal("expected ok=false for URL with ://")
	}
	if url != input {
		t.Errorf("URL: got %q, want %q", url, input)
	}

	// Existing directory should not be expanded
	dir := t.TempDir()
	url, ok = ExpandGitHubShorthand(dir)
	if ok {
		t.Fatal("expected ok=false for existing directory")
	}
	if url != dir {
		t.Errorf("URL: got %q, want %q", url, dir)
	}
}

// ─── T15: Git clone from local repo ───────────────────────────────────────

func TestInstall_GitClone(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed, skipping git clone test")
	}

	server := newGitRepoServer(t, "", &Manifest{
		Name:    "test-flow",
		Version: "1.0.0",
		Schemas: []string{"flow.foundry.io/v1"},
		Resources: []ManifestResource{
			{Path: "flow.yaml", Kind: "FoundryFlow"},
		},
	}, map[string]string{
		"flow.yaml": "apiVersion: flow.foundry.io/v1\nkind: FoundryFlow\nmetadata:\n  name: placeholder\n",
	})
	defer server.Close()

	k8s := newInstallFakeClient(t, nil)
	var stdout, stderr bytes.Buffer
	result, err := InstallFlow(context.Background(), k8s, InstallOptions{
		Source:   server.URL,
		FlowName: testFlowName,
	}, &stdout, &stderr, strings.NewReader(""))
	if err != nil {
		t.Fatalf("InstallFlow: %v", err)
	}
	if result.Created != 1 {
		t.Errorf("Created: got %d, want 1", result.Created)
	}
}

// ─── T16: Git clone with --ref ────────────────────────────────────────────

func TestInstall_GitCloneWithRef(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed, skipping git clone test")
	}

	server := newGitRepoServer(t, "develop", &Manifest{
		Name:    "test-flow",
		Version: "1.0.0",
		Schemas: []string{"flow.foundry.io/v1"},
		Resources: []ManifestResource{
			{Path: "flow.yaml", Kind: "FoundryFlow"},
		},
	}, map[string]string{
		"flow.yaml": "apiVersion: flow.foundry.io/v1\nkind: FoundryFlow\nmetadata:\n  name: placeholder\n",
	})
	defer server.Close()

	k8s := newInstallFakeClient(t, nil)
	var stdout, stderr bytes.Buffer
	result, err := InstallFlow(context.Background(), k8s, InstallOptions{
		Source:   server.URL,
		FlowName: testFlowName,
		Ref:      "develop",
	}, &stdout, &stderr, strings.NewReader(""))
	if err != nil {
		t.Fatalf("InstallFlow: %v", err)
	}
	if result.Created != 1 {
		t.Errorf("Created: got %d, want 1", result.Created)
	}
}

// ─── T17: Git clone missing manifest.yaml ─────────────────────────────────

func TestInstall_GitCloneNoManifest(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed, skipping git clone test")
	}

	// Create a repo with no manifest.yaml (just a random YAML file)
	srcDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(srcDir, "somefile.yaml"), []byte("kind: ConfigMap"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	runGit(t, srcDir, "init")
	runGit(t, srcDir, "add", ".")
	runGit(t, srcDir, "commit", "-m", "init")

	bareDir := t.TempDir()
	runGit(t, "", "clone", "--bare", srcDir, bareDir)

	url := "file://" + bareDir

	k8s := newInstallFakeClient(t, nil)
	var stdout, stderr bytes.Buffer
	_, err := InstallFlow(context.Background(), k8s, InstallOptions{
		Source:   url,
		FlowName: testFlowName,
	}, &stdout, &stderr, strings.NewReader(""))
	if err == nil {
		t.Fatal("expected error for missing manifest.yaml, got nil")
	}
	if !strings.Contains(err.Error(), "not a valid flow package") {
		t.Errorf("error should contain 'not a valid flow package', got: %v", err)
	}
}

// ─── T18: Git not installed error ────────────────────────────────────────

func TestInstall_GitNotInstalled(t *testing.T) {
	// Override execLookPath to simulate git not found
	oldLookPath := execLookPath
	execLookPath = func(name string) (string, error) {
		if name == "git" {
			return "", exec.ErrNotFound
		}
		return oldLookPath(name)
	}
	defer func() { execLookPath = oldLookPath }()

	k8s := newInstallFakeClient(t, nil)
	var stdout, stderr bytes.Buffer
	_, err := InstallFlow(context.Background(), k8s, InstallOptions{
		Source:   "https://github.com/owner/repo.git",
		FlowName: testFlowName,
	}, &stdout, &stderr, strings.NewReader(""))
	if err == nil {
		t.Fatal("expected error for git not installed, got nil")
	}
	if !strings.Contains(err.Error(), "git is required") {
		t.Errorf("error should contain 'git is required', got: %v", err)
	}
}

// ─── T19: Unrecognized source error ──────────────────────────────────────

func TestInstall_UnrecognizedSource(t *testing.T) {
	k8s := newInstallFakeClient(t, nil)
	var stdout, stderr bytes.Buffer
	_, err := InstallFlow(context.Background(), k8s, InstallOptions{
		Source:   "something.unknown",
		FlowName: testFlowName,
	}, &stdout, &stderr, strings.NewReader(""))
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "unrecognized source") {
		t.Errorf("error should contain 'unrecognized source', got: %v", err)
	}
}

// ─── T20: Git repo server unavailable ────────────────────────────────────

func TestInstall_GitCloneFailure(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed, skipping git clone test")
	}

	// Use a file:// URL that doesn't exist
	k8s := newInstallFakeClient(t, nil)
	var stdout, stderr bytes.Buffer
	_, err := InstallFlow(context.Background(), k8s, InstallOptions{
		Source:   "file:///nonexistent/path/to/repo",
		FlowName: testFlowName,
	}, &stdout, &stderr, strings.NewReader(""))
	if err == nil {
		t.Fatal("expected error for clone failure, got nil")
	}
	if !strings.Contains(err.Error(), "failed to clone repository") {
		t.Errorf("error should contain 'failed to clone repository', got: %v", err)
	}
}

// ─── Filter tests ─────────────────────────────────────────────────────────

func TestInstall_FiltersSecrets(t *testing.T) {
	dir := writeTestDir(t, &Manifest{
		Name:    testFlowName,
		Version: "1.0.0",
		Schemas: []string{"flow.foundry.io/v1"},
		Resources: []ManifestResource{
			{Path: "resources.yaml", Kind: "GovernedArtefact"},
			{Path: "secrets.yaml", Kind: "Secret"},
		},
	}, map[string]string{
		"resources.yaml": "apiVersion: flow.foundry.io/v1\nkind: GovernedArtefact\nmetadata:\n  name: haiku\n",
		"secrets.yaml":   "apiVersion: v1\nkind: Secret\nmetadata:\n  name: my-secret\ndata:\n  password: hunter2\n",
	})

	k8s := newInstallFakeClient(t, nil)
	var stdout, stderr bytes.Buffer
	result, err := InstallFlow(context.Background(), k8s, InstallOptions{
		Source:   dir,
		FlowName: testFlowName,
	}, &stdout, &stderr, strings.NewReader(""))
	if err != nil {
		t.Fatalf("InstallFlow: %v", err)
	}
	if result.Created != 1 {
		t.Errorf("Created: got %d, want 1 (Secret should be filtered)", result.Created)
	}
	if !strings.Contains(stderr.String(), "skipped") {
		t.Errorf("stderr should contain 'skipped' for filtered Secret, got: %s", stderr.String())
	}
}

// ─── Apply order test ─────────────────────────────────────────────────────

func TestInstall_ApplyOrder(t *testing.T) {
	dir := writeTestDir(t, &Manifest{
		Name:    testFlowName,
		Version: "1.0.0",
		Schemas: []string{"flow.foundry.io/v1"},
		Resources: []ManifestResource{
			{Path: "stuff.yaml", Kind: "FoundryFlow"},
			{Path: "workitems.yaml", Kind: "Workitem"},
		},
	}, map[string]string{
		"stuff.yaml":     "apiVersion: flow.foundry.io/v1\nkind: FoundryFlow\nmetadata:\n  name: placeholder\n",
		"workitems.yaml": "apiVersion: flow.foundry.io/v1\nkind: Workitem\nmetadata:\n  name: wi-1\n",
	})

	k8s := newInstallFakeClient(t, nil)
	var stdout, stderr bytes.Buffer
	result, err := InstallFlow(context.Background(), k8s, InstallOptions{
		Source:   dir,
		FlowName: testFlowName,
	}, &stdout, &stderr, strings.NewReader(""))
	if err != nil {
		t.Fatalf("InstallFlow: %v", err)
	}
	if result.Created != 2 {
		t.Errorf("Created: got %d, want 2", result.Created)
	}
}

// ─── DNS label validation test ────────────────────────────────────────────

func TestInstall_InvalidFlowName(t *testing.T) {
	k8s := newInstallFakeClient(t, nil)
	var stdout, stderr bytes.Buffer
	_, err := InstallFlow(context.Background(), k8s, InstallOptions{
		Source:   writeTestDir(t, &Manifest{Name: "test", Version: "1.0.0", Schemas: []string{"flow.foundry.io/v1"}, Resources: []ManifestResource{{Path: "r.yaml", Kind: "ConfigMap"}}}, map[string]string{"r.yaml": "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: test\n"}),
		FlowName: "INVALID_UPPERCASE",
	}, &stdout, &stderr, strings.NewReader(""))
	if err == nil {
		t.Fatal("expected error for invalid flow name, got nil")
	}
	if !strings.Contains(err.Error(), "invalid namespace name") {
		t.Errorf("error should contain 'invalid namespace name', got: %v", err)
	}
}


