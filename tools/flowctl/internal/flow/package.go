package flow

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"sigs.k8s.io/yaml"

	"github.com/foundry/flow/tools/flowctl/internal/api"
)

const (
	// ManifestFile is the name of the manifest file inside a flow package archive.
	ManifestFile = "manifest.yaml"

	// PackageExt is the file extension for flow package archives.
	PackageExt = ".tgz"
)

// PackageWriter creates flow package archives. Zero dependencies on
// Kubernetes — caller provides already-serialized resource data.
// Phase 03 populates this with live cluster data.
type PackageWriter struct {
	Name        string
	Version     string
	Description string
	Resources   map[string][]byte // filename -> serialized YAML
	KindIndex   map[string]string // filename -> Kubernetes kind
}

// Write writes the flow package archive (`.tgz`) to the given writer.
func (pw *PackageWriter) Write(w io.Writer) error {
	gzWriter := gzip.NewWriter(w)
	defer gzWriter.Close()

	tarWriter := tar.NewWriter(gzWriter)
	defer tarWriter.Close()

	// Build manifest
	manifest := Manifest{
		Name:        pw.Name,
		Version:     pw.Version,
		Description: pw.Description,
		Schemas:     []string{"flow.foundry.io/v1"},
	}

	// Sorted resource keys for deterministic output
	var keys []string
	for k := range pw.Resources {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, key := range keys {
		kind := pw.KindIndex[key]
		if kind == "" {
			return fmt.Errorf("package: missing kind for resource %q", key)
		}
		manifest.Resources = append(manifest.Resources, ManifestResource{
			Path: key,
			Kind: kind,
		})
	}

	// Marshal manifest
	manifestData, err := manifest.Marshal()
	if err != nil {
		return fmt.Errorf("package: failed to marshal manifest: %w", err)
	}

	// Write manifest.yaml entry
	if err := writeTarEntry(tarWriter, ManifestFile, manifestData); err != nil {
		return fmt.Errorf("package: failed to write manifest: %w", err)
	}

	// Write resource entries in order
	for _, key := range keys {
		if err := writeTarEntry(tarWriter, key, pw.Resources[key]); err != nil {
			return fmt.Errorf("package: failed to write resource %q: %w", key, err)
		}
	}

	return nil
}

// ─── GVR constants for flow.foundry.io CRDs ──────────────────────────────

var (
	flowGVR     = schema.GroupVersionResource{Group: "flow.foundry.io", Version: "v1", Resource: "foundryflows"}
	nodeGVR     = schema.GroupVersionResource{Group: "flow.foundry.io", Version: "v1", Resource: "foundrynodes"}
	artefactGVR = schema.GroupVersionResource{Group: "flow.foundry.io", Version: "v1", Resource: "governedartefacts"}
	lawGVR      = schema.GroupVersionResource{Group: "flow.foundry.io", Version: "v1", Resource: "laws"}
	lawGroupGVR = schema.GroupVersionResource{Group: "flow.foundry.io", Version: "v1", Resource: "lawgroups"}
	treatyGVR   = schema.GroupVersionResource{Group: "flow.foundry.io", Version: "v1", Resource: "treaties"}
)

// ─── PackageOptions ─────────────────────────────────────────────────────

// PackageOptions carries all CLI flags and the positional flow-name for
// `flowctl package <flow-name>`.
type PackageOptions struct {
	FlowName    string // positional: the flow to package
	OutputPath  string // --output, default "./<flow-name>.tgz"
	OutputDir   string // --output-dir, mutually exclusive with OutputPath
	Version     string // --version, default "0.0.0"
	Description string // --description
}

// PackageResult describes the outcome of a successful PackageFlow call.
type PackageResult struct {
	FlowName       string
	OutputPath     string // path to .tgz archive (empty if OutputDir used)
	OutputDir      string // path to output directory (empty if OutputPath used)
	TotalResources int    // count of individual resources packaged
	FileCount      int    // count of YAML files in the archive (excluding manifest.yaml)
	NodeCount      int    // count of FoundryNodes found
}

// ─── Kind-to-filename mapping ───────────────────────────────────────────

// kindFilename maps Kubernetes kind to the canonical YAML filename.
var kindFilename = map[string]string{
	"FoundryFlow":      "flow.yaml",
	"FoundryNode":      "nodes.yaml",
	"GovernedArtefact": "governed_artefacts.yaml",
	"Law":              "laws.yaml",
	"LawGroup":         "lawgroups.yaml",
	"Treaty":           "treaties.yaml",
	"ConfigMap":        "configmaps.yaml",
}

// resourceOrder defines the install dependency order for the manifest resources list.
var resourceOrder = []string{
	"governed_artefacts.yaml",
	"flow.yaml",
	"configmaps.yaml",
	"nodes.yaml",
	"laws.yaml",
	"lawgroups.yaml",
	"treaties.yaml",
}

// ─── checkFlowConvention ────────────────────────────────────────────────

// checkFlowConvention verifies that the fetched FoundryFlow's namespace matches
// the expected flow name. This is a belt-and-suspenders defensive check: we
// queried the correct namespace, but a bug or data corruption could cause the
// returned object to have a mismatched namespace.
func checkFlowConvention(obj *unstructured.Unstructured, flowName string) error {
	if obj.GetNamespace() != flowName {
		return fmt.Errorf("convention violation: flow `%s` must reside in namespace `%s`", obj.GetName(), flowName)
	}
	return nil
}

// ─── PackageFlow ────────────────────────────────────────────────────────

// PackageFlow discovers a running flow on the cluster, collects all associated
// resources, normalizes them, groups them by kind into multi-document YAML
// files, generates manifest.yaml, and creates a .tgz archive (or writes to a
// directory if opts.OutputDir is set).
//
// If opts.OutputDir is non-empty, normalized files are written to that
// directory instead of creating an archive. opts.OutputPath and opts.OutputDir
// are mutually exclusive — if both are set, PackageFlow returns an error.
func PackageFlow(ctx context.Context, k8s *api.K8sClient, opts PackageOptions) (*PackageResult, error) {
	if opts.OutputPath != "" && opts.OutputDir != "" {
		return nil, fmt.Errorf("--output and --output-dir are mutually exclusive")
	}
	if opts.OutputPath == "" && opts.OutputDir == "" {
		return nil, fmt.Errorf("either OutputPath or OutputDir must be set")
	}

	dyn := k8s.DynamicClient
	ns := opts.FlowName

	// Step 1: Discover FoundryFlow
	flowObj, err := dyn.Resource(flowGVR).Namespace(ns).Get(ctx, opts.FlowName, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("no FoundryFlow named `%s` in namespace `%s`", opts.FlowName, ns)
		}
		return nil, err
	}
	if err := checkFlowConvention(flowObj, opts.FlowName); err != nil {
		return nil, err
	}

	// Step 2: Extract contract artefact names from FoundryFlow spec
	artefactNames := extractArtefactNames(flowObj)

	// Step 3: Collect all resources
	var allResources []*unstructured.Unstructured

	// FoundryFlow itself
	allResources = append(allResources, flowObj)

	// FoundryNodes (with label selector)
	nodes, err := listResources(ctx, dyn, nodeGVR, ns, "flow.foundry.io/flow-name="+opts.FlowName)
	if err != nil {
		if apierrors.IsForbidden(err) {
			return nil, fmt.Errorf("permission denied listing foundrynodes: %w", err)
		}
		return nil, fmt.Errorf("failed to list foundrynodes: %w", err)
	}
	if len(nodes) == 0 {
		fmt.Fprintf(os.Stderr, "warning: no nodes found for flow `%s`\n", opts.FlowName)
	}
	allResources = append(allResources, nodes...)

	// GovernedArtefacts (from contract references)
	for _, name := range artefactNames {
		artefact, err := dyn.Resource(artefactGVR).Namespace(ns).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			if apierrors.IsNotFound(err) {
				fmt.Fprintf(os.Stderr, "warning: GovernedArtefact `%s` referenced in contracts but not found\n", name)
				continue
			}
			if apierrors.IsForbidden(err) {
				return nil, fmt.Errorf("permission denied listing governedartefacts: %w", err)
			}
			return nil, fmt.Errorf("failed to get governedartefact %q: %w", name, err)
		}
		allResources = append(allResources, artefact)
	}

	// Laws
	laws, err := listResources(ctx, dyn, lawGVR, ns, "")
	if err != nil {
		if apierrors.IsForbidden(err) {
			return nil, fmt.Errorf("permission denied listing laws: %w", err)
		}
		return nil, fmt.Errorf("failed to list laws: %w", err)
	}
	allResources = append(allResources, laws...)

	// LawGroups
	lawGroups, err := listResources(ctx, dyn, lawGroupGVR, ns, "")
	if err != nil {
		if apierrors.IsForbidden(err) {
			return nil, fmt.Errorf("permission denied listing lawgroups: %w", err)
		}
		return nil, fmt.Errorf("failed to list lawgroups: %w", err)
	}
	allResources = append(allResources, lawGroups...)

	// Treaties
	treaties, err := listResources(ctx, dyn, treatyGVR, ns, "")
	if err != nil {
		if apierrors.IsForbidden(err) {
			return nil, fmt.Errorf("permission denied listing treaties: %w", err)
		}
		return nil, fmt.Errorf("failed to list treaties: %w", err)
	}
	allResources = append(allResources, treaties...)

	// ConfigMaps (via core client)
	cmList, err := k8s.CoreClient.CoreV1().ConfigMaps(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		if apierrors.IsForbidden(err) {
			return nil, fmt.Errorf("permission denied listing configmaps: %w", err)
		}
		return nil, fmt.Errorf("failed to list configmaps: %w", err)
	}
	for i := range cmList.Items {
		cm := &cmList.Items[i]
		// Filter out platform-injected ConfigMaps (name ends with -ca.crt)
		if len(cm.Name) >= 7 && cm.Name[len(cm.Name)-7:] == "-ca.crt" {
			continue
		}
		u, err := configMapToUnstructured(cm)
		if err != nil {
			return nil, fmt.Errorf("failed to convert ConfigMap %q: %w", cm.Name, err)
		}
		allResources = append(allResources, u)
	}

	// Step 4: Normalize all resources
	for _, obj := range allResources {
		Normalize(obj)
	}

	// Step 5: Group by kind
	byKind := make(map[string][]*unstructured.Unstructured)
	for _, obj := range allResources {
		kind := obj.GetKind()
		byKind[kind] = append(byKind[kind], obj)
	}

	// Step 6: Sort each group by name for deterministic output
	for _, group := range byKind {
		sort.Slice(group, func(i, j int) bool {
			return group[i].GetName() < group[j].GetName()
		})
	}

	// Step 7: Build multi-doc YAML files
	files := make(map[string][]byte)
	var fileCount int

	// Process kinds in resource order for deterministic output
	kindOrder := []string{"GovernedArtefact", "FoundryFlow", "ConfigMap", "FoundryNode", "Law", "LawGroup", "Treaty"}
	totalResources := 0

	for _, kind := range kindOrder {
		group, ok := byKind[kind]
		if !ok || len(group) == 0 {
			continue
		}
		filename := kindFilename[kind]
		if filename == "" {
			continue
		}

		var yamlData []byte
		for _, obj := range group {
			var err error
			yamlData, err = AppendResource(yamlData, obj)
			if err != nil {
				return nil, fmt.Errorf("failed to marshal %s/%s: %w", kind, obj.GetName(), err)
			}
			totalResources++
		}
		files[filename] = yamlData
		fileCount++
	}

	// Step 8: Build manifest
	manifest := &Manifest{
		Name:        opts.FlowName,
		Version:     opts.Version,
		Description: opts.Description,
		Schemas:     []string{"flow.foundry.io/v1"},
	}

	// Build resources list in install dependency order
	for _, filename := range resourceOrder {
		if _, ok := files[filename]; !ok {
			continue
		}
		kind := ""
		for k, f := range kindFilename {
			if f == filename {
				kind = k
				break
			}
		}
		manifest.Resources = append(manifest.Resources, ManifestResource{
			Path: filename,
			Kind: kind,
		})
	}

	manifestData, err := manifest.Marshal()
	if err != nil {
		return nil, fmt.Errorf("failed to marshal manifest: %w", err)
	}

	// Step 9: Create archive or write directory
	result := &PackageResult{
		FlowName:       opts.FlowName,
		TotalResources: totalResources,
		FileCount:      fileCount,
		NodeCount:      len(nodes),
	}

	if opts.OutputDir != "" {
		if err := os.MkdirAll(opts.OutputDir, 0755); err != nil {
			return nil, fmt.Errorf("failed to create output directory: %w", err)
		}
		// Write manifest.yaml
		if err := os.WriteFile(filepath.Join(opts.OutputDir, ManifestFile), manifestData, 0644); err != nil {
			return nil, fmt.Errorf("failed to write manifest.yaml: %w", err)
		}
		// Write resource files
		for _, filename := range resourceOrder {
			data, ok := files[filename]
			if !ok {
				continue
			}
			if err := os.WriteFile(filepath.Join(opts.OutputDir, filename), data, 0644); err != nil {
				return nil, fmt.Errorf("failed to write %s: %w", filename, err)
			}
		}
		result.OutputDir = opts.OutputDir
	} else {
		// Build files map for archive (manifest + resources)
		archiveFiles := map[string][]byte{
			ManifestFile: manifestData,
		}
		for _, filename := range resourceOrder {
			data, ok := files[filename]
			if !ok {
				continue
			}
			archiveFiles[filename] = data
		}
		if err := CreateTGZ(opts.OutputPath, archiveFiles); err != nil {
			return nil, fmt.Errorf("failed to create package: %w", err)
		}
		result.OutputPath = opts.OutputPath
	}

	return result, nil
}

