package flow_test

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	crdfake "sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/foundry/flow/tools/flowctl/internal/api"
	"github.com/foundry/flow/tools/flowctl/internal/flow"
)

// ─── Fake scheme helpers ─────────────────────────────────────────────────

// newPackageFakeScheme registers all CRD kinds needed for packaging
// (FoundryFlow, FoundryNode, GovernedArtefact, Law, LawGroup, Treaty)
// and their List variants as unstructured.
func newPackageFakeScheme() *runtime.Scheme {
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
	return scheme
}

// newPackageFakeClient creates a K8sClient wired to fake dynamic + core clients.
func newPackageFakeClient(t *testing.T, crdObjects []runtime.Object, coreObjects ...runtime.Object) *api.K8sClient {
	t.Helper()
	scheme := newPackageFakeScheme()
	dyn := dynamicfake.NewSimpleDynamicClient(scheme, crdObjects...)
	core := k8sfake.NewSimpleClientset(coreObjects...)
	crdClient := crdfake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(crdObjects...).Build()
	return api.NewK8sClientWithComponents(dyn, core, crdClient, scheme)
}

// ─── Unstructured builders ───────────────────────────────────────────────

func makeObj(apiVersion, kind, name, namespace string) *unstructured.Unstructured {
	obj := &unstructured.Unstructured{}
	obj.SetAPIVersion(apiVersion)
	obj.SetKind(kind)
	obj.SetName(name)
	obj.SetNamespace(namespace)
	return obj
}

func makeFlow(name, namespace string, entryContracts, exitContracts map[string]map[string]string) *unstructured.Unstructured {
	obj := makeObj("flow.foundry.io/v1", "FoundryFlow", name, namespace)
	if entryContracts != nil {
		_ = unstructured.SetNestedMap(obj.Object, toRawMap(entryContracts), "spec", "entryContracts")
	}
	if exitContracts != nil {
		_ = unstructured.SetNestedMap(obj.Object, toRawMap(exitContracts), "spec", "exitContracts")
	}
	return obj
}

func makeNode(name, namespace, flowName string) *unstructured.Unstructured {
	obj := makeObj("flow.foundry.io/v1", "FoundryNode", name, namespace)
	obj.SetLabels(map[string]string{"flow.foundry.io/flow-name": flowName})
	return obj
}

func makeArtefact(name, namespace string) *unstructured.Unstructured {
	return makeObj("flow.foundry.io/v1", "GovernedArtefact", name, namespace)
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

func toRawMap(m map[string]map[string]string) map[string]interface{} {
	raw := make(map[string]interface{}, len(m))
	for k, v := range m {
		inner := make(map[string]interface{}, len(v))
		for ik, iv := range v {
			inner[ik] = iv
		}
		raw[k] = inner
	}
	return raw
}

// ─── Tests ───────────────────────────────────────────────────────────────

const testFlowName = "haiku-flow"

// T10: Successful package with all resource types
func TestPackageFlow_AllResourceTypes(t *testing.T) {
	ns := testFlowName
	// Reference artefacts via contracts so they're collected
	contracts := map[string]map[string]string{
		"haiku":    {"type": "poem"},
		"petition": {"type": "request"},
	}
	var crds []runtime.Object
	crds = append(crds, makeFlow(testFlowName, ns, contracts, nil))
	crds = append(crds, makeNode("forge", ns, testFlowName))
	crds = append(crds, makeNode("sort", ns, testFlowName))
	crds = append(crds, makeNode("reviewer", ns, testFlowName))
	crds = append(crds, makeArtefact("haiku", ns))
	crds = append(crds, makeArtefact("petition", ns))
	crds = append(crds, makeLaw("law-1", ns))
	crds = append(crds, makeLawGroup("lg-1", ns))
	crds = append(crds, makeTreaty("treaty-1", ns))

	var cms []runtime.Object
	cms = append(cms, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "app-config", Namespace: ns}})
	cms = append(cms, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "log-config", Namespace: ns}})

	k8s := newPackageFakeClient(t, crds, cms...)

	dir := t.TempDir()
	outputPath := filepath.Join(dir, testFlowName+".tgz")

	result, err := flow.PackageFlow(context.Background(), k8s, flow.PackageOptions{
		FlowName:   testFlowName,
		OutputPath: outputPath,
		Version:    "0.0.0",
	})
	if err != nil {
		t.Fatalf("PackageFlow: %v", err)
	}

	if result.FlowName != testFlowName {
		t.Errorf("FlowName: got %q, want %q", result.FlowName, testFlowName)
	}
	if result.NodeCount != 3 {
		t.Errorf("NodeCount: got %d, want 3", result.NodeCount)
	}
	if result.TotalResources == 0 {
		t.Error("TotalResources should be > 0")
	}
	if result.FileCount != 7 {
		t.Errorf("FileCount: got %d, want 7", result.FileCount)
	}
}

