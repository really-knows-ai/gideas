package flow

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/yaml"

	"github.com/foundry/flow/tools/flowctl/internal/api"
	"github.com/foundry/flow/tools/flowctl/manifestfs"
)

// GVRs for cluster-scoped and namespace-scoped resources during bootstrap.
var (
	crdGVR          = schema.GroupVersionResource{Group: "apiextensions.k8s.io", Version: "v1", Resource: "customresourcedefinitions"}
	namespaceGVR    = schema.GroupVersionResource{Group: "", Version: "v1", Resource: "namespaces"}
	saGVR           = schema.GroupVersionResource{Group: "", Version: "v1", Resource: "serviceaccounts"}
	clusterRoleGVR  = schema.GroupVersionResource{Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "clusterroles"}
	clusterRoleBind = schema.GroupVersionResource{Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "clusterrolebindings"}
	deploymentGVR   = schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}
)

const (
	// operatorNamespace is the target namespace for all operator resources.
	operatorNamespace = "foundry-system"

	// defaultTimeout is how long to wait for the operator Deployment to become ready.
	defaultTimeout = 120 * time.Second

	// pollInterval is how often to poll Deployment readiness.
	pollInterval = 2 * time.Second
)

// setImageTag strips the registry host, digest, and existing tag from ref,
// then appends :tag.
//
// ponytail: heuristic — the first path segment is treated as a registry host
// iff it contains '.' or ':' (or is "localhost"), which is standard OCI/Docker
// convention. This covers all common cases (named registries, registry with
// port, docker hub official/library images, digest+tag combos). A full
// github.com/distribution/reference parser would handle exotic edge cases,
// but the dependency cost is not justified.
func setImageTag(ref, tag string) string {
	if i := strings.IndexByte(ref, '/'); i >= 0 {
		if strings.ContainsAny(ref[:i], ".:") || ref[:i] == "localhost" {
			ref = ref[i+1:] // strip registry[:port]/
		}
	}
	if i := strings.IndexByte(ref, '@'); i >= 0 {
		ref = ref[:i] // strip @digest
	}
	if i := strings.LastIndexByte(ref, ':'); i >= 0 {
		ref = ref[:i] // strip existing tag
	}
	return ref + ":" + tag
}

// applyCRDs parses all embedded CRD YAML files, creates each CustomResourceDefinition
// via the dynamic client, and warns on AlreadyExists. Returns the count of CRDs
// created (excluding already-existing ones).
func applyCRDs(ctx context.Context, client dynamic.Interface, stderr io.Writer) (int, error) {
	crdFiles, err := manifestfs.Manifests.ReadDir("crd")
	if err != nil {
		return 0, fmt.Errorf("failed to read embedded CRD directory: %w", err)
	}

	var created int
	for _, f := range crdFiles {
		if f.IsDir() {
			continue
		}
		data, err := manifestfs.Manifests.ReadFile("crd/" + f.Name())
		if err != nil {
			return created, fmt.Errorf("failed to read %s: %w", f.Name(), err)
		}

		docs, err := ParseMultiDocYAML(data)
		if err != nil {
			return created, fmt.Errorf("failed to parse %s: %w", f.Name(), err)
		}

		for _, doc := range docs {
			name := doc.GetName()
			_, err := client.Resource(crdGVR).Create(ctx, doc, metav1.CreateOptions{})
			if err == nil {
				created++
			} else if apierrors.IsAlreadyExists(err) {
				fmt.Fprintf(stderr, "already exists: %s\n", name)
			} else {
				return created, fmt.Errorf("failed to create CRD `%s`: %w", name, err)
			}
		}
	}
	return created, nil
}

// applyOperatorNamespace creates the foundry-system namespace if it does not exist.
func applyOperatorNamespace(ctx context.Context, client dynamic.Interface) error {
	data, err := manifestfs.Manifests.ReadFile("operator/namespace.yaml")
	if err != nil {
		return fmt.Errorf("failed to read embedded namespace: %w", err)
	}
	var m map[string]interface{}
	if err := yaml.Unmarshal(data, &m); err != nil {
		return fmt.Errorf("failed to parse namespace: %w", err)
	}
	ns := &unstructured.Unstructured{Object: m}
	_, err = client.Resource(namespaceGVR).Create(ctx, ns, metav1.CreateOptions{})
	if err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("failed to create namespace %s: %w", operatorNamespace, err)
	}
	return nil
}

