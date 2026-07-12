package flow

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/yaml"

	"github.com/foundry/flow/tools/flowctl/internal/api"
)

// execLookPath is a package-level variable so tests can override it
// to simulate "git not installed" (see T18).
var execLookPath = exec.LookPath

// InstallOptions carries all CLI flags and positional arguments for `flowctl install`.
type InstallOptions struct {
	Source   string // positional: <source> — directory, .tgz, git URL, or owner/repo
	FlowName string // positional: <flow-name>
	Force    bool   // --force: delete namespace before install
	Yes      bool   // --yes: skip confirmation prompt
	DryRun   bool   // --dry-run: print rewritten YAML, skip mutation
	Ref      string // --ref: branch or lightweight tag for git sources (default: HEAD of default branch)

	// PollInterval and PollTimeout override default poll timing for namespace deletion.
	// Zero values use defaults (2s interval, 120s timeout).
	PollInterval time.Duration
	PollTimeout  time.Duration
}

// InstallResult describes the outcome of InstallFlow.
type InstallResult struct {
	FlowName  string
	Created   int      // count of resources created
	Unchanged int      // count of resources that already exist
	Failed    int      // count of resources that failed to apply
	Errors    []string // individual resource errors (only if Failed > 0)
}

// resourceApplyResult is an internal type tracking per-resource outcome.
type resourceApplyResult struct {
	kind   string
	name   string
	status string // "created", "unchanged", "failed"
	err    error
}

// dnsLabelRe matches a valid DNS-1123 label.
var dnsLabelRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// shorthandRe matches an owner/repo shorthand pattern.
var shorthandRe = regexp.MustCompile(`^[a-zA-Z0-9._-]+/[a-zA-Z0-9._-]+$`)

// ─── GitHub Shorthand Expansion ───────────────────────────────────────────

// ExpandGitHubShorthand expands an "owner/repo" string to a GitHub HTTPS URL.
// If raw matches the pattern ^[a-zA-Z0-9._-]+/[a-zA-Z0-9._-]+$ (single slash,
// no ://, not an existing path) it returns "https://github.com/<owner>/<repo>.git"
// and true. A trailing ".git" is stripped from the repo part before URL
// construction to avoid a double ".git.git" suffix. Otherwise it returns raw
// unchanged with false.
func ExpandGitHubShorthand(raw string) (string, bool) {
	// Must match the owner/repo pattern
	if !shorthandRe.MatchString(raw) {
		return raw, false
	}
	// Must not contain :// (that would be a URL, not shorthand)
	if strings.Contains(raw, "://") {
		return raw, false
	}
	// Must not be an existing directory path
	if _, err := os.Stat(raw); err == nil {
		return raw, false
	}

	parts := strings.SplitN(raw, "/", 2)
	repo := strings.TrimSuffix(parts[1], ".git")
	return fmt.Sprintf("https://github.com/%s/%s.git", parts[0], repo), true
}

// ─── Source Resolution ───────────────────────────────────────────────────

// resolveSource resolves the source argument to a local working directory.
// It returns the directory path and a cleanup function (which may be nil if
// no temp dir was created). The caller must call cleanup when done.
func resolveSource(ctx context.Context, src, ref string) (workingDir string, cleanup func(), err error) {
	// 1. Existing directory
	if info, statErr := os.Stat(src); statErr == nil && info.IsDir() {
		return src, nil, nil
	}

	// 2. .tgz file
	if strings.HasSuffix(src, ".tgz") {
		files, err := ExtractTGZ(src)
		if err != nil {
			return "", nil, fmt.Errorf("invalid flow package: %w", err)
		}
		tmpDir, err := os.MkdirTemp("", "flowctl-*")
		if err != nil {
			return "", nil, fmt.Errorf("failed to create temp directory: %w", err)
		}
		cleanup = func() { os.RemoveAll(tmpDir) }
		for name, data := range files {
			if err := os.WriteFile(filepath.Join(tmpDir, name), data, 0644); err != nil {
				cleanup()
				return "", nil, fmt.Errorf("failed to extract archive: %w", err)
			}
		}
		return tmpDir, cleanup, nil
	}

	// 3. GitHub shorthand expansion
	expanded, ok := ExpandGitHubShorthand(src)
	if ok {
		src = expanded
		// Fall through to git clone
	}

	// 4. Git URL (contains ://)
	if strings.Contains(src, "://") {
		return cloneRepo(ctx, src, ref)
	}

	// 5. Unknown source type
	return "", nil, fmt.Errorf("unrecognized source: `%s`; use a directory, .tgz file, git URL, or owner/repo shorthand", src)
}