// T11: TGZ contains correct file names
func TestPackageFlow_TGZFileNames(t *testing.T) {
	ns := testFlowName
	contracts := map[string]map[string]string{
		"haiku":    {"type": "poem"},
		"petition": {"type": "request"},
	}
	var crds []runtime.Object
	crds = append(crds, makeFlow(testFlowName, ns, contracts, nil))
	crds = append(crds, makeNode("forge", ns, testFlowName))
	crds = append(crds, makeNode("sort", ns, testFlowName))
	crds = append(crds, makeNode("reviewer", ns, testFlowName))
	crds = append(crds, makeArtefact("haiku", ns))
	crds = append(crds, makeArtefact("petition", ns))
	crds = append(crds, makeLaw("law-1", ns))
	crds = append(crds, makeLawGroup("lg-1", ns))
	crds = append(crds, makeTreaty("treaty-1", ns))

	var cms []runtime.Object
	cms = append(cms, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "app-config", Namespace: ns}})

	k8s := newPackageFakeClient(t, crds, cms...)

	dir := t.TempDir()
	outputPath := filepath.Join(dir, testFlowName+".tgz")
	if _, err := flow.PackageFlow(context.Background(), k8s, flow.PackageOptions{
		FlowName:   testFlowName,
		OutputPath: outputPath,
		Version:    "0.0.0",
	}); err != nil {
		t.Fatalf("PackageFlow: %v", err)
	}

	extracted, err := flow.ExtractTGZ(outputPath)
	if err != nil {
		t.Fatalf("ExtractTGZ: %v", err)
	}

	expected := []string{"manifest.yaml", "flow.yaml", "nodes.yaml", "governed_artefacts.yaml", "laws.yaml", "lawgroups.yaml", "treaties.yaml", "configmaps.yaml"}
	for _, name := range expected {
		if _, ok := extracted[name]; !ok {
			t.Errorf("missing file %q in archive", name)
		}
	}
	if len(extracted) != len(expected) {
		t.Errorf("expected %d files, got %d", len(expected), len(extracted))
	}
}

// T12: manifest.yaml validates
func TestPackageFlow_ManifestValidates(t *testing.T) {
	ns := testFlowName
	contracts := map[string]map[string]string{
		"haiku": {"type": "poem"},
	}
	var crds []runtime.Object
	crds = append(crds, makeFlow(testFlowName, ns, contracts, nil))
	crds = append(crds, makeNode("forge", ns, testFlowName))
	crds = append(crds, makeArtefact("haiku", ns))
	crds = append(crds, makeLaw("law-1", ns))
	crds = append(crds, makeLawGroup("lg-1", ns))
	crds = append(crds, makeTreaty("treaty-1", ns))

	var cms []runtime.Object
	cms = append(cms, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "app-config", Namespace: ns}})

	k8s := newPackageFakeClient(t, crds, cms...)

	dir := t.TempDir()
	outputPath := filepath.Join(dir, testFlowName+".tgz")
	if _, err := flow.PackageFlow(context.Background(), k8s, flow.PackageOptions{
		FlowName:   testFlowName,
		OutputPath: outputPath,
		Version:    "0.0.0",
	}); err != nil {
		t.Fatalf("PackageFlow: %v", err)
	}

	extracted, err := flow.ExtractTGZ(outputPath)
	if err != nil {
		t.Fatalf("ExtractTGZ: %v", err)
	}

	manifestData, ok := extracted["manifest.yaml"]
	if !ok {
		t.Fatal("manifest.yaml not found in archive")
	}

	manifest, err := flow.UnmarshalManifest(manifestData)
	if err != nil {
		t.Fatalf("UnmarshalManifest: %v", err)
	}
	if manifest.Name != testFlowName {
		t.Errorf("Name: got %q, want %q", manifest.Name, testFlowName)
	}
	if manifest.Version != "0.0.0" {
		t.Errorf("Version: got %q, want 0.0.0", manifest.Version)
	}
	if len(manifest.Resources) == 0 {
		t.Fatal("Resources list is empty")
	}

	// Check resources order follows install dependency order
	order := []string{"governed_artefacts.yaml", "flow.yaml", "configmaps.yaml", "nodes.yaml", "laws.yaml", "lawgroups.yaml", "treaties.yaml"}
	for i, res := range manifest.Resources {
		if i >= len(order) {
			break
		}
		if res.Path != order[i] {
			t.Errorf("resource[%d] path: got %q, want %q", i, res.Path, order[i])
		}
	}
}