// applyOperatorRBAC applies the embedded ServiceAccount, ClusterRole, and
// ClusterRoleBinding. AlreadyExists is treated as success.
func applyOperatorRBAC(ctx context.Context, client dynamic.Interface, stderr io.Writer) error {
	entries := []struct {
		path string
		gvr  schema.GroupVersionResource
		name string
	}{
		{path: "operator/serviceaccount.yaml", gvr: saGVR, name: "ServiceAccount"},
		{path: "operator/role.yaml", gvr: clusterRoleGVR, name: "ClusterRole"},
		{path: "operator/rolebinding.yaml", gvr: clusterRoleBind, name: "ClusterRoleBinding"},
	}

	for _, e := range entries {
		data, err := manifestfs.Manifests.ReadFile(e.path)
		if err != nil {
			return fmt.Errorf("failed to read %s: %w", e.path, err)
		}
		docs, err := ParseMultiDocYAML(data)
		if err != nil {
			return fmt.Errorf("failed to parse %s: %w", e.path, err)
		}
		for _, doc := range docs {
			var createErr error
			if e.gvr == saGVR {
				// ServiceAccount is namespaced; must scope to operatorNamespace.
				_, createErr = client.Resource(e.gvr).Namespace(operatorNamespace).Create(ctx, doc, metav1.CreateOptions{})
			} else {
				// ClusterRole and ClusterRoleBinding are cluster-scoped.
				_, createErr = client.Resource(e.gvr).Create(ctx, doc, metav1.CreateOptions{})
			}
			if createErr != nil && !apierrors.IsAlreadyExists(createErr) {
				return fmt.Errorf("failed to create %s: %w", doc.GetName(), createErr)
			}
			if apierrors.IsAlreadyExists(createErr) {
				fmt.Fprintf(stderr, "already exists: %s/%s\n", e.name, doc.GetName())
			}
		}
	}
	return nil
}

// readAndMutateDeployment deserializes the embedded deployment.yaml, ensures the
// foundry-system namespace, and mutates the container named "manager" to use the
// given image tag. It is pure in-memory — the caller decides whether to apply it
// to the cluster or print it (dry-run), and how to surface errors.
func readAndMutateDeployment(version string) (*unstructured.Unstructured, error) {
	data, err := manifestfs.Manifests.ReadFile("operator/deployment.yaml")
	if err != nil {
		return nil, fmt.Errorf("failed to read embedded deployment: %w", err)
	}

	var m map[string]interface{}
	if err := yaml.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("failed to parse deployment: %w", err)
	}
	deploy := &unstructured.Unstructured{Object: m}

	// Ensure namespace is foundry-system.
	deploy.SetNamespace(operatorNamespace)

	// Find container "manager" and mutate its image tag.
	containers, found, err := unstructured.NestedSlice(deploy.Object, "spec", "template", "spec", "containers")
	if err != nil || !found {
		return nil, fmt.Errorf("deployment has no spec.template.spec.containers")
	}

	mutated := false
	for i, c := range containers {
		cm, ok := c.(map[string]interface{})
		if !ok {
			continue
		}
		name, _ := cm["name"].(string)
		if name != "manager" {
			continue
		}
		image, _ := cm["image"].(string)
		if image == "" {
			return nil, fmt.Errorf("container %q has no image field", name)
		}
		cm["image"] = setImageTag(image, version)
		containers[i] = cm
		mutated = true
		break
	}
	if !mutated {
		return nil, fmt.Errorf("no container with name %q found in deployment", "manager")
	}
	if err := unstructured.SetNestedSlice(deploy.Object, containers, "spec", "template", "spec", "containers"); err != nil {
		return nil, fmt.Errorf("failed to set containers: %w", err)
	}
	return deploy, nil
}