// ─── Helpers ────────────────────────────────────────────────────────────

// extractArtefactNames collects all GovernedArtefact names referenced in the
// FoundryFlow's entryContracts, exitContracts, and nodeGroup-level contracts.
func extractArtefactNames(flowObj *unstructured.Unstructured) []string {
	seen := make(map[string]bool)
	addContractKeys := func(fields ...string) {
		m, ok, _ := unstructured.NestedMap(flowObj.Object, fields...)
		if !ok {
			return
		}
		for k := range m {
			seen[k] = true
		}
	}

	// Top-level contracts
	addContractKeys("spec", "entryContracts")
	addContractKeys("spec", "exitContracts")

	// NodeGroup-level contracts (nodeGroups is an array, not a map)
	nodeGroups, ok, _ := unstructured.NestedSlice(flowObj.Object, "spec", "nodeGroups")
	if ok {
		for _, ng := range nodeGroups {
			ngMap, ok := ng.(map[string]interface{})
			if !ok {
				continue
			}
			addFromMap(ngMap, "entryContracts", seen)
			addFromMap(ngMap, "exitContracts", seen)
		}
	}

	result := make([]string, 0, len(seen))
	for name := range seen {
		result = append(result, name)
	}
	sort.Strings(result)
	return result
}

// addFromMap extracts map keys from a nested map[string]interface{} at the given key.
func addFromMap(m map[string]interface{}, key string, seen map[string]bool) {
	raw, ok := m[key]
	if !ok {
		return
	}
	sub, ok := raw.(map[string]interface{})
	if !ok {
		return
	}
	for k := range sub {
		seen[k] = true
	}
}