// T13: Namespace stripped from YAML
func TestPackageFlow_NamespaceStripped(t *testing.T) {
	ns := testFlowName
	var crds []runtime.Object
	crds = append(crds, makeFlow(testFlowName, ns, nil, nil))
	crds = append(crds, makeNode("forge", ns, testFlowName))

	k8s := newPackageFakeClient(t, crds)

	dir := t.TempDir()
	outputPath := filepath.Join(dir, testFlowName+".tgz")
	if _, err := flow.PackageFlow(context.Background(), k8s, flow.PackageOptions{
		FlowName:   testFlowName,
		OutputPath: outputPath,
	}); err != nil {
		t.Fatalf("PackageFlow: %v", err)
	}

	extracted, err := flow.ExtractTGZ(outputPath)
	if err != nil {
		t.Fatalf("ExtractTGZ: %v", err)
	}

	flowYAML := string(extracted["flow.yaml"])
	// Should not contain namespace, resourceVersion, uid, creationTimestamp, or status
	for _, field := range []string{"namespace:", "resourceVersion:", "uid:", "creationTimestamp:", "status:"} {
		if strings.Contains(flowYAML, field) {
			t.Errorf("flow.yaml should not contain %q after normalization", field)
		}
	}
}

// T14: Multi-doc files contain all resources of kind
func TestPackageFlow_MultiDoc(t *testing.T) {
	ns := testFlowName
	var crds []runtime.Object
	crds = append(crds, makeFlow(testFlowName, ns, nil, nil))
	crds = append(crds, makeNode("forge", ns, testFlowName))
	crds = append(crds, makeNode("sort", ns, testFlowName))
	crds = append(crds, makeNode("reviewer", ns, testFlowName))
	crds = append(crds, makeLaw("law-1", ns))
	crds = append(crds, makeLaw("law-2", ns))

	var cms []runtime.Object
	cms = append(cms, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "cfg", Namespace: ns}})

	k8s := newPackageFakeClient(t, crds, cms...)

	dir := t.TempDir()
	outputPath := filepath.Join(dir, testFlowName+".tgz")
	if _, err := flow.PackageFlow(context.Background(), k8s, flow.PackageOptions{
		FlowName:   testFlowName,
		OutputPath: outputPath,
	}); err != nil {
		t.Fatalf("PackageFlow: %v", err)
	}

	extracted, err := flow.ExtractTGZ(outputPath)
	if err != nil {
		t.Fatalf("ExtractTGZ: %v", err)
	}

	nodesDocs, err := flow.ParseMultiDocYAML(extracted["nodes.yaml"])
	if err != nil {
		t.Fatalf("ParseMultiDocYAML nodes.yaml: %v", err)
	}
	if len(nodesDocs) != 3 {
		t.Errorf("nodes.yaml: expected 3 documents, got %d", len(nodesDocs))
	}

	lawsDocs, err := flow.ParseMultiDocYAML(extracted["laws.yaml"])
	if err != nil {
		t.Fatalf("ParseMultiDocYAML laws.yaml: %v", err)
	}
	if len(lawsDocs) != 2 {
		t.Errorf("laws.yaml: expected 2 documents, got %d", len(lawsDocs))
	}
}

