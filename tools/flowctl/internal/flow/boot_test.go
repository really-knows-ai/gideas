package flow

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic/fake"
	k8sfake "k8s.io/client-go/kubernetes/fake"
)

var ctx = context.Background()

// ─── TestDryRunPrintsManifests ─────────────────────────────────────────────

func TestDryRunPrintsManifests(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	// Bootstrap with dryRun=true should not touch the cluster — we pass
	// a nil context because no API calls are made.
	err := Bootstrap(context.Background(), "", "latest", true, &stdout, &stderr)
	if err != nil {
		t.Fatalf("Bootstrap dry-run failed: %v", err)
	}

	output := stdout.String()

	// Should contain CRD YAML (evidence: apiVersion: apiextensions.k8s.io/v1)
	if !strings.Contains(output, "apiextensions.k8s.io/v1") {
		t.Errorf("dry-run output missing CRD YAML")
	}

	// Should contain Namespace manifest.
	if !strings.Contains(output, "kind: Namespace") {
		t.Errorf("dry-run output missing Namespace")
	}

	// Should contain Deployment manifest.
	if !strings.Contains(output, "kind: Deployment") {
		t.Errorf("dry-run output missing Deployment")
	}

	// Should contain foundry-system namespace reference.
	if !strings.Contains(output, "foundry-system") {
		t.Errorf("dry-run output missing foundry-system namespace")
	}

	// Should contain the summary line.
	if !strings.Contains(output, "# dry-run:") {
		t.Errorf("dry-run output missing summary line")
	}

	// Stderr should be empty.
	if stderr.Len() > 0 {
		t.Errorf("dry-run wrote to stderr: %s", stderr.String())
	}
}

// ─── TestImageTagMutation ────────────────────────────────────────────────

func TestImageTagMutation(t *testing.T) {
	tests := []struct {
		ref  string
		tag  string
		want string
	}{
		{ref: "controller:latest", tag: "v1.2.3", want: "controller:v1.2.3"},
		{ref: "controller", tag: "v1.2.3", want: "controller:v1.2.3"},
		{ref: "gcr.io/gideas/controller:latest", tag: "v1.2.3", want: "gideas/controller:v1.2.3"},
		{ref: "gcr.io/gideas/controller@sha256:abc123", tag: "v1.2.3", want: "gideas/controller:v1.2.3"},
		{ref: "registry:5000/controller:1.0", tag: "2.0", want: "controller:2.0"},
		{ref: "controller:1.0", tag: "", want: "controller:"},
		{ref: "localhost/myimage:tag", tag: "newtag", want: "myimage:newtag"},
	}

	for _, tt := range tests {
		got := setImageTag(tt.ref, tt.tag)
		if got != tt.want {
			t.Errorf("setImageTag(%q, %q) = %q, want %q", tt.ref, tt.tag, got, tt.want)
		}
	}
}

// ─── TestCRDAlreadyExists ─────────────────────────────────────────────────

func TestCRDAlreadyExists(t *testing.T) {
	ctx := context.Background()
	scheme := runtime.NewScheme()
	fakeDyn := fake.NewSimpleDynamicClient(scheme)
	var stderr bytes.Buffer

	// First call: CRDs do not exist, should create all.
	created1, err := applyCRDs(ctx, fakeDyn, &stderr)
	if err != nil {
		t.Fatalf("first applyCRDs failed: %v", err)
	}
	if created1 == 0 {
		t.Fatal("expected at least 1 CRD to be created on first call")
	}

	// Second call: all CRDs already exist, should warn but not error.
	created2, err := applyCRDs(ctx, fakeDyn, &stderr)
	if err != nil {
		t.Fatalf("second applyCRDs failed: %v", err)
	}
	if created2 != 0 {
		t.Errorf("expected 0 CRDs created on second call, got %d", created2)
	}

	if !strings.Contains(stderr.String(), "already exists") {
		t.Errorf("expected 'already exists' warnings on stderr, got: %s", stderr.String())
	}
}

// ─── TestMultiDocCRDYAML ──────────────────────────────────────────────────