// cloneRepo clones a git repository into a temp directory and returns the path.
func cloneRepo(ctx context.Context, url, ref string) (workingDir string, cleanup func(), err error) {
	if _, err := execLookPath("git"); err != nil {
		return "", nil, fmt.Errorf("git is required to install from a repository URL; install git or use a .tgz file or local directory instead")
	}

	tmpDir, err := os.MkdirTemp("", "flowctl-*")
	if err != nil {
		return "", nil, fmt.Errorf("failed to create temp directory: %w", err)
	}
	cleanup = func() { os.RemoveAll(tmpDir) }

	args := []string{"clone", "--depth", "1"}
	if ref != "" {
		// ponytail: --branch with --depth 1 works for branches and lightweight tags.
		// For annotated tags or arbitrary commits, a full clone + fetch + checkout
		// would be needed, but the common use case is a named branch or tag.
		args = append(args, "--branch", ref)
	}
	args = append(args, url, tmpDir)

	cmd := exec.CommandContext(ctx, "git", args...)
	stderr, err := cmd.CombinedOutput()
	if err != nil {
		cleanup()
		return "", nil, fmt.Errorf("failed to clone repository: %s", strings.TrimSpace(string(stderr)))
	}

	return tmpDir, cleanup, nil
}

// ─── Kind Priority ──────────────────────────────────────────────────────

// kindPriority returns the GroupVersionResource, priority, and a boolean
// indicating whether the kind is known. Unknown kinds get priority 99.
func kindPriority(kind string) (schema.GroupVersionResource, int, bool) {
	switch kind {
	case "GovernedArtefact":
		return schema.GroupVersionResource{Group: "flow.foundry.io", Version: "v1", Resource: "governedartefacts"}, 1, true
	case "FoundryFlow":
		return schema.GroupVersionResource{Group: "flow.foundry.io", Version: "v1", Resource: "foundryflows"}, 2, true
	case "ConfigMap":
		return schema.GroupVersionResource{Group: "", Version: "v1", Resource: "configmaps"}, 3, true
	case "FoundryNode":
		return schema.GroupVersionResource{Group: "flow.foundry.io", Version: "v1", Resource: "foundrynodes"}, 4, true
	case "Law":
		return schema.GroupVersionResource{Group: "flow.foundry.io", Version: "v1", Resource: "laws"}, 5, true
	case "LawGroup":
		return schema.GroupVersionResource{Group: "flow.foundry.io", Version: "v1", Resource: "lawgroups"}, 5, true
	case "Treaty":
		return schema.GroupVersionResource{Group: "flow.foundry.io", Version: "v1", Resource: "treaties"}, 6, true
	case "Workitem":
		return schema.GroupVersionResource{Group: "flow.foundry.io", Version: "v1", Resource: "workitems"}, 7, true
	default:
		return schema.GroupVersionResource{}, 99, false
	}
}

// ─── Parsed Resource ────────────────────────────────────────────────────

// parsedResource holds a parsed unstructured object along with its source filename.
type parsedResource struct {
	obj      *unstructured.Unstructured
	filename string
}

// ─── InstallFlow ────────────────────────────────────────────────────────