// T15: Missing flow error
func TestPackageFlow_MissingFlow(t *testing.T) {
	k8s := newPackageFakeClient(t, nil)
	dir := t.TempDir()
	outputPath := filepath.Join(dir, "out.tgz")

	_, err := flow.PackageFlow(context.Background(), k8s, flow.PackageOptions{
		FlowName:   "nonexistent",
		OutputPath: outputPath,
	})
	if err == nil {
		t.Fatal("expected error for missing flow, got nil")
	}
	if !strings.Contains(err.Error(), "no FoundryFlow named") {
		t.Errorf("error should contain 'no FoundryFlow named', got: %v", err)
	}
}

// T16: No-nodes warning
func TestPackageFlow_NoNodesWarning(t *testing.T) {
	ns := testFlowName
	var crds []runtime.Object
	crds = append(crds, makeFlow(testFlowName, ns, nil, nil))

	k8s := newPackageFakeClient(t, crds)

	stderr := captureStderr(func() {
		dir := t.TempDir()
		outputPath := filepath.Join(dir, testFlowName+".tgz")
		_, err := flow.PackageFlow(context.Background(), k8s, flow.PackageOptions{
			FlowName:   testFlowName,
			OutputPath: outputPath,
			Version:    "0.0.0",
		})
		if err != nil {
			t.Fatalf("PackageFlow: %v", err)
		}
	})

	if !strings.Contains(stderr, "no nodes found") {
		t.Errorf("expected 'no nodes found' warning on stderr, got: %s", stderr)
	}
}

// T17: Missing contract artefact warning
func TestPackageFlow_MissingContractArtefact(t *testing.T) {
	ns := testFlowName
	entryContracts := map[string]map[string]string{
		"existing-artefact": {"key": "value"},
		"missing-artefact":  {"key2": "value2"},
	}
	var crds []runtime.Object
	crds = append(crds, makeFlow(testFlowName, ns, entryContracts, nil))
	crds = append(crds, makeNode("forge", ns, testFlowName))
	crds = append(crds, makeArtefact("existing-artefact", ns))

	k8s := newPackageFakeClient(t, crds)

	stderrOutput := captureStderr(func() {
		dir := t.TempDir()
		outputPath := filepath.Join(dir, testFlowName+".tgz")
		_, err := flow.PackageFlow(context.Background(), k8s, flow.PackageOptions{
			FlowName:   testFlowName,
			OutputPath: outputPath,
			Version:    "0.0.0",
		})
		if err != nil {
			t.Fatalf("PackageFlow: %v", err)
		}
	})

	if !strings.Contains(stderrOutput, "not found") {
		t.Errorf("expected 'not found' warning on stderr, got: %s", stderrOutput)
	}
}

// T18: Platform-injected ConfigMaps excluded
func TestPackageFlow_ExcludesCACertConfigMaps(t *testing.T) {
	ns := testFlowName
	var crds []runtime.Object
	crds = append(crds, makeFlow(testFlowName, ns, nil, nil))
	crds = append(crds, makeNode("forge", ns, testFlowName))

	var cms []runtime.Object
	cms = append(cms, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "app-config", Namespace: ns}})
	cms = append(cms, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "kube-root-ca.crt", Namespace: ns}})
	cms = append(cms, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "openshift-service-ca.crt", Namespace: ns}})
	cms = append(cms, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "logging-config", Namespace: ns}})

	k8s := newPackageFakeClient(t, crds, cms...)

	dir := t.TempDir()
	outputPath := filepath.Join(dir, testFlowName+".tgz")
	if _, err := flow.PackageFlow(context.Background(), k8s, flow.PackageOptions{
		FlowName:   testFlowName,
		OutputPath: outputPath,
	}); err != nil {
		t.Fatalf("PackageFlow: %v", err)
	}

	extracted, err := flow.ExtractTGZ(outputPath)
	if err != nil {
		t.Fatalf("ExtractTGZ: %v", err)
	}

	cmData := extracted["configmaps.yaml"]
	// Should contain exactly 2 documents (2 non-CA configmaps)
	docs, err := flow.ParseMultiDocYAML(cmData)
	if err != nil {
		t.Fatalf("ParseMultiDocYAML: %v", err)
	}
	if len(docs) != 2 {
		t.Errorf("expected 2 ConfigMap documents, got %d", len(docs))
	}
}