func TestMultiDocCRDYAML(t *testing.T) {
	// Read one CRD file that might contain multiple documents.
	// Most CRD files are single-document, but we test the parsing path for
	// multi-document YAML by creating a synthetic multi-doc byte slice.
	multiDoc := []byte(`apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: testcrd1.example.com
spec:
  group: example.com
  versions:
  - name: v1
    served: true
    storage: true
    schema:
      openAPIV3Schema:
        type: object
  scope: Namespaced
  names:
    plural: testcrd1s
    singular: testcrd1
    kind: TestCrd1
---
apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: testcrd2.example.com
spec:
  group: example.com
  versions:
  - name: v1
    served: true
    storage: true
    schema:
      openAPIV3Schema:
        type: object
  scope: Namespaced
  names:
    plural: testcrd2s
    singular: testcrd2
    kind: TestCrd2`)

	docs, err := ParseMultiDocYAML(multiDoc)
	if err != nil {
		t.Fatalf("ParseMultiDocYAML failed: %v", err)
	}
	if len(docs) != 2 {
		t.Fatalf("expected 2 documents, got %d", len(docs))
	}

	scheme := runtime.NewScheme()
	fakeDyn := fake.NewSimpleDynamicClient(scheme)
	var stderr bytes.Buffer

	for _, doc := range docs {
		_, err := fakeDyn.Resource(crdGVR).Create(ctx, doc, metav1.CreateOptions{})
		if err != nil {
			t.Fatalf("failed to create CRD %s: %v", doc.GetName(), err)
		}
	}

	// Verify both CRDs are created by checking AlreadyExists on reapply.
	_, err = fakeDyn.Resource(crdGVR).Create(ctx, docs[0], metav1.CreateOptions{})
	if !apierrors.IsAlreadyExists(err) {
		t.Errorf("expected AlreadyExists for first CRD, got: %v", err)
	}
	_, err = fakeDyn.Resource(crdGVR).Create(ctx, docs[1], metav1.CreateOptions{})
	if !apierrors.IsAlreadyExists(err) {
		t.Errorf("expected AlreadyExists for second CRD, got: %v", err)
	}
	_ = stderr
}

// ─── Helper: fake clients with pod support ─────────────────────────────────

// fakeDeployment creates a typed apps Deployment object suitable for the fake clientset.
func fakeDeployment(name, namespace string, availableReplicas int32, labels map[string]string) *appsv1.Deployment {
	if labels == nil {
		labels = map[string]string{"app": "test"}
	}
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			UID:       types.UID("test-uid-" + name),
			Generation: 1,
		},
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{MatchLabels: labels},
			Replicas: int32Ptr(1),
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: labels,
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{Name: "manager", Image: "controller:latest"},
					},
				},
			},
		},
		Status: appsv1.DeploymentStatus{
			AvailableReplicas: availableReplicas,
			Replicas:          1,
		},
	}
}

// fakeReplicaSet creates a ReplicaSet with owner reference to the given deployment.
func fakeReplicaSet(name, namespace string, generation, observedGeneration int64, labels, podLabels map[string]string, depUID types.UID) *appsv1.ReplicaSet {
	if labels == nil {
		labels = map[string]string{"app": "test"}
	}
	if podLabels == nil {
		podLabels = labels
	}
	return &appsv1.ReplicaSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels:    labels,
			OwnerReferences: []metav1.OwnerReference{
				{APIVersion: "apps/v1", Kind: "Deployment", Name: "controller-manager", UID: depUID, Controller: boolPtr(true)},
			},
			Generation: generation,
		},
		Spec: appsv1.ReplicaSetSpec{
			Selector: &metav1.LabelSelector{MatchLabels: labels},
			Replicas: int32Ptr(1),
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: podLabels},
				Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "manager", Image: "controller:latest"}}},
			},
		},
		Status: appsv1.ReplicaSetStatus{
			ObservedGeneration: observedGeneration,
			Replicas:           1,
			AvailableReplicas:  1,
		},
	}
}