// mutateAndApplyDeployment reads the embedded deployment, mutates the image tag,
// and creates or updates the Deployment.
func mutateAndApplyDeployment(ctx context.Context, client dynamic.Interface, version string) error {
	deploy, err := readAndMutateDeployment(version)
	if err != nil {
		return err
	}

	// Create-or-update: try Get first, if found do Update, else Create.
	nsClient := client.Resource(deploymentGVR).Namespace(operatorNamespace)
	existing, getErr := nsClient.Get(ctx, deploy.GetName(), metav1.GetOptions{})
	if getErr == nil {
		// Preserve resource version for update.
		deploy.SetResourceVersion(existing.GetResourceVersion())
		_, err = nsClient.Update(ctx, deploy, metav1.UpdateOptions{})
	} else if apierrors.IsNotFound(getErr) {
		_, err = nsClient.Create(ctx, deploy, metav1.CreateOptions{})
	} else {
		return fmt.Errorf("failed to check existing deployment: %w", getErr)
	}
	return err
}

// waitForOperatorReady polls the operator Deployment until AvailableReplicas >= 1
// or the context timeout expires. While waiting, it follows the
// Deployment→ReplicaSet→Pod chain and reports container waiting states to stderr.
func waitForOperatorReady(ctx context.Context, client kubernetes.Interface, namespace string, timeout time.Duration, stderr io.Writer) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	deployName := "controller-manager"

	for {
		select {
		case <-ctx.Done():
			// On timeout, collect and report last observed state.
			dumpDeploymentState(ctx, client, namespace, deployName, stderr)
			return fmt.Errorf("operator deployment %s/%s did not become ready within %v", namespace, deployName, timeout)
		default:
		}

		dep, err := client.AppsV1().Deployments(namespace).Get(ctx, deployName, metav1.GetOptions{})
		if err != nil {
			return fmt.Errorf("failed to get deployment %s/%s: %w", namespace, deployName, err)
		}

		if dep.Status.AvailableReplicas >= 1 {
			return nil
		}

		// Report pod waiting states via Deployment→ReplicaSet→Pod chain.
		reportPodWaitingStates(ctx, client, dep, namespace, stderr)

		time.Sleep(pollInterval)
	}
}

// reportPodWaitingStates follows the Deployment→ReplicaSet→Pod chain and
// prints container waiting reasons to stderr.
func reportPodWaitingStates(ctx context.Context, client kubernetes.Interface, dep *appsv1.Deployment, namespace string, stderr io.Writer) {
	selector := dep.Spec.Selector
	if selector == nil || len(selector.MatchLabels) == 0 {
		return
	}

	labelSelector := labelSelectorToString(selector.MatchLabels)
	rsList, err := client.AppsV1().ReplicaSets(namespace).List(ctx, metav1.ListOptions{LabelSelector: labelSelector})
	if err != nil {
		return
	}

	// Find the current ReplicaSet owned by this Deployment.
	// We use the owner reference as the primary filter — the ReplicaSet's
	// metadata.generation is its own counter and is not comparable to the
	// Deployment's generation.
	var currentRSSelector map[string]string
	for i := range rsList.Items {
		rs := &rsList.Items[i]
		ownerRef := metav1.GetControllerOf(rs)
		if ownerRef == nil || ownerRef.UID != dep.UID {
			continue
		}
		currentRSSelector = rs.Spec.Selector.MatchLabels
		break
	}
	if currentRSSelector == nil {
		// No current ReplicaSet yet; maybe still creating.
		return
	}

	podLabelSelector := labelSelectorToString(currentRSSelector)
	pods, err := client.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{LabelSelector: podLabelSelector})
	if err != nil {
		return
	}

	for _, pod := range pods.Items {
		for _, cs := range pod.Status.ContainerStatuses {
			if cs.State.Waiting != nil {
				reason := cs.State.Waiting.Reason
				msg := cs.State.Waiting.Message
				if msg != "" {
					fmt.Fprintf(stderr, "%s: %s: %s\n", reason, cs.Name, msg)
				} else {
					fmt.Fprintf(stderr, "%s: %s\n", reason, cs.Name)
				}
			}
		}
	}
}

