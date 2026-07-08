package api

import (
	"context"
	"fmt"
	"math"
	"sort"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"
)

// Package GVR constants for flow.gideas.io CRDs.
var (
	workitemGVR = schema.GroupVersionResource{
		Group:    "flow.gideas.io",
		Version:  "v1",
		Resource: "workitems",
	}
)

// ErrMultipleFoundryFlows is returned by GetFoundryFlow when more than one
// FoundryFlow CRD exists in the namespace.
var ErrMultipleFoundryFlows = fmt.Errorf("multiple FoundryFlows detected")

// K8sClient wraps Kubernetes and controller-runtime clients for CRD operations.
type K8sClient struct {
	CoreClient    kubernetes.Interface // client-go for core/v1
	CRDClient     client.Client        // controller-runtime for CRDs
	RESTConfig    *rest.Config         // stored for port-forward creation
	scheme        *runtime.Scheme      // scheme with flow.gideas.io types
	dynamicClient dynamic.Interface    // client-go dynamic for watch/status
}

// WatchOptions controls WatchWithBackoff behavior.
type WatchOptions struct {
	OnDisconnect func(error) // called when watch closes, carries the error (nil for clean disconnect)
	OnReconnect  func()      // called when watch re-establishes
}

// NewK8sClient creates a K8sClient from the given kubeconfig path.
// An empty path uses client-go's default loading rules (KUBECONFIG, ~/.kube/config).
func NewK8sClient(kubeconfigPath string) (*K8sClient, error) {
	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	if kubeconfigPath != "" {
		loadingRules.ExplicitPath = kubeconfigPath
	}
	configOverrides := &clientcmd.ConfigOverrides{}
	kubeConfig := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, configOverrides)

	config, err := kubeConfig.ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to load kubeconfig: %w", err)
	}

	return NewForConfig(config)
}

// NewForConfig creates a K8sClient from a pre-built *rest.Config.
func NewForConfig(config *rest.Config) (*K8sClient, error) {
	coreClient, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("failed to create Kubernetes client: %w", err)
	}

	dynamicClient, err := dynamic.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("failed to create dynamic client: %w", err)
	}

	scheme := runtime.NewScheme()
	if err := addFlowScheme(scheme); err != nil {
		return nil, fmt.Errorf("failed to register flow scheme: %w", err)
	}

	crdClient, err := client.New(config, client.Options{Scheme: scheme})
	if err != nil {
		return nil, fmt.Errorf("failed to create controller-runtime client: %w", err)
	}

	return &K8sClient{
		CoreClient:    coreClient,
		CRDClient:     crdClient,
		RESTConfig:    config,
		scheme:        scheme,
		dynamicClient: dynamicClient,
	}, nil
}

// addFlowScheme registers unstructured types for flow.gideas.io CRDs.
func addFlowScheme(s *runtime.Scheme) error {
	gv := schema.GroupVersion{Group: "flow.gideas.io", Version: "v1"}
	s.AddKnownTypeWithName(gv.WithKind("Workitem"), &unstructured.Unstructured{})
	s.AddKnownTypeWithName(gv.WithKind("WorkitemList"), &unstructured.UnstructuredList{})
	s.AddKnownTypeWithName(gv.WithKind("FoundryFlow"), &unstructured.Unstructured{})
	s.AddKnownTypeWithName(gv.WithKind("FoundryFlowList"), &unstructured.UnstructuredList{})
	s.AddKnownTypeWithName(gv.WithKind("FoundryNode"), &unstructured.Unstructured{})
	s.AddKnownTypeWithName(gv.WithKind("FoundryNodeList"), &unstructured.UnstructuredList{})
	s.AddKnownTypeWithName(gv.WithKind("GovernedArtefact"), &unstructured.Unstructured{})
	s.AddKnownTypeWithName(gv.WithKind("GovernedArtefactList"), &unstructured.UnstructuredList{})

	// Add core/v1 types needed by the scheme for namespace pod operations.
	if err := corev1.AddToScheme(s); err != nil {
		return err
	}
	return nil
}

// GetCurrentContextNamespace returns the namespace from the current kube context,
// falling back to "default" if not set or on error.
func GetCurrentContextNamespace() string {
	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	configOverrides := &clientcmd.ConfigOverrides{}
	kubeConfig := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, configOverrides)

	ns, _, err := kubeConfig.Namespace()
	if err != nil {
		return "default"
	}
	if ns == "" {
		return "default"
	}
	return ns
}