// listResources lists all resources of the given GVR in the namespace, optionally
// filtered by a label selector.
func listResources(ctx context.Context, dyn dynamic.Interface, gvr schema.GroupVersionResource, namespace, labelSelector string) ([]*unstructured.Unstructured, error) {
	opts := metav1.ListOptions{}
	if labelSelector != "" {
		opts.LabelSelector = labelSelector
	}
	list, err := dyn.Resource(gvr).Namespace(namespace).List(ctx, opts)
	if err != nil {
		// Check for forbidden errors explicitly
		return nil, err
	}
	result := make([]*unstructured.Unstructured, len(list.Items))
	for i := range list.Items {
		// Copy to avoid aliasing
		item := list.Items[i]
		result[i] = &item
	}
	return result, nil
}

// configMapToUnstructured converts a typed corev1.ConfigMap to an
// *unstructured.Unstructured for uniform handling.
// Always sets apiVersion: v1 and kind: ConfigMap because struct-literal
// corev1.ConfigMap objects have empty TypeMeta (the JSON tags use omitempty).
func configMapToUnstructured(cm *corev1.ConfigMap) (*unstructured.Unstructured, error) {
	data, err := yaml.Marshal(cm)
	if err != nil {
		return nil, err
	}
	var m map[string]interface{}
	if err := yaml.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	u := &unstructured.Unstructured{Object: m}
	// ponytail: struct-literal corev1.ConfigMap has empty TypeMeta; ensure
	// GVK is set so the resource is grouped by ConfigMap kind downstream.
	if u.GetKind() == "" {
		u.SetAPIVersion("v1")
		u.SetKind("ConfigMap")
	}
	return u, nil
}

// writeTarEntry writes a single file entry to the tar writer.
func writeTarEntry(tw *tar.Writer, name string, data []byte) error {
	hdr := &tar.Header{
		Name:     name,
		Size:     int64(len(data)),
		Mode:     0644,
		Typeflag: tar.TypeReg,
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return err
	}
	_, err := tw.Write(data)
	return err
}