// dumpDeploymentState prints the last observed state of a Deployment, its
// ReplicaSets, and their Pods to stderr.
func dumpDeploymentState(ctx context.Context, client kubernetes.Interface, namespace, deployName string, stderr io.Writer) {
	dep, err := client.AppsV1().Deployments(namespace).Get(ctx, deployName, metav1.GetOptions{})
	if err != nil {
		fmt.Fprintf(stderr, "failed to get deployment for status dump: %v\n", err)
		return
	}

	fmt.Fprintf(stderr, "--- Deployment %s/%s status ---\n", namespace, deployName)
	fmt.Fprintf(stderr, "  AvailableReplicas: %d\n", dep.Status.AvailableReplicas)
	fmt.Fprintf(stderr, "  ReadyReplicas: %d\n", dep.Status.ReadyReplicas)
	fmt.Fprintf(stderr, "  Replicas: %d\n", dep.Status.Replicas)
	fmt.Fprintf(stderr, "  Conditions:\n")
	for _, c := range dep.Status.Conditions {
		fmt.Fprintf(stderr, "    %s: %s — %s\n", c.Type, c.Status, c.Reason)
	}

	// List ReplicaSets.
	rsList, err := client.AppsV1().ReplicaSets(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: labelSelectorToString(dep.Spec.Selector.MatchLabels),
	})
	if err != nil {
		fmt.Fprintf(stderr, "  (failed to list ReplicaSets: %v)\n", err)
		return
	}
	for _, rs := range rsList.Items {
		fmt.Fprintf(stderr, "  ReplicaSet %s: generation=%d observedGeneration=%d replicas=%d available=%d\n",
			rs.Name, rs.Generation, rs.Status.ObservedGeneration, rs.Status.Replicas, rs.Status.AvailableReplicas)
		// List pods for this ReplicaSet.
		pods, err := client.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
			LabelSelector: labelSelectorToString(rs.Spec.Selector.MatchLabels),
		})
		if err != nil {
			continue
		}
		for _, pod := range pods.Items {
			fmt.Fprintf(stderr, "    Pod %s: phase=%s\n", pod.Name, pod.Status.Phase)
			for _, cs := range pod.Status.ContainerStatuses {
				if cs.State.Waiting != nil {
					fmt.Fprintf(stderr, "      container %s: waiting %s: %s\n", cs.Name, cs.State.Waiting.Reason, cs.State.Waiting.Message)
				}
				if cs.State.Terminated != nil {
					fmt.Fprintf(stderr, "      container %s: terminated %s: exit=%d\n", cs.Name, cs.State.Terminated.Reason, cs.State.Terminated.ExitCode)
				}
			}
		}
	}
}

// labelSelectorToString converts a map of match labels to a string suitable
// for use as a Kubernetes label selector.
func labelSelectorToString(labels map[string]string) string {
	if len(labels) == 0 {
		return ""
	}
	parts := make([]string, 0, len(labels))
	for k, v := range labels {
		parts = append(parts, k+"="+v)
	}
	sort.Strings(parts)
	return strings.Join(parts, ",")
}