// T19: Secrets never exported (passive — PackageFlow never queries Secrets)
func TestPackageFlow_SecretsNotExported(t *testing.T) {
	ns := testFlowName
	var crds []runtime.Object
	crds = append(crds, makeFlow(testFlowName, ns, nil, nil))
	crds = append(crds, makeNode("forge", ns, testFlowName))

	var cms []runtime.Object
	cms = append(cms, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "app-config", Namespace: ns}})

	k8s := newPackageFakeClient(t, crds, cms...)

	dir := t.TempDir()
	outputPath := filepath.Join(dir, testFlowName+".tgz")
	if _, err := flow.PackageFlow(context.Background(), k8s, flow.PackageOptions{
		FlowName:   testFlowName,
		OutputPath: outputPath,
	}); err != nil {
		t.Fatalf("PackageFlow: %v", err)
	}

	extracted, err := flow.ExtractTGZ(outputPath)
	if err != nil {
		t.Fatalf("ExtractTGZ: %v", err)
	}

	// No file for Secrets, so the archive should not contain any secret data
	for name, data := range extracted {
		if strings.Contains(string(data), "my-secret") {
			t.Errorf("secret data found in %s, should not be exported", name)
		}
	}
}

// T20: --output-dir directory output
func TestPackageFlow_OutputDir(t *testing.T) {
	ns := testFlowName
	contracts := map[string]map[string]string{
		"haiku": {"type": "poem"},
	}
	var crds []runtime.Object
	crds = append(crds, makeFlow(testFlowName, ns, contracts, nil))
	crds = append(crds, makeNode("forge", ns, testFlowName))
	crds = append(crds, makeNode("sort", ns, testFlowName))
	crds = append(crds, makeNode("reviewer", ns, testFlowName))
	crds = append(crds, makeArtefact("haiku", ns))
	crds = append(crds, makeLaw("law-1", ns))
	crds = append(crds, makeLawGroup("lg-1", ns))
	crds = append(crds, makeTreaty("treaty-1", ns))

	var cms []runtime.Object
	cms = append(cms, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "app-config", Namespace: ns}})

	k8s := newPackageFakeClient(t, crds, cms...)

	outputDir := t.TempDir()
	result, err := flow.PackageFlow(context.Background(), k8s, flow.PackageOptions{
		FlowName:  testFlowName,
		OutputDir: outputDir,
		Version:   "0.0.0",
	})
	if err != nil {
		t.Fatalf("PackageFlow: %v", err)
	}

	if result.OutputDir != outputDir {
		t.Errorf("OutputDir: got %q, want %q", result.OutputDir, outputDir)
	}
	if result.OutputPath != "" {
		t.Errorf("OutputPath should be empty when OutputDir is set, got %q", result.OutputPath)
	}
	if result.NodeCount != 3 {
		t.Errorf("NodeCount: got %d, want 3", result.NodeCount)
	}

	// Check files exist in directory
	expected := []string{"manifest.yaml", "flow.yaml", "nodes.yaml", "governed_artefacts.yaml", "laws.yaml", "lawgroups.yaml", "treaties.yaml", "configmaps.yaml"}
	for _, name := range expected {
		path := filepath.Join(outputDir, name)
		if _, err := os.Stat(path); os.IsNotExist(err) {
			t.Errorf("file %s not found in output directory", path)
		}
	}

	// Verify .tgz is NOT created in the output dir
	tgzPath := filepath.Join(outputDir, testFlowName+".tgz")
	if _, err := os.Stat(tgzPath); !os.IsNotExist(err) {
		t.Error("output directory should not contain .tgz file")
	}
}

// T21: Mutual exclusivity error (both --output and --output-dir)
func TestPackageFlow_MutualExclusivity(t *testing.T) {
	k8s := newPackageFakeClient(t, nil)

	dir := t.TempDir()
	_, err := flow.PackageFlow(context.Background(), k8s, flow.PackageOptions{
		FlowName:   testFlowName,
		OutputPath: filepath.Join(dir, "out.tgz"),
		OutputDir:  dir,
	})
	if err == nil {
		t.Fatal("expected error for mutually exclusive options, got nil")
	}
	if !strings.Contains(err.Error(), "mutually exclusive") {
		t.Errorf("error should contain 'mutually exclusive', got: %v", err)
	}
}