// InstallFlow implements the `flowctl install <source> <flow-name>` verb.
// It resolves the source to a working directory (directory, extracted .tgz,
// or cloned git repo), validates the package, rewrites resources, checks CRDs,
// manages the namespace, and applies resources in dependency order.
//
// If opts.DryRun is true, InstallFlow prints the rewritten YAML to stdout
// and returns early without any cluster mutation. opts.Force is ignored.
//
// If opts.Force is true, InstallFlow confirms (unless opts.Yes), deletes
// the namespace, polls until 404 (120s timeout), then proceeds with install.
func InstallFlow(ctx context.Context, k8s *api.K8sClient, opts InstallOptions, stdout io.Writer, stderr io.Writer, stdin io.Reader) (*InstallResult, error) {
	// ── Step 1: Resolve source to a working directory ──────────────────
	workingDir, cleanup, err := resolveSource(ctx, opts.Source, opts.Ref)
	if err != nil {
		return nil, err
	}
	defer func() {
		if cleanup != nil {
			cleanup()
		}
	}()

	// ── Step 2: Validate package ───────────────────────────────────────
	manifestData, err := os.ReadFile(filepath.Join(workingDir, ManifestFile))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("no manifest.yaml found in directory; not a valid flow package")
		}
		return nil, fmt.Errorf("failed to read manifest.yaml: %w", err)
	}

	manifest, err := UnmarshalManifest(manifestData)
	if err != nil {
		return nil, fmt.Errorf("invalid flow package: %v", err)
	}

	// Validate referenced files exist
	for _, res := range manifest.Resources {
		if _, err := os.Stat(filepath.Join(workingDir, res.Path)); err != nil {
			if os.IsNotExist(err) {
				return nil, fmt.Errorf("invalid flow package: resource file `%s` referenced in manifest but not found", res.Path)
			}
			return nil, fmt.Errorf("failed to stat %s: %w", res.Path, err)
		}
	}

	// ── Step 3: Parse resource files ───────────────────────────────────
	var allResources []parsedResource
	for _, res := range manifest.Resources {
		data, err := os.ReadFile(filepath.Join(workingDir, res.Path))
		if err != nil {
			return nil, fmt.Errorf("failed to read %s: %w", res.Path, err)
		}
		parsed, err := ParseMultiDocYAML(data)
		if err != nil {
			return nil, fmt.Errorf("invalid YAML in %s: %v", res.Path, err)
		}
		for _, obj := range parsed {
			allResources = append(allResources, parsedResource{obj: obj, filename: res.Path})
		}
	}

	// ── Step 4: Validate DNS label ─────────────────────────────────────
	if !dnsLabelRe.MatchString(opts.FlowName) {
		return nil, fmt.Errorf("invalid namespace name `%s`: must be a valid DNS label (lowercase alphanumeric and hyphens, max 63 characters)", opts.FlowName)
	}

	// ── Step 5: CRD check (skip if dry-run) ────────────────────────────
	if !opts.DryRun {
		for _, schema := range manifest.Schemas {
			// Parse group/version from schema string (e.g. "flow.foundry.io/v1")
			parts := strings.SplitN(schema, "/", 2)
			if len(parts) != 2 {
				return nil, fmt.Errorf("invalid schema %q: expected format <group>/<version>", schema)
			}
			_, err := k8s.CoreClient.Discovery().ServerResourcesForGroupVersion(schema)
			if err != nil {
				if apierrors.IsNotFound(err) {
					return nil, fmt.Errorf("CRDs not found — run flowctl init first")
				}
				return nil, fmt.Errorf("failed to verify CRDs: %w", err)
			}
			_ = parts // keep the compiler happy; parts are used for potential future validation
		}
	}

	// ── Step 6: Dry-run early return ───────────────────────────────────
	if opts.DryRun {
		for i, pr := range allResources {
			// Normalize then Rewrite (same order as apply path)
			Normalize(pr.obj)
			Rewrite(pr.obj, opts.FlowName)
			out, err := yaml.Marshal(pr.obj)
			if err != nil {
				return nil, fmt.Errorf("failed to serialize resource %s/%s: %w", pr.obj.GetKind(), pr.obj.GetName(), err)
			}
			if i > 0 {
				fmt.Fprint(stdout, "---\n")
			}
			fmt.Fprint(stdout, string(out))
		}
		if len(allResources) > 0 {
			fmt.Fprint(stdout, "---\n")
		}
		return &InstallResult{FlowName: opts.FlowName}, nil
	}

	// ── Step 7: Force — confirm, delete namespace, poll ────────────────
	if opts.Force {
		if !opts.Yes {
			fmt.Fprintf(stderr, "WARNING: --force will delete namespace `%s` and ALL resources in it. Continue? [y/N] ", opts.FlowName)
			reader := bufio.NewReader(stdin)
			response, err := reader.ReadString('\n')
			if err != nil {
				return nil, fmt.Errorf("failed to read confirmation: %w", err)
			}
			response = strings.TrimSpace(response)
			if response != "y" && response != "Y" {
				return nil, fmt.Errorf("aborted by user")
			}
		}

		// Delete namespace
		err := k8s.CoreClient.CoreV1().Namespaces().Delete(ctx, opts.FlowName, metav1.DeleteOptions{})
		if err != nil && !apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("failed to delete namespace: %w", err)
		}

		// If namespace was not found, skip polling
		if err == nil || !apierrors.IsNotFound(err) {
			// Poll for deletion
			pollInterval := opts.PollInterval
			if pollInterval <= 0 {
				pollInterval = 2 * time.Second
			}
			pollTimeout := opts.PollTimeout
			if pollTimeout <= 0 {
				pollTimeout = 120 * time.Second
			}

			deadline := time.Now().Add(pollTimeout)
			for {
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				default:
				}

				_, getErr := k8s.CoreClient.CoreV1().Namespaces().Get(ctx, opts.FlowName, metav1.GetOptions{})
				if apierrors.IsNotFound(getErr) {
					break // namespace is gone
				}
				if getErr != nil {
					return nil, fmt.Errorf("failed to check namespace deletion: %w", getErr)
				}

				if time.Now().After(deadline) {
					return nil, fmt.Errorf("namespace `%s` is stuck terminating; check finalizers on custom resources in that namespace", opts.FlowName)
				}

				time.Sleep(pollInterval)
			}
		}
	}

	// ── Step 8: Create namespace ───────────────────────────────────────
	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: opts.FlowName},
	}
	_, err = k8s.CoreClient.CoreV1().Namespaces().Create(ctx, ns, metav1.CreateOptions{})
	if err != nil {
		if apierrors.IsAlreadyExists(err) {
			// Check if namespace already contains a FoundryFlow
			flowSummary, getFlowErr := k8s.GetFoundryFlow(ctx, opts.FlowName)
			if getFlowErr != nil {
				// If it's a permissions error, log it but proceed (TOCTOU edge case)
				if !apierrors.IsForbidden(getFlowErr) {
					return nil, fmt.Errorf("failed to check existing flow in namespace: %w", getFlowErr)
				}
			}
			if flowSummary != nil {
				return nil, fmt.Errorf("namespace already contains a flow — use `--force` to delete and replace")
			}
			// Namespace exists but has no flow — silently reuse
		} else {
			return nil, fmt.Errorf("failed to create namespace: %w", err)
		}
	}

	// ── Step 9: Rewrite resources ──────────────────────────────────────
	for _, pr := range allResources {
		Normalize(pr.obj)
		Rewrite(pr.obj, opts.FlowName)
	}

	// ── Step 10: Filter never-apply resources ──────────────────────────
	var filtered []parsedResource
	for _, pr := range allResources {
		kind := pr.obj.GetKind()
		name := pr.obj.GetName()
		if kind == "Secret" || kind == "FlowSupportService" || kind == "CodificationService" {
			fmt.Fprintf(stderr, "%s/%s: skipped (not supported)\n", kind, name)
			continue
		}
		filtered = append(filtered, pr)
	}

	// ── Step 11: Classify resources into apply order ───────────────────
	type classifiedResource struct {
		obj      *unstructured.Unstructured
		gvr      schema.GroupVersionResource
		priority int
		filename string
	}

	var classified []classifiedResource
	for _, pr := range filtered {
		gvr, priority, ok := kindPriority(pr.obj.GetKind())
		if !ok {
			fmt.Fprintf(stderr, "unknown resource kind `%s` in `%s`: applying with lowest priority\n", pr.obj.GetKind(), pr.filename)
		}
		classified = append(classified, classifiedResource{
			obj:      pr.obj,
			gvr:      gvr,
			priority: priority,
			filename: pr.filename,
		})
	}

	// Stable sort by priority (preserves parse order within same priority)
	sort.SliceStable(classified, func(i, j int) bool {
		return classified[i].priority < classified[j].priority
	})

	// ── Step 12: Apply resources with reporting ────────────────────────
	result := &InstallResult{FlowName: opts.FlowName}

	for _, cr := range classified {
		nsClient := k8s.DynamicClient().Resource(cr.gvr).Namespace(opts.FlowName)
		_, err := nsClient.Create(ctx, cr.obj, metav1.CreateOptions{})
		kind := cr.obj.GetKind()
		name := cr.obj.GetName()

		if err == nil {
			result.Created++
			fmt.Fprintf(stdout, "%s/%s: created\n", kind, name)
		} else if apierrors.IsAlreadyExists(err) {
			result.Unchanged++
			fmt.Fprintf(stdout, "%s/%s: unchanged\n", kind, name)
		} else {
			result.Failed++
			errMsg := fmt.Sprintf("%s/%s: %v", kind, name, err)
			result.Errors = append(result.Errors, errMsg)
			fmt.Fprintf(stdout, "%s/%s: failed — %v\n", kind, name, err)
		}
	}

	// ── Step 12: Return result ─────────────────────────────────────────
	if result.Failed > 0 {
		return result, fmt.Errorf("install partial failure: %d succeeded, %d failed", result.Created+result.Unchanged, result.Failed)
	}
	return result, nil
}