// ListNamespaces returns all accessible namespaces sorted lexicographically.
func (c *K8sClient) ListNamespaces(ctx context.Context) ([]string, error) {
	nsList, err := c.CoreClient.CoreV1().Namespaces().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}

	namespaces := make([]string, 0, len(nsList.Items))
	for _, ns := range nsList.Items {
		namespaces = append(namespaces, ns.Name)
	}
	sort.Strings(namespaces)
	return namespaces, nil
}

// IsForbiddenError checks if the given error is a Kubernetes 403 Forbidden error,
// indicating RBAC denial rather than a transient connectivity or server error.
func IsForbiddenError(err error) bool {
	return apierrors.IsForbidden(err)
}

// ListWorkitems returns all Workitems in the given namespace as summaries.
func (c *K8sClient) ListWorkitems(ctx context.Context, namespace string) ([]WorkitemSummary, error) {
	list := &unstructured.UnstructuredList{}
	list.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "flow.gideas.io",
		Version: "v1",
		Kind:    "WorkitemList",
	})

	if err := c.CRDClient.List(ctx, list, client.InNamespace(namespace)); err != nil {
		return nil, err
	}

	summaries := make([]WorkitemSummary, 0, len(list.Items))
	for _, item := range list.Items {
		summaries = append(summaries, extractSummaryFromUnstructured(&item))
	}
	return summaries, nil
}

// WatchWorkitems returns a watch.Interface for Workitems in the namespace.
func (c *K8sClient) WatchWorkitems(ctx context.Context, namespace string) (watch.Interface, error) {
	return c.dynamicClient.Resource(workitemGVR).Namespace(namespace).Watch(ctx, metav1.ListOptions{})
}

// WatchWithBackoff watches Workitems in namespace, calling handler for each event.
// Automatically reconnects on disconnect with exponential backoff (1s initial, 30s max).
// Blocks until ctx is cancelled or the watch permanently fails.
func (c *K8sClient) WatchWithBackoff(ctx context.Context, namespace string, handler func(watch.Event), opts ...WatchOptions) {
	const (
		base   = 1 * time.Second
		max    = 30 * time.Second
		factor = 2.0
	)

	options := WatchOptions{}
	if len(opts) > 0 {
		options = opts[0]
	}

	for attempt := 0; ; attempt++ {
		watcher, err := c.WatchWorkitems(ctx, namespace)
		if err != nil {
			if options.OnDisconnect != nil {
				options.OnDisconnect(err)
			}
			sleep := time.Duration(float64(base) * math.Pow(factor, float64(attempt)))
			if sleep > max {
				sleep = max
			}
			select {
			case <-time.After(sleep):
				continue
			case <-ctx.Done():
				return
			}
		}

		if options.OnReconnect != nil {
			options.OnReconnect()
		}
		// Reset attempt so backoff after a successful watch starts from 1s again
		attempt = 0
		events := watcher.ResultChan()
	eventLoop:
		for {
			select {
			case event, ok := <-events:
				if !ok {
					watcher.Stop()
					if options.OnDisconnect != nil {
						options.OnDisconnect(nil)
					}
					break eventLoop
				}
				handler(event)

			case <-ctx.Done():
				watcher.Stop()
				return
			}
		}

		// Exponential backoff before reconnect
		sleep := time.Duration(float64(base) * math.Pow(factor, float64(attempt)))
		if sleep > max {
			sleep = max
		}
		select {
		case <-time.After(sleep):
		case <-ctx.Done():
			return
		}
	}
}

// GetWorkitem fetches a single Workitem with full detail.
func (c *K8sClient) GetWorkitem(ctx context.Context, namespace string, name string) (*WorkitemDetail, error) {
	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "flow.gideas.io",
		Version: "v1",
		Kind:    "Workitem",
	})

	if err := c.CRDClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, obj); err != nil {
		return nil, err
	}

	summary := extractSummaryFromUnstructured(obj)

	detail := &WorkitemDetail{
		WorkitemSummary: summary,
		Labels:          obj.GetLabels(),
		Annotations:     obj.GetAnnotations(),
	}

	// Extract failureReason
	if failureReason, ok, _ := unstructured.NestedString(obj.Object, "status", "failureReason"); ok {
		detail.FailureReason = failureReason
	}

	// Extract thrashCounters
	if thrashRaw, ok, _ := unstructured.NestedMap(obj.Object, "status", "thrashCounters"); ok {
		counters := make(map[string]int32, len(thrashRaw))
		for k, v := range thrashRaw {
			// v can be int64 or float64 in unstructured; convert to int32
			switch val := v.(type) {
			case int64:
				counters[k] = int32(val)
			case float64:
				counters[k] = int32(val)
			}
		}
		detail.ThrashCounters = counters
	}

	// Fetch children
	children, err := c.ListChildren(ctx, namespace, name)
	if err != nil {
		// Non-fatal — return detail without children
		children = nil
	}
	detail.ChildWorkitems = children

	return detail, nil
}