// T22: NodeGroup contract artefacts collected
func TestPackageFlow_NodeGroupContracts(t *testing.T) {
	ns := testFlowName
	flowObj := makeFlow(testFlowName, ns, nil, nil)
	// Add nodeGroups with contracts
	_ = unstructured.SetNestedField(flowObj.Object, []interface{}{
		map[string]interface{}{
			"name": "group1",
			"entryContracts": map[string]interface{}{
				"node-artefact": map[string]interface{}{"key": "value"},
			},
			"exitContracts": map[string]interface{}{
				"node-artefact-2": map[string]interface{}{"key2": "value2"},
			},
		},
	}, "spec", "nodeGroups")

	var crds []runtime.Object
	crds = append(crds, flowObj)
	crds = append(crds, makeNode("forge", ns, testFlowName))
	crds = append(crds, makeArtefact("node-artefact", ns))
	crds = append(crds, makeArtefact("node-artefact-2", ns))

	k8s := newPackageFakeClient(t, crds)

	dir := t.TempDir()
	outputPath := filepath.Join(dir, testFlowName+".tgz")
	if _, err := flow.PackageFlow(context.Background(), k8s, flow.PackageOptions{
		FlowName:   testFlowName,
		OutputPath: outputPath,
	}); err != nil {
		t.Fatalf("PackageFlow: %v", err)
	}

	extracted, err := flow.ExtractTGZ(outputPath)
	if err != nil {
		t.Fatalf("ExtractTGZ: %v", err)
	}

	docs, err := flow.ParseMultiDocYAML(extracted["governed_artefacts.yaml"])
	if err != nil {
		t.Fatalf("ParseMultiDocYAML: %v", err)
	}
	if len(docs) != 2 {
		t.Errorf("expected 2 governed artefacts (from node group contracts), got %d", len(docs))
	}
}

// T23: Convention violation / flow not found when namespace != flow name
func TestPackageFlow_ConventionViolation(t *testing.T) {
	// Build an object with mismatched name/namespace.
	obj := makeObj("flow.foundry.io/v1", "FoundryFlow", "my-flow", "wrong-ns")

	var crds []runtime.Object
	crds = append(crds, obj)

	k8s := newPackageFakeClient(t, crds)

	dir := t.TempDir()
	// Query with flow name = "my-flow", which triggers GET in namespace "my-flow"
	// The object is stored in namespace "wrong-ns", so GET returns 404.
	_, err := flow.PackageFlow(context.Background(), k8s, flow.PackageOptions{
		FlowName:   "my-flow",
		OutputPath: filepath.Join(dir, "out.tgz"),
	})
	if err == nil {
		t.Fatal("expected error for flow not found, got nil")
	}
	if !strings.Contains(err.Error(), "no FoundryFlow named") {
		t.Errorf("expected 'no FoundryFlow named', got: %v", err)
	}
}

// T24: PackageFlow with --output-dir produces correct result
func TestPackageFlow_OutputDirOnly(t *testing.T) {
	ns := testFlowName
	var crds []runtime.Object
	crds = append(crds, makeFlow(testFlowName, ns, nil, nil))
	crds = append(crds, makeNode("forge", ns, testFlowName))

	k8s := newPackageFakeClient(t, crds)

	outputDir := t.TempDir()
	result, err := flow.PackageFlow(context.Background(), k8s, flow.PackageOptions{
		FlowName:  testFlowName,
		OutputDir: outputDir,
	})
	if err != nil {
		t.Fatalf("PackageFlow: %v", err)
	}
	if result.OutputDir != outputDir {
		t.Errorf("OutputDir: got %q, want %q", result.OutputDir, outputDir)
	}
	if result.OutputPath != "" {
		t.Errorf("OutputPath should be empty, got %q", result.OutputPath)
	}
}

// ─── Helpers ─────────────────────────────────────────────────────────────

// captureStderr runs f and returns any output written to stderr.
func captureStderr(f func()) string {
	buf := &bytes.Buffer{}
	return captureStderrWithBuf(buf, f)
}

func captureStderrWithBuf(buf *bytes.Buffer, f func()) string {
	old := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w

	f()

	w.Close()
	os.Stderr = old
	var outBuf bytes.Buffer
	outBuf.ReadFrom(r)
	return buf.String() + outBuf.String()
}

// Ensure test helper compiles
var _ = fmt.Sprintf