// fakePod creates a Pod with owner reference to the given ReplicaSet.
func fakePod(name, namespace string, waiting *corev1.ContainerStateWaiting, labels map[string]string, rsUID types.UID) *corev1.Pod {
	if labels == nil {
		labels = map[string]string{"app": "test"}
	}
	state := corev1.ContainerState{}
	if waiting != nil {
		state.Waiting = waiting
	}
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels:    labels,
			OwnerReferences: []metav1.OwnerReference{
				{APIVersion: "apps/v1", Kind: "ReplicaSet", Name: "test-rs", UID: rsUID, Controller: boolPtr(true)},
			},
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{Name: "manager", Image: "controller:latest"},
			},
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			ContainerStatuses: []corev1.ContainerStatus{
				{
					Name:  "manager",
					State: state,
				},
			},
		},
	}
}

func int32Ptr(v int32) *int32    { return &v }
func boolPtr(v bool) *bool       { return &v }

// ─── TestWaitForReadySuccess ──────────────────────────────────────────────

func TestWaitForReadySuccess(t *testing.T) {
	ctx := context.Background()
	fakeClient := k8sfake.NewSimpleClientset()

	// Create a Deployment that is already ready.
	dep := fakeDeployment("controller-manager", "foundry-system", 1, map[string]string{"control-plane": "controller-manager", "app.kubernetes.io/name": "operator"})
	_, err := fakeClient.AppsV1().Deployments("foundry-system").Create(ctx, dep, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("failed to create deployment: %v", err)
	}

	var stderr bytes.Buffer
	err = waitForOperatorReady(ctx, fakeClient, "foundry-system", 5*time.Second, &stderr)
	if err != nil {
		t.Fatalf("waitForOperatorReady failed: %v", err)
	}
}

// ─── TestWaitForReadyTimeout ─────────────────────────────────────────────

func TestWaitForReadyTimeout(t *testing.T) {
	ctx := context.Background()
	fakeClient := k8sfake.NewSimpleClientset()

	// Create a Deployment with 0 AvailableReplicas that never changes.
	dep := fakeDeployment("controller-manager", "foundry-system", 0, map[string]string{"control-plane": "controller-manager", "app.kubernetes.io/name": "operator"})
	_, err := fakeClient.AppsV1().Deployments("foundry-system").Create(ctx, dep, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("failed to create deployment: %v", err)
	}

	var stderr bytes.Buffer
	err = waitForOperatorReady(ctx, fakeClient, "foundry-system", 1*time.Second, &stderr)
	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
	if !strings.Contains(err.Error(), "did not become ready") {
		t.Errorf("error message mismatch: %v", err)
	}
}

// ─── TestPodWatcherReportsWaiting ────────────────────────────────────────

func TestPodWatcherReportsWaiting(t *testing.T) {
	ctx := context.Background()
	fakeClient := k8sfake.NewSimpleClientset()

	dep := fakeDeployment("controller-manager", "foundry-system", 0, map[string]string{"app": "test"})
	dep.Generation = 1
	dep.UID = types.UID("dep-uid-1")
	_, err := fakeClient.AppsV1().Deployments("foundry-system").Create(ctx, dep, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("failed to create deployment: %v", err)
	}

	// Create a ReplicaSet owned by the deployment, current.
	rs := fakeReplicaSet("test-rs-abc", "foundry-system", 1, 1, map[string]string{"app": "test"}, nil, dep.UID)
	_, err = fakeClient.AppsV1().ReplicaSets("foundry-system").Create(ctx, rs, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("failed to create ReplicaSet: %v", err)
	}

	// Create a Pod with ImagePullBackOff waiting state.
	pod := fakePod("test-pod-xyz", "foundry-system",
		&corev1.ContainerStateWaiting{
			Reason:  "ImagePullBackOff",
			Message: `Back-off pulling image "controller:bad-tag"`,
		},
		map[string]string{"app": "test"},
		rs.UID,
	)
	_, err = fakeClient.CoreV1().Pods("foundry-system").Create(ctx, pod, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("failed to create pod: %v", err)
	}

	var stderr bytes.Buffer

	// Use a short timeout; the failing pod should show in stderr before timeout.
	err = waitForOperatorReady(ctx, fakeClient, "foundry-system", 2*time.Second, &stderr)
	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}

	if !strings.Contains(stderr.String(), "ImagePullBackOff") {
		t.Errorf("expected ImagePullBackOff in stderr, got: %s", stderr.String())
	}
}