// ListChildren returns Workitems with the given parent label.
func (c *K8sClient) ListChildren(ctx context.Context, namespace string, parentID string) ([]WorkitemSummary, error) {
	list := &unstructured.UnstructuredList{}
	list.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "flow.gideas.io",
		Version: "v1",
		Kind:    "WorkitemList",
	})

	if err := c.CRDClient.List(ctx, list,
		client.InNamespace(namespace),
		client.MatchingLabels{"flow.gideas.io/parent": parentID},
	); err != nil {
		return nil, err
	}

	summaries := make([]WorkitemSummary, 0, len(list.Items))
	for _, item := range list.Items {
		summaries = append(summaries, extractSummaryFromUnstructured(&item))
	}
	return summaries, nil
}

// GetFoundryFlow returns the singular FoundryFlow in the namespace, or nil if none,
// or an error if multiple exist.
func (c *K8sClient) GetFoundryFlow(ctx context.Context, namespace string) (*FoundryFlowSummary, error) {
	list := &unstructured.UnstructuredList{}
	list.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "flow.gideas.io",
		Version: "v1",
		Kind:    "FoundryFlowList",
	})

	if err := c.CRDClient.List(ctx, list, client.InNamespace(namespace)); err != nil {
		return nil, err
	}

	if len(list.Items) == 0 {
		return nil, nil
	}
	if len(list.Items) > 1 {
		return nil, fmt.Errorf("%w in namespace %s; expected exactly one", ErrMultipleFoundryFlows, namespace)
	}

	flow := &list.Items[0]
	summary := &FoundryFlowSummary{
		Name: flow.GetName(),
	}
	if entryContracts, ok, _ := unstructured.NestedMap(flow.Object, "spec", "entryContracts"); ok {
		summary.EntryContracts = entryContracts
	}
	return summary, nil
}

// ListFoundryNodes returns all FoundryNodes in the namespace.
func (c *K8sClient) ListFoundryNodes(ctx context.Context, namespace string) ([]FoundryNodeSummary, error) {
	list := &unstructured.UnstructuredList{}
	list.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "flow.gideas.io",
		Version: "v1",
		Kind:    "FoundryNodeList",
	})

	if err := c.CRDClient.List(ctx, list, client.InNamespace(namespace)); err != nil {
		return nil, err
	}

	nodes := make([]FoundryNodeSummary, 0, len(list.Items))
	for _, item := range list.Items {
		summary := FoundryNodeSummary{
			Name:   item.GetName(),
			Labels: item.GetLabels(),
		}

		if entry, ok, _ := unstructured.NestedString(item.Object, "spec", "entry"); ok {
			summary.Entry = entry
		}

		if outputs, ok, _ := unstructured.NestedSlice(item.Object, "spec", "outputs"); ok {
			for _, o := range outputs {
				if outputMap, ok := o.(map[string]interface{}); ok {
					if target, ok := outputMap["target"].(string); ok {
						summary.Targets = append(summary.Targets, target)
					}
				}
			}
		}

		nodes = append(nodes, summary)
	}
	return nodes, nil
}

// ListGovernedArtefacts returns all GovernedArtefacts in the namespace.
func (c *K8sClient) ListGovernedArtefacts(ctx context.Context, namespace string) ([]GovernedArtefactSummary, error) {
	list := &unstructured.UnstructuredList{}
	list.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "flow.gideas.io",
		Version: "v1",
		Kind:    "GovernedArtefactList",
	})

	if err := c.CRDClient.List(ctx, list, client.InNamespace(namespace)); err != nil {
		return nil, err
	}

	artefacts := make([]GovernedArtefactSummary, 0, len(list.Items))
	for _, item := range list.Items {
		artefacts = append(artefacts, GovernedArtefactSummary{Name: item.GetName()})
	}
	return artefacts, nil
}

// CreateWorkitem creates a metadata-only Workitem CRD.
func (c *K8sClient) CreateWorkitem(ctx context.Context, namespace string, name string, labels map[string]string) error {
	obj := &unstructured.Unstructured{}
	obj.SetAPIVersion("flow.gideas.io/v1")
	obj.SetKind("Workitem")
	obj.SetName(name)
	obj.SetNamespace(namespace)

	if labels == nil {
		labels = make(map[string]string)
	}
	labels["flow.gideas.io/creator"] = "flowctl"
	obj.SetLabels(labels)

	return c.CRDClient.Create(ctx, obj)
}