// Bootstrap orchestrates the full `flowctl init` sequence.
func Bootstrap(ctx context.Context, kubeconfigPath, version string, dryRun bool, stdout, stderr io.Writer) error {
	// Dry-run does not require a cluster connection.
	if dryRun {
		return dryRunBootstrap(stdout, version)
	}

	// ── Connect ─────────────────────────────────────────────────────────
	k8s, err := api.NewK8sClient(kubeconfigPath)
	if err != nil {
		return fmt.Errorf("failed to connect to Kubernetes: %w", err)
	}

	// ── Probe connectivity ──────────────────────────────────────────────
	connectivityCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := api.CheckConnectivity(connectivityCtx, k8s); err != nil {
		return err
	}

	dynamicClient := k8s.DynamicClient
	coreClient := k8s.CoreClient

	// ── Apply CRDs ──────────────────────────────────────────────────────
	crdCount, err := applyCRDs(ctx, dynamicClient, stderr)
	if err != nil {
		return err
	}

	// ── Apply operator Namespace ──────────────────────────────────────
	if err := applyOperatorNamespace(ctx, dynamicClient); err != nil {
		return err
	}
	fmt.Fprintf(stderr, "namespace/%s: created\n", operatorNamespace)

	// ── Apply operator RBAC ───────────────────────────────────────────
	if err := applyOperatorRBAC(ctx, dynamicClient, stderr); err != nil {
		return err
	}
	fmt.Fprintf(stderr, "RBAC resources: applied\n")

	// ── Mutate + apply Deployment ─────────────────────────────────────
	if err := mutateAndApplyDeployment(ctx, dynamicClient, version); err != nil {
		return err
	}
	fmt.Fprintf(stderr, "deployment/controller-manager: applied\n")

	// ── Wait for readiness ────────────────────────────────────────────
	if err := waitForOperatorReady(ctx, coreClient, operatorNamespace, defaultTimeout, stderr); err != nil {
		return err
	}

	// ── Success summary ──────────────────────────────────────────────
	fmt.Fprintf(stdout, "✓ Installed %d CRDs\n", crdCount)
	fmt.Fprintf(stdout, "✓ Operator running in %s\n", operatorNamespace)
	fmt.Fprintf(stdout, "→ Ready for flowctl install\n")
	return nil
}

// dryRunBootstrap prints all embedded manifests to stdout with the deployment
// image tag mutated, then prints a summary.
func dryRunBootstrap(stdout io.Writer, version string) error {
	// Count CRD files for summary.
	crdFiles, err := manifestfs.Manifests.ReadDir("crd")
	if err != nil {
		return fmt.Errorf("failed to read embedded CRD directory: %w", err)
	}

	var crdCount int
	var first = true
	for _, f := range crdFiles {
		if f.IsDir() {
			continue
		}
		data, err := manifestfs.Manifests.ReadFile("crd/" + f.Name())
		if err != nil {
			return fmt.Errorf("failed to read %s: %w", f.Name(), err)
		}
		docs, err := ParseMultiDocYAML(data)
		if err != nil {
			return fmt.Errorf("failed to parse %s: %w", f.Name(), err)
		}
		for _, doc := range docs {
			if !first {
				fmt.Fprint(stdout, "---\n")
			}
			first = false
			out, err := yaml.Marshal(doc)
			if err != nil {
				return fmt.Errorf("failed to marshal CRD: %w", err)
			}
			fmt.Fprint(stdout, string(out))
			crdCount++
		}
	}

	// Operator manifests.
	operatorFiles := []string{
		"operator/namespace.yaml",
		"operator/serviceaccount.yaml",
		"operator/role.yaml",
		"operator/rolebinding.yaml",
	}
	for _, path := range operatorFiles {
		data, err := manifestfs.Manifests.ReadFile(path)
		if err != nil {
			return fmt.Errorf("failed to read %s: %w", path, err)
		}
		var m map[string]interface{}
		if err := yaml.Unmarshal(data, &m); err != nil {
			return fmt.Errorf("failed to parse %s: %w", path, err)
		}
		doc := &unstructured.Unstructured{Object: m}
		if !first {
			fmt.Fprint(stdout, "---\n")
		}
		first = false
		out, err := yaml.Marshal(doc)
		if err != nil {
			return fmt.Errorf("failed to marshal %s: %w", path, err)
		}
		fmt.Fprint(stdout, string(out))
	}

	// Deployment (with image tag mutation).
	deploy, err := readAndMutateDeployment(version)
	if err != nil {
		return fmt.Errorf("failed to prepare deployment: %w", err)
	}
	if !first {
		fmt.Fprint(stdout, "---\n")
	}
	first = false
	out, err := yaml.Marshal(deploy)
	if err != nil {
		return fmt.Errorf("failed to marshal deployment: %w", err)
	}
	fmt.Fprint(stdout, string(out))

	fmt.Fprintf(stdout, "# dry-run: %d CRDs, 3 RBAC resources, 1 Namespace, 1 Deployment\n", crdCount)
	return nil
}