// ─── TestWaitForReadyNoReplicaSetYet ─────────────────────────────────────

func TestWaitForReadyNoReplicaSetYet(t *testing.T) {
	ctx := context.Background()
	fakeClient := k8sfake.NewSimpleClientset()

	// Deployment exists but with 0 replicas ready and no ReplicaSet.
	dep := fakeDeployment("controller-manager", "foundry-system", 0, map[string]string{"app": "test"})
	_, err := fakeClient.AppsV1().Deployments("foundry-system").Create(ctx, dep, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("failed to create deployment: %v", err)
	}

	var stderr bytes.Buffer
	// Should keep polling without crashing until timeout.
	err = waitForOperatorReady(ctx, fakeClient, "foundry-system", 1*time.Second, &stderr)
	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
}

// ─── TestWaitForReadyMultipleReplicaSets ─────────────────────────────────

func TestWaitForReadyMultipleReplicaSets(t *testing.T) {
	ctx := context.Background()
	fakeClient := k8sfake.NewSimpleClientset()

	dep := fakeDeployment("controller-manager", "foundry-system", 0, map[string]string{"app": "test"})
	dep.Generation = 2
	dep.UID = types.UID("dep-uid-multi")
	_, err := fakeClient.AppsV1().Deployments("foundry-system").Create(ctx, dep, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("failed to create deployment: %v", err)
	}

	// Stale ReplicaSet (observedGeneration < generation).
	staleRS := fakeReplicaSet("test-rs-old", "foundry-system", dep.Generation, 1, map[string]string{"app": "test"}, nil, dep.UID)
	_, err = fakeClient.AppsV1().ReplicaSets("foundry-system").Create(ctx, staleRS, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("failed to create stale ReplicaSet: %v", err)
	}

	// Current ReplicaSet (observedGeneration == generation).
	currentRS := fakeReplicaSet("test-rs-current", "foundry-system", dep.Generation, dep.Generation, map[string]string{"app": "test"}, nil, dep.UID)
	currentRS.Status.AvailableReplicas = 1
	_, err = fakeClient.AppsV1().ReplicaSets("foundry-system").Create(ctx, currentRS, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("failed to create current ReplicaSet: %v", err)
	}

	// Pod owned by the current ReplicaSet with no waiting state.
	pod := fakePod("test-pod-current", "foundry-system", nil, map[string]string{"app": "test"}, currentRS.UID)
	_, err = fakeClient.CoreV1().Pods("foundry-system").Create(ctx, pod, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("failed to create pod: %v", err)
	}

	var stderr bytes.Buffer
	// Use short timeout; function should keep polling without crashing.
	err = waitForOperatorReady(ctx, fakeClient, "foundry-system", 1*time.Second, &stderr)
	if err == nil {
		t.Fatal("expected timeout error (AvailableReplicas is 0 on Deployment), got nil")
	}

	// Now make the deployment ready by updating its status.
	dep.Status.AvailableReplicas = 1
	_, err = fakeClient.AppsV1().Deployments("foundry-system").Update(ctx, dep, metav1.UpdateOptions{})
	if err != nil {
		t.Fatalf("failed to update deployment status: %v", err)
	}
}

// ─── TestImageTagInDryRun ────────────────────────────────────────────────

func TestImageTagInDryRun(t *testing.T) {
	// Verify that --version flag changes the image tag in dry-run output.
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	err := Bootstrap(context.Background(), "", "v0.1.0", true, &stdout, &stderr)
	if err != nil {
		t.Fatalf("Bootstrap dry-run failed: %v", err)
	}

	output := stdout.String()
	if !strings.Contains(output, "controller:v0.1.0") {
		t.Errorf("dry-run output should contain image tag 'controller:v0.1.0', got:\n%s", output)
	}
}