// UpdateWorkitemStatus updates the status subresource (phase and assignee).
func (c *K8sClient) UpdateWorkitemStatus(ctx context.Context, namespace string, name string, phase string, assignee string) error {
	// Use dynamic client for both read and write to ensure consistency
	// (controller-runtime and dynamic fake clients are not backed by the same store).
	wiClient := c.dynamicClient.Resource(workitemGVR).Namespace(namespace)

	getObj, err := wiClient.Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return err
	}

	// Read current status into a map so we can preserve other fields
	statusMap, _, _ := unstructured.NestedMap(getObj.Object, "status")
	if statusMap == nil {
		statusMap = make(map[string]interface{})
	}
	statusMap["phase"] = phase
	statusMap["currentAssignee"] = assignee

	if err := unstructured.SetNestedMap(getObj.Object, statusMap, "status"); err != nil {
		return fmt.Errorf("setting status fields: %w", err)
	}

	_, err = wiClient.UpdateStatus(ctx, getObj, metav1.UpdateOptions{})
	return err
}

// DeleteWorkitem deletes a Workitem CRD.
func (c *K8sClient) DeleteWorkitem(ctx context.Context, namespace string, name string) error {
	obj := &unstructured.Unstructured{}
	obj.SetAPIVersion("flow.gideas.io/v1")
	obj.SetKind("Workitem")
	obj.SetName(name)
	obj.SetNamespace(namespace)

	return c.CRDClient.Delete(ctx, obj)
}

// ExtractSummary converts a watch event's runtime.Object into a WorkitemSummary.
func ExtractSummary(obj runtime.Object) WorkitemSummary {
	u, ok := obj.(*unstructured.Unstructured)
	if !ok {
		return WorkitemSummary{}
	}
	return extractSummaryFromUnstructured(u)
}

// extractSummaryFromUnstructured converts an unstructured Workitem to WorkitemSummary.
func extractSummaryFromUnstructured(u *unstructured.Unstructured) WorkitemSummary {
	s := WorkitemSummary{
		Name: u.GetName(),
		Age:  time.Since(u.GetCreationTimestamp().Time),
	}

	if phase, ok, _ := unstructured.NestedString(u.Object, "status", "phase"); ok {
		s.State = phase
	}
	if assignee, ok, _ := unstructured.NestedString(u.Object, "status", "currentAssignee"); ok {
		s.Node = assignee
	}

	// Terminal workitems with no active assignee display "-".
	if s.Node == "" && (s.State == "Completed" || s.State == "Failed") {
		s.Node = "-"
	}

	return s
}

// ResolveSystemNamespace resolves the system namespace based on config.
// If cfg.SystemNamespace is "auto", it scans all namespaces for a Ready
// archivist pod (app.kubernetes.io/name=flow-archivist). Otherwise it uses
// the configured value or falls back to workitemNS.
func (c *K8sClient) ResolveSystemNamespace(ctx context.Context, cfgNS, workitemNS string) (string, error) {
	if cfgNS == "auto" {
		namespaces, err := c.ListNamespaces(ctx)
		if err != nil {
			return workitemNS, err
		}
		for _, ns := range namespaces {
			pods, err := c.CoreClient.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{
				LabelSelector: "app.kubernetes.io/name=flow-archivist",
			})
			if err != nil {
				continue
			}
			for _, pod := range pods.Items {
				if PodReady(&pod) {
					return ns, nil
				}
			}
		}
		return "", fmt.Errorf("no Ready archivist pod found in any accessible namespace")
	}
	if cfgNS != "" {
		return cfgNS, nil
	}
	return workitemNS, nil
}

// RESTConfig returns the underlying *rest.Config for port-forward creation.
func (c *K8sClient) GetRESTConfig() *rest.Config {
	return c.RESTConfig
}

// Ensure apiutil is imported for scheme registration (used implicitly).
var _ = apiutil.GVKForObject

// PodReady returns true when the pod is Running, Ready=True, and has a non-empty PodIP.
func PodReady(pod *corev1.Pod) bool {
	if pod.Status.Phase != corev1.PodRunning {
		return false
	}
	if pod.Status.PodIP == "" {
		return false
	}
	for _, c := range pod.Status.Conditions {
		if c.Type == corev1.PodReady && c.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}
