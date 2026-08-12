/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	flowv1 "github.com/foundry/flow/operator/api/v1"
)

func TestDeploymentEnvVars(t *testing.T) {
	r := &FoundryGraphReconciler{
		CartographerPort:          50051,
		CartographerImage:         "flow-cartographer:latest",
		CapabilityStalenessWindow: "30s",
	}

	fg := &flowv1.FoundryGraph{}
	fg.Name = defaultGraphName
	fg.Spec.Versioning = &flowv1.VersioningSpec{
		Remote: &flowv1.RemoteConfig{
			URL: "https://github.com/org/repo.git",
		},
	}

	env := r.deploymentEnvVars(fg)

	envMap := make(map[string]corev1.EnvVar, len(env))
	for _, e := range env {
		envMap[e.Name] = e
	}

	// Verify standard env vars.
	expected := []string{
		"LADYBUG_DB_PATH",
		"CARTOGRAPHER_PORT",
		"TRANSACTION_TIMEOUT",
		"REMOTE_PULL_ON_INIT",
		"OPERATOR_VERIFICATION_KEY",
		"SIDECAR_VERIFICATION_KEY",
		"POD_NAMESPACE",
		"CAPABILITY_STALENESS_WINDOW",
	}
	for _, name := range expected {
		if _, ok := envMap[name]; !ok {
			t.Errorf("expected env var %q not found", name)
		}
	}

	// Verify REMOTE_URL is set.
	if e, ok := envMap["REMOTE_URL"]; !ok || e.Value != "https://github.com/org/repo.git" {
		t.Errorf("expected REMOTE_URL=https://github.com/org/repo.git, got %v", e)
	}

	// Verify CARTOGRAPHER_PORT.
	if e, ok := envMap["CARTOGRAPHER_PORT"]; !ok || e.Value != "50051" {
		t.Errorf("expected CARTOGRAPHER_PORT=50051, got %v", e)
	}
}

// TestDeploymentEnvVarsPullOnInit exercises the two REMOTE_PULL_ON_INIT value branches
// (foundrygraph_infra.go:301-304): the env var must be rendered from the CRD spec field
// versioning.remote.pullOnInit — "true" when the field is set, "false" when it is unset
// (SPEC R6 step 3: REMOTE_PULL_ON_INIT set from the corresponding CRD spec field; SPEC R5
// table: default false). A wrong value in either branch must fail the test.
func TestDeploymentEnvVarsPullOnInit(t *testing.T) {
	r := &FoundryGraphReconciler{CartographerPort: 50051, CapabilityStalenessWindow: "30s"}

	t.Run("true when pullOnInit is set", func(t *testing.T) {
		fg := &flowv1.FoundryGraph{
			ObjectMeta: metav1.ObjectMeta{Name: defaultGraphName},
			Spec: flowv1.FoundryGraphSpec{
				Versioning: &flowv1.VersioningSpec{
					Remote: &flowv1.RemoteConfig{
						URL:        "https://github.com/org/repo.git",
						PullOnInit: true,
					},
				},
			},
		}

		env := r.deploymentEnvVars(fg)
		envMap := make(map[string]corev1.EnvVar, len(env))
		for _, e := range env {
			envMap[e.Name] = e
		}
		if e, ok := envMap["REMOTE_PULL_ON_INIT"]; !ok || e.Value != "true" {
			t.Errorf("expected REMOTE_PULL_ON_INIT=true from versioning.remote.pullOnInit, got %+v (present=%v)", e, ok)
		}
	})

	t.Run("false when pullOnInit is unset", func(t *testing.T) {
		fg := &flowv1.FoundryGraph{
			ObjectMeta: metav1.ObjectMeta{Name: defaultGraphName},
			Spec: flowv1.FoundryGraphSpec{
				Versioning: &flowv1.VersioningSpec{
					Remote: &flowv1.RemoteConfig{
						URL: "https://github.com/org/repo.git",
					},
				},
			},
		}

		env := r.deploymentEnvVars(fg)
		envMap := make(map[string]corev1.EnvVar, len(env))
		for _, e := range env {
			envMap[e.Name] = e
		}
		if e, ok := envMap["REMOTE_PULL_ON_INIT"]; !ok || e.Value != "false" {
			t.Errorf("expected REMOTE_PULL_ON_INIT=false when pullOnInit is unset (SPEC R5 default), got %+v (present=%v)", e, ok)
		}
	})
}

// TestDeploymentEnvVarsRemoteAuthSecretRef exercises the REMOTE_AUTH_SECRET_REF env var
// branch (foundrygraph_infra.go:300-302): when secretRef is set on the remote auth
// config, the env var must be populated with the secret name.
func TestDeploymentEnvVarsRemoteAuthSecretRef(t *testing.T) {
	r := &FoundryGraphReconciler{
		CartographerPort:          50051,
		CartographerImage:         "flow-cartographer:latest",
		CapabilityStalenessWindow: "30s",
	}

	fg := &flowv1.FoundryGraph{
		ObjectMeta: metav1.ObjectMeta{Name: defaultGraphName},
		Spec: flowv1.FoundryGraphSpec{
			Versioning: &flowv1.VersioningSpec{
				Remote: &flowv1.RemoteConfig{
					URL: "https://github.com/org/repo.git",
					Auth: &flowv1.RemoteAuth{
						SecretRef: "remote-creds",
					},
				},
			},
		},
	}

	env := r.deploymentEnvVars(fg)
	envMap := make(map[string]corev1.EnvVar, len(env))
	for _, e := range env {
		envMap[e.Name] = e
	}

	// Verify REMOTE_AUTH_SECRET_REF is set when secretRef is non-empty.
	if e, ok := envMap["REMOTE_AUTH_SECRET_REF"]; !ok || e.Value != "remote-creds" {
		t.Errorf("expected REMOTE_AUTH_SECRET_REF=remote-creds, got %+v (present=%v)", e, ok)
	}
}

// TestDeploymentEnvVarsNoRemoteAuthSecretRef verifies that REMOTE_AUTH_SECRET_REF is
// absent when secretRef is empty or absent (the default path).
func TestDeploymentEnvVarsNoRemoteAuthSecretRef(t *testing.T) {
	r := &FoundryGraphReconciler{
		CartographerPort:          50051,
		CapabilityStalenessWindow: "30s",
	}

	// No auth config at all.
	fg := &flowv1.FoundryGraph{
		ObjectMeta: metav1.ObjectMeta{Name: defaultGraphName},
		Spec: flowv1.FoundryGraphSpec{
			Versioning: &flowv1.VersioningSpec{
				Remote: &flowv1.RemoteConfig{
					URL: "https://github.com/org/repo.git",
				},
			},
		},
	}

	env := r.deploymentEnvVars(fg)
	envMap := make(map[string]corev1.EnvVar, len(env))
	for _, e := range env {
		envMap[e.Name] = e
	}

	if _, ok := envMap["REMOTE_AUTH_SECRET_REF"]; ok {
		t.Error("REMOTE_AUTH_SECRET_REF must not be set when secretRef is absent")
	}
}

// TestDeploymentEnvVarsRemoteURLAbsent exercises the REMOTE_URL-absent branch
// (foundrygraph_infra.go: REMOTE_URL is appended only when the remote URL is non-empty):
// a FoundryGraph without versioning.remote.url must not render the env var at all (SPEC
// R5: REMOTE_URL has no default).
func TestDeploymentEnvVarsRemoteURLAbsent(t *testing.T) {
	r := &FoundryGraphReconciler{
		CartographerPort:          50051,
		CapabilityStalenessWindow: "30s",
	}
	fg := &flowv1.FoundryGraph{
		ObjectMeta: metav1.ObjectMeta{Name: defaultGraphName},
		Spec: flowv1.FoundryGraphSpec{
			Versioning: &flowv1.VersioningSpec{
				Remote: &flowv1.RemoteConfig{
					Auth: &flowv1.RemoteAuth{SecretRef: "remote-creds"},
				},
			},
		},
	}

	env := r.deploymentEnvVars(fg)
	envMap := make(map[string]corev1.EnvVar, len(env))
	for _, e := range env {
		envMap[e.Name] = e
	}

	if _, ok := envMap["REMOTE_URL"]; ok {
		t.Error("REMOTE_URL must be omitted when versioning.remote.url is empty (SPEC R5: no default)")
	}
	// The auth secret ref (independent branch) must still be rendered.
	if e, ok := envMap["REMOTE_AUTH_SECRET_REF"]; !ok || e.Value != "remote-creds" {
		t.Errorf("expected REMOTE_AUTH_SECRET_REF=remote-creds alongside the absent REMOTE_URL, got %+v (present=%v)", e, ok)
	}
}

// TestDeploymentEnvVarsEventBusAddressPresent exercises the EVENT_BUS_ADDRESS-present
// branch (foundrygraph_infra.go: EVENT_BUS_ADDRESS is appended from the reconciler's
// runtime configuration when non-empty): the operator's EventBusAddress must be rendered
// as the env var value (SPEC R5/R6 step 3).
func TestDeploymentEnvVarsEventBusAddressPresent(t *testing.T) {
	r := &FoundryGraphReconciler{
		CartographerPort:          50051,
		EventBusAddress:           "eventbus.foundry-flow.svc.cluster.local:50054",
		CapabilityStalenessWindow: "30s",
	}
	fg := &flowv1.FoundryGraph{ObjectMeta: metav1.ObjectMeta{Name: defaultGraphName}}

	env := r.deploymentEnvVars(fg)
	envMap := make(map[string]corev1.EnvVar, len(env))
	for _, e := range env {
		envMap[e.Name] = e
	}

	if e, ok := envMap["EVENT_BUS_ADDRESS"]; !ok || e.Value != "eventbus.foundry-flow.svc.cluster.local:50054" {
		t.Errorf("expected EVENT_BUS_ADDRESS from the operator's runtime configuration, got %+v (present=%v)", e, ok)
	}
}

// TestDeploymentEnvVarsEventBusAddressAbsent exercises the EVENT_BUS_ADDRESS-absent
// branch: when the reconciler's EventBusAddress is empty the env var must be omitted
// (SPEC R5: EVENT_BUS_ADDRESS has no default — the Cartographer's proxy is disabled).
func TestDeploymentEnvVarsEventBusAddressAbsent(t *testing.T) {
	r := &FoundryGraphReconciler{
		CartographerPort:          50051,
		CapabilityStalenessWindow: "30s",
	}
	fg := &flowv1.FoundryGraph{ObjectMeta: metav1.ObjectMeta{Name: defaultGraphName}}

	env := r.deploymentEnvVars(fg)
	envMap := make(map[string]corev1.EnvVar, len(env))
	for _, e := range env {
		envMap[e.Name] = e
	}

	if _, ok := envMap["EVENT_BUS_ADDRESS"]; ok {
		t.Error("EVENT_BUS_ADDRESS must be omitted when the operator has no Event Bus address (SPEC R5: no default)")
	}
}

// TestDeploymentEnvVarsCapabilityStalenessWindowFallback exercises the "30s" default
// fallback branch of CAPABILITY_STALENESS_WINDOW (foundrygraph_infra.go): when the
// reconciler's CapabilityStalenessWindow is empty the env var must fall back to the SPEC
// R5 default "30s".
func TestDeploymentEnvVarsCapabilityStalenessWindowFallback(t *testing.T) {
	r := &FoundryGraphReconciler{
		CartographerPort: 50051,
		// CapabilityStalenessWindow left empty → the "30s" fallback must apply.
	}
	fg := &flowv1.FoundryGraph{ObjectMeta: metav1.ObjectMeta{Name: defaultGraphName}}

	env := r.deploymentEnvVars(fg)
	envMap := make(map[string]corev1.EnvVar, len(env))
	for _, e := range env {
		envMap[e.Name] = e
	}

	if e, ok := envMap["CAPABILITY_STALENESS_WINDOW"]; !ok || e.Value != "30s" {
		t.Errorf("expected CAPABILITY_STALENESS_WINDOW to fall back to the SPEC R5 default 30s, got %+v (present=%v)", e, ok)
	}
}

// TestDeploymentEnvVarsCapabilityStalenessWindowConfigured exercises the configured-value
// branch: a non-empty reconciler CapabilityStalenessWindow must be rendered verbatim
// rather than the default.
func TestDeploymentEnvVarsCapabilityStalenessWindowConfigured(t *testing.T) {
	r := &FoundryGraphReconciler{
		CartographerPort:          50051,
		CapabilityStalenessWindow: "1m",
	}
	fg := &flowv1.FoundryGraph{ObjectMeta: metav1.ObjectMeta{Name: defaultGraphName}}

	env := r.deploymentEnvVars(fg)
	envMap := make(map[string]corev1.EnvVar, len(env))
	for _, e := range env {
		envMap[e.Name] = e
	}

	if e, ok := envMap["CAPABILITY_STALENESS_WINDOW"]; !ok || e.Value != "1m" {
		t.Errorf("expected CAPABILITY_STALENESS_WINDOW to carry the configured value 1m, got %+v (present=%v)", e, ok)
	}
}

// TestDeploymentEnvVarsTransactionTimeout exercises the two TRANSACTION_TIMEOUT branches
// (foundrygraph_infra.go: the spec duration is forwarded as the env value when set; the
// SPEC R5 default "30m" applies when absent): a wrong value in either branch must fail
// the test.
func TestDeploymentEnvVarsTransactionTimeout(t *testing.T) {
	t.Run("spec duration forwarded as the env value", func(t *testing.T) {
		r := &FoundryGraphReconciler{CartographerPort: 50051, CapabilityStalenessWindow: "30s"}
		fg := &flowv1.FoundryGraph{
			ObjectMeta: metav1.ObjectMeta{Name: defaultGraphName},
			Spec: flowv1.FoundryGraphSpec{
				Versioning: &flowv1.VersioningSpec{
					TransactionTimeout: &metav1.Duration{Duration: 45 * time.Minute},
				},
			},
		}

		env := r.deploymentEnvVars(fg)
		envMap := make(map[string]corev1.EnvVar, len(env))
		for _, e := range env {
			envMap[e.Name] = e
		}
		if e, ok := envMap["TRANSACTION_TIMEOUT"]; !ok || e.Value != "45m0s" {
			t.Errorf("expected TRANSACTION_TIMEOUT to forward the spec duration 45m0s, got %+v (present=%v)", e, ok)
		}
	})

	t.Run("30m fallback when the spec omits transactionTimeout", func(t *testing.T) {
		r := &FoundryGraphReconciler{CartographerPort: 50051, CapabilityStalenessWindow: "30s"}
		fg := &flowv1.FoundryGraph{ObjectMeta: metav1.ObjectMeta{Name: defaultGraphName}}

		env := r.deploymentEnvVars(fg)
		envMap := make(map[string]corev1.EnvVar, len(env))
		for _, e := range env {
			envMap[e.Name] = e
		}
		if e, ok := envMap["TRANSACTION_TIMEOUT"]; !ok || e.Value != transactionTimeoutDefault {
			t.Errorf("expected TRANSACTION_TIMEOUT to fall back to the SPEC R5 default 30m, got %+v (present=%v)", e, ok)
		}
	})
}

func TestLabelsForCartographer(t *testing.T) {
	r := &FoundryGraphReconciler{}
	fg := &flowv1.FoundryGraph{}
	fg.Name = defaultGraphName

	labels := r.labelsForCartographer(fg)
	if labels["app.kubernetes.io/component"] != "cartographer" {
		t.Errorf("expected component=cartographer, got %q", labels["app.kubernetes.io/component"])
	}
	if labels["app.kubernetes.io/instance"] != defaultGraphName {
		t.Errorf("expected instance=flow-graph, got %q", labels["app.kubernetes.io/instance"])
	}
	if labels["app.kubernetes.io/managed-by"] != managedByOperator {
		t.Errorf("expected managed-by=%q, got %q", managedByOperator, labels["app.kubernetes.io/managed-by"])
	}
}

func TestCartographerServiceName(t *testing.T) {
	r := &FoundryGraphReconciler{}
	fg := &flowv1.FoundryGraph{}
	fg.Name = defaultGraphName

	name := r.cartographerServiceName(fg)
	if name != cartographerSvcName {
		t.Errorf("expected cartographer-flow-graph, got %q", name)
	}
}

func TestCartographerPodSecurityContext(t *testing.T) {
	s := scheme.Scheme
	_ = flowv1.AddToScheme(s)
	_ = appsv1.AddToScheme(s)
	_ = corev1.AddToScheme(s)

	fg := &flowv1.FoundryGraph{
		ObjectMeta: metav1.ObjectMeta{
			Name:      defaultGraphName,
			Namespace: testNS,
		},
	}

	fakeCli := fake.NewClientBuilder().WithScheme(s).WithObjects(fg).Build()
	r := &FoundryGraphReconciler{
		Client:            fakeCli,
		Scheme:            s,
		CartographerPort:  50051,
		CartographerImage: "flow-cartographer:latest",
	}

	ctx := context.Background()
	if err := r.reconcileDeployment(ctx, fg); err != nil {
		t.Fatalf("reconcileDeployment: %v", err)
	}

	var deploy appsv1.Deployment
	if err := fakeCli.Get(ctx, client.ObjectKey{Name: cartographerSvcName, Namespace: testNS}, &deploy); err != nil {
		t.Fatalf("get Deployment: %v", err)
	}

	psc := deploy.Spec.Template.Spec.SecurityContext
	if psc == nil {
		t.Fatal("PodSecurityContext is nil")
	}

	// terminationGracePeriodSeconds must exceed the GracefulStop drain so durability
	// teardown completes before kubelet SIGKILL (cartographer deployment.yaml: 100s).
	if deploy.Spec.Template.Spec.TerminationGracePeriodSeconds == nil {
		t.Fatal("TerminationGracePeriodSeconds is nil — durability teardown could be SIGKILLed mid-write")
	}
	if *deploy.Spec.Template.Spec.TerminationGracePeriodSeconds < 30 {
		t.Errorf("expected TerminationGracePeriodSeconds >= 30 (GracefulStop budget), got %d", *deploy.Spec.Template.Spec.TerminationGracePeriodSeconds)
	}

	if psc.FSGroup == nil {
		t.Fatal("FSGroup is nil — root-owned PVC will not be writable by UID 65532")
	}
	if *psc.FSGroup != 65532 {
		t.Errorf("expected FSGroup=65532, got %d", *psc.FSGroup)
	}
	if psc.RunAsUser == nil || *psc.RunAsUser != 65532 {
		t.Errorf("expected RunAsUser=65532, got %v", psc.RunAsUser)
	}
	if psc.RunAsGroup == nil || *psc.RunAsGroup != 65532 {
		t.Errorf("expected RunAsGroup=65532, got %v", psc.RunAsGroup)
	}
	if psc.FSGroupChangePolicy == nil || *psc.FSGroupChangePolicy != corev1.FSGroupChangeOnRootMismatch {
		t.Errorf("expected FSGroupChangePolicy=OnRootMismatch, got %v", psc.FSGroupChangePolicy)
	}

	// SPEC R6 step 3: readiness probe must be gRPC HealthCheck on CARTOGRAPHER_PORT
	// with InitialDelaySeconds=5 and PeriodSeconds=10.
	containers := deploy.Spec.Template.Spec.Containers
	if len(containers) != 1 {
		t.Fatalf("expected 1 container, got %d", len(containers))
	}
	probe := containers[0].ReadinessProbe
	if probe == nil {
		t.Fatal("expected readiness probe to be set")
	}
	if probe.GRPC == nil {
		t.Fatal("expected readiness probe to use gRPC action (grpc.health.v1.Health/Check)")
	}
	if probe.GRPC.Port != 50051 {
		t.Errorf("expected readiness probe gRPC port=CARTOGRAPHER_PORT (50051), got %d", probe.GRPC.Port)
	}
	if probe.InitialDelaySeconds != 5 {
		t.Errorf("expected readiness probe InitialDelaySeconds=5, got %d", probe.InitialDelaySeconds)
	}
	if probe.PeriodSeconds != 10 {
		t.Errorf("expected readiness probe PeriodSeconds=10, got %d", probe.PeriodSeconds)
	}
}

func TestPVCStorageDefaulting(t *testing.T) {
	// Verify cartographerStorageSize constant is "1Gi".
	if cartographerStorageSize != "1Gi" {
		t.Errorf("expected cartographerStorageSize=1Gi, got %q", cartographerStorageSize)
	}

	// Verify 1Gi resolves to 1*1024*1024*1024 bytes.
	qty := resource.MustParse(cartographerStorageSize)
	if qty.Value() != 1*1024*1024*1024 {
		t.Errorf("expected 1Gi, got %v", qty)
	}
}

func TestReconcilePVCDefaultsToConstant(t *testing.T) {
	s := scheme.Scheme
	_ = flowv1.AddToScheme(s)
	_ = corev1.AddToScheme(s)

	fg := &flowv1.FoundryGraph{ObjectMeta: metav1.ObjectMeta{Name: defaultGraphName, Namespace: testNS}}
	fakeCli := fake.NewClientBuilder().WithScheme(s).WithObjects(fg).Build()
	r := &FoundryGraphReconciler{Client: fakeCli, Scheme: s}

	ctx := context.Background()
	if err := r.reconcilePVC(ctx, fg); err != nil {
		t.Fatalf("reconcilePVC: %v", err)
	}

	var pvc corev1.PersistentVolumeClaim
	if err := fakeCli.Get(ctx, client.ObjectKey{Name: "data-flow-graph", Namespace: testNS}, &pvc); err != nil {
		t.Fatalf("get PVC: %v", err)
	}
	got := pvc.Spec.Resources.Requests[corev1.ResourceStorage]
	if got.Value() != 1*1024*1024*1024 {
		t.Errorf("expected default size 1Gi, got %v", got)
	}
}

func TestReconcilePVCMinimumClamp(t *testing.T) {
	s := scheme.Scheme
	_ = flowv1.AddToScheme(s)
	_ = corev1.AddToScheme(s)

	tiny := resource.MustParse("100Ki")
	fg := &flowv1.FoundryGraph{
		ObjectMeta: metav1.ObjectMeta{Name: defaultGraphName, Namespace: testNS},
		Spec: flowv1.FoundryGraphSpec{
			Storage: &flowv1.StorageSpec{Size: &tiny},
		},
	}
	fakeCli := fake.NewClientBuilder().WithScheme(s).WithObjects(fg).Build()
	r := &FoundryGraphReconciler{Client: fakeCli, Scheme: s}

	ctx := context.Background()
	if err := r.reconcilePVC(ctx, fg); err != nil {
		t.Fatalf("reconcilePVC: %v", err)
	}

	var pvc corev1.PersistentVolumeClaim
	if err := fakeCli.Get(ctx, client.ObjectKey{Name: "data-flow-graph", Namespace: testNS}, &pvc); err != nil {
		t.Fatalf("get PVC: %v", err)
	}
	got := pvc.Spec.Resources.Requests[corev1.ResourceStorage]
	if got.Value() < 1*1024*1024 {
		t.Errorf("expected request clamped to at least 1Mi, got %v", got)
	}
	if got.Value() != 1*1024*1024 {
		t.Errorf("expected request exactly 1Mi after clamp, got %v", got)
	}
}

func TestReconcilePVCNeverShrinks(t *testing.T) {
	s := scheme.Scheme
	_ = flowv1.AddToScheme(s)
	_ = corev1.AddToScheme(s)

	large := resource.MustParse("5Gi")
	small := resource.MustParse("1Gi")
	fg := &flowv1.FoundryGraph{
		ObjectMeta: metav1.ObjectMeta{Name: defaultGraphName, Namespace: testNS},
		Spec: flowv1.FoundryGraphSpec{
			Storage: &flowv1.StorageSpec{Size: &large},
		},
	}

	// Pre-seed an existing PVC already allocated at 5Gi.
	existing := corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "data-flow-graph", Namespace: testNS},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceStorage: large},
			},
		},
	}

	fakeCli := fake.NewClientBuilder().WithScheme(s).WithObjects(fg, &existing).Build()
	r := &FoundryGraphReconciler{Client: fakeCli, Scheme: s}

	ctx := context.Background()

	// Now reduce spec.storage.size to 1Gi — the PVC must silently retain 5Gi.
	fg.Spec.Storage.Size = &small
	if err := r.reconcilePVC(ctx, fg); err != nil {
		t.Fatalf("reconcilePVC: %v", err)
	}

	var pvc corev1.PersistentVolumeClaim
	if err := fakeCli.Get(ctx, client.ObjectKey{Name: "data-flow-graph", Namespace: testNS}, &pvc); err != nil {
		t.Fatalf("get PVC: %v", err)
	}
	got := pvc.Spec.Resources.Requests[corev1.ResourceStorage]
	if got.Value() != 5*1024*1024*1024 {
		t.Errorf("expected PVC to retain 5Gi (never shrink), got %v", got)
	}
}

func TestReconcileRBACRemoteAuthTeardown(t *testing.T) {
	s := scheme.Scheme
	_ = flowv1.AddToScheme(s)
	_ = corev1.AddToScheme(s)
	_ = rbacv1.AddToScheme(s)

	ns := testNS
	fg := &flowv1.FoundryGraph{
		ObjectMeta: metav1.ObjectMeta{Name: defaultGraphName, Namespace: ns},
		// Remote config present but secretRef empty → teardown path.
		Spec: flowv1.FoundryGraphSpec{
			Versioning: &flowv1.VersioningSpec{
				Remote: &flowv1.RemoteConfig{Auth: &flowv1.RemoteAuth{SecretRef: ""}},
			},
		},
	}

	// Pre-create the remote-auth Role and RoleBinding so teardown must delete them.
	remoteRole := &rbacv1.Role{ObjectMeta: metav1.ObjectMeta{Name: "cartographer-flow-graph-remote-auth", Namespace: ns}}
	remoteRB := &rbacv1.RoleBinding{ObjectMeta: metav1.ObjectMeta{Name: "cartographer-flow-graph-remote-auth", Namespace: ns}}

	fakeCli := fake.NewClientBuilder().WithScheme(s).WithObjects(fg, remoteRole, remoteRB).Build()
	r := &FoundryGraphReconciler{Client: fakeCli, Scheme: s}

	ctx := context.Background()
	if err := r.reconcileRBAC(ctx, fg); err != nil {
		t.Fatalf("reconcileRBAC: %v", err)
	}

	// Remote-auth RoleBinding must be deleted.
	var gotRB rbacv1.RoleBinding
	if err := fakeCli.Get(ctx, client.ObjectKey{Name: "cartographer-flow-graph-remote-auth", Namespace: ns}, &gotRB); err == nil {
		t.Fatal("expected remote-auth RoleBinding to be deleted when secretRef is removed")
	}
	var gotRole rbacv1.Role
	if err := fakeCli.Get(ctx, client.ObjectKey{Name: "cartographer-flow-graph-remote-auth", Namespace: ns}, &gotRole); err == nil {
		t.Fatal("expected remote-auth Role to be deleted when secretRef is removed")
	}

	// SPEC R6: "The verification key Role and RoleBinding are unaffected by either
	// change." Tearing down the remote-auth set must leave the key-reader Role and
	// RoleBinding in place, still referencing both verification-key Secrets.
	var keyRole rbacv1.Role
	if err := fakeCli.Get(ctx, client.ObjectKey{Name: keyReaderRoleName, Namespace: ns}, &keyRole); err != nil {
		t.Fatalf("expected the key-reader Role to survive the remote-auth teardown: %v", err)
	}
	if len(keyRole.Rules) != 1 || len(keyRole.Rules[0].ResourceNames) != 2 ||
		keyRole.Rules[0].ResourceNames[0] != operatorKeySecretName || keyRole.Rules[0].ResourceNames[1] != sidecarKeySecretName {
		t.Errorf("key-reader Role must still reference both verification key Secrets, got %+v", keyRole.Rules)
	}
	var keyRB rbacv1.RoleBinding
	if err := fakeCli.Get(ctx, client.ObjectKey{Name: keyReaderRoleName, Namespace: ns}, &keyRB); err != nil {
		t.Fatalf("expected the key-reader RoleBinding to survive the remote-auth teardown: %v", err)
	}
	if keyRB.RoleRef.Name != keyReaderRoleName {
		t.Errorf("key-reader RoleBinding RoleRef mismatch after teardown: %q", keyRB.RoleRef.Name)
	}
}

// TestReconcileRBACRemoteAuthTeardownGetError covers the non-NotFound Get-error branches of
// the remote-auth teardown path (foundrygraph_infra.go:225-226, 234-235): when the
// remote-auth Role or RoleBinding cannot be read for any reason other than absence, the
// teardown must surface the error rather than silently skipping the deletion (SPEC R6:
// "If secretRef is removed entirely ... the Operator tears down only the remote-auth-specific
// Role and RoleBinding" — a read failure must not be swallowed as if the resource were gone).
func TestReconcileRBACRemoteAuthTeardownGetError(t *testing.T) {
	s := scheme.Scheme
	_ = flowv1.AddToScheme(s)
	_ = corev1.AddToScheme(s)
	_ = rbacv1.AddToScheme(s)

	ns := testNS
	fg := &flowv1.FoundryGraph{
		ObjectMeta: metav1.ObjectMeta{Name: defaultGraphName, Namespace: ns},
		// Remote config present but secretRef empty → teardown path.
		Spec: flowv1.FoundryGraphSpec{
			Versioning: &flowv1.VersioningSpec{
				Remote: &flowv1.RemoteConfig{Auth: &flowv1.RemoteAuth{SecretRef: ""}},
			},
		},
	}

	t.Run("role get error surfaces", func(t *testing.T) {
		interceptorFuncs := interceptor.Funcs{
			Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
				// Only the remote-auth Role read fails; everything else (the key-reader
				// CreateOrUpdate Gets and the RoleBinding Get) delegates to the real
				// fake client so NotFound/Create semantics are preserved.
				if key.Name == remoteAuthRoleName {
					if _, ok := obj.(*rbacv1.Role); ok {
						return errors.New("apiserver unavailable")
					}
				}
				return c.Get(ctx, key, obj, opts...)
			},
		}
		fakeCli := fake.NewClientBuilder().WithScheme(s).WithObjects(fg).WithInterceptorFuncs(interceptorFuncs).Build()
		r := &FoundryGraphReconciler{Client: fakeCli, Scheme: s}

		err := r.reconcileRBAC(context.Background(), fg)
		if err == nil {
			t.Fatal("expected the remote-auth Role Get error to surface from the teardown")
		}
		if !strings.Contains(err.Error(), "get remote-auth Role") {
			t.Errorf("expected the error to name the remote-auth Role, got: %v", err)
		}
	})

	t.Run("rolebinding get error surfaces", func(t *testing.T) {
		interceptorFuncs := interceptor.Funcs{
			Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
				if key.Name == remoteAuthRoleName {
					if _, ok := obj.(*rbacv1.RoleBinding); ok {
						return errors.New("apiserver unavailable")
					}
				}
				return c.Get(ctx, key, obj, opts...)
			},
		}
		fakeCli := fake.NewClientBuilder().WithScheme(s).WithObjects(fg).WithInterceptorFuncs(interceptorFuncs).Build()
		r := &FoundryGraphReconciler{Client: fakeCli, Scheme: s}

		err := r.reconcileRBAC(context.Background(), fg)
		if err == nil {
			t.Fatal("expected the remote-auth RoleBinding Get error to surface from the teardown")
		}
		if !strings.Contains(err.Error(), "get remote-auth RoleBinding") {
			t.Errorf("expected the error to name the remote-auth RoleBinding, got: %v", err)
		}
	})
}

// TestReconcileRBACRemoteAuthTeardownDeleteError covers the Delete-failure branches of the
// remote-auth teardown path (foundrygraph_infra.go:222-223, 231-232): a remote-auth Role or
// RoleBinding that exists but cannot be deleted must surface the error so the reconcile
// requeues with backoff instead of silently leaving the stale Role/RoleBinding in place
// (SPEC R6 removal flow — the teardown must be loud on failure, never a silent partial
// cleanup).
func TestReconcileRBACRemoteAuthTeardownDeleteError(t *testing.T) {
	s := scheme.Scheme
	_ = flowv1.AddToScheme(s)
	_ = corev1.AddToScheme(s)
	_ = rbacv1.AddToScheme(s)

	ns := testNS
	fg := &flowv1.FoundryGraph{
		ObjectMeta: metav1.ObjectMeta{Name: defaultGraphName, Namespace: ns},
		// Remote config present but secretRef empty → teardown path.
		Spec: flowv1.FoundryGraphSpec{
			Versioning: &flowv1.VersioningSpec{
				Remote: &flowv1.RemoteConfig{Auth: &flowv1.RemoteAuth{SecretRef: ""}},
			},
		},
	}

	t.Run("role delete error surfaces", func(t *testing.T) {
		// The remote-auth Role exists (Get succeeds) but its Delete fails.
		remoteRole := &rbacv1.Role{ObjectMeta: metav1.ObjectMeta{Name: "cartographer-flow-graph-remote-auth", Namespace: ns}}
		interceptorFuncs := interceptor.Funcs{
			Delete: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
				if _, ok := obj.(*rbacv1.Role); ok {
					return errors.New("delete denied")
				}
				return c.Delete(ctx, obj, opts...)
			},
		}
		fakeCli := fake.NewClientBuilder().WithScheme(s).WithObjects(fg, remoteRole).WithInterceptorFuncs(interceptorFuncs).Build()
		r := &FoundryGraphReconciler{Client: fakeCli, Scheme: s}

		err := r.reconcileRBAC(context.Background(), fg)
		if err == nil {
			t.Fatal("expected the remote-auth Role Delete error to surface from the teardown")
		}
		if !strings.Contains(err.Error(), "delete remote-auth Role") {
			t.Errorf("expected the error to name the remote-auth Role delete, got: %v", err)
		}
	})

	t.Run("rolebinding delete error surfaces", func(t *testing.T) {
		// Both exist; the Role delete succeeds and the RoleBinding delete fails.
		remoteRole := &rbacv1.Role{ObjectMeta: metav1.ObjectMeta{Name: "cartographer-flow-graph-remote-auth", Namespace: ns}}
		remoteRB := &rbacv1.RoleBinding{ObjectMeta: metav1.ObjectMeta{Name: "cartographer-flow-graph-remote-auth", Namespace: ns}}
		interceptorFuncs := interceptor.Funcs{
			Delete: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
				if _, ok := obj.(*rbacv1.RoleBinding); ok {
					return errors.New("delete denied")
				}
				return c.Delete(ctx, obj, opts...)
			},
		}
		fakeCli := fake.NewClientBuilder().WithScheme(s).WithObjects(fg, remoteRole, remoteRB).WithInterceptorFuncs(interceptorFuncs).Build()
		r := &FoundryGraphReconciler{Client: fakeCli, Scheme: s}

		err := r.reconcileRBAC(context.Background(), fg)
		if err == nil {
			t.Fatal("expected the remote-auth RoleBinding Delete error to surface from the teardown")
		}
		if !strings.Contains(err.Error(), "delete remote-auth RoleBinding") {
			t.Errorf("expected the error to name the remote-auth RoleBinding delete, got: %v", err)
		}
	})
}

// TestReconcileRBACCreation verifies the CREATE path (item 8): reconcileRBAC creates the
// ServiceAccount, the verification-key Role/RoleBinding, and, when secretRef is set, the
// remote-auth Role and RoleBinding.
func TestReconcileRBACCreation(t *testing.T) {
	s := scheme.Scheme
	_ = flowv1.AddToScheme(s)
	_ = corev1.AddToScheme(s)
	_ = rbacv1.AddToScheme(s)

	ns := testNS
	fg := &flowv1.FoundryGraph{
		ObjectMeta: metav1.ObjectMeta{Name: defaultGraphName, Namespace: ns},
		Spec: flowv1.FoundryGraphSpec{
			Versioning: &flowv1.VersioningSpec{
				Remote: &flowv1.RemoteConfig{Auth: &flowv1.RemoteAuth{SecretRef: "remote-secret"}},
			},
		},
	}

	fakeCli := fake.NewClientBuilder().WithScheme(s).WithObjects(fg).Build()
	r := &FoundryGraphReconciler{Client: fakeCli, Scheme: s}

	ctx := context.Background()
	if err := r.reconcileRBAC(ctx, fg); err != nil {
		t.Fatalf("reconcileRBAC: %v", err)
	}

	// ServiceAccount created.
	var sa corev1.ServiceAccount
	if err := fakeCli.Get(ctx, client.ObjectKey{Name: cartographerSvcName, Namespace: ns}, &sa); err != nil {
		t.Fatalf("expected ServiceAccount to be created: %v", err)
	}

	// Key-reader Role created with get on the verification-key Secrets.
	var keyRole rbacv1.Role
	if err := fakeCli.Get(ctx, client.ObjectKey{Name: keyReaderRoleName, Namespace: ns}, &keyRole); err != nil {
		t.Fatalf("expected key-reader Role to be created: %v", err)
	}
	if len(keyRole.Rules) != 1 {
		t.Fatalf("expected 1 rule on key-reader Role, got %d", len(keyRole.Rules))
	}
	names := keyRole.Rules[0].ResourceNames
	if len(names) != 2 || names[0] != operatorKeySecretName || names[1] != sidecarKeySecretName {
		t.Errorf("key-reader Role must reference the two verification key Secrets, got %v", names)
	}

	// Key-reader RoleBinding bound to the ServiceAccount.
	var keyRB rbacv1.RoleBinding
	if err := fakeCli.Get(ctx, client.ObjectKey{Name: keyReaderRoleName, Namespace: ns}, &keyRB); err != nil {
		t.Fatalf("expected key-reader RoleBinding to be created: %v", err)
	}
	if keyRB.RoleRef.Name != keyReaderRoleName {
		t.Errorf("key-reader RoleBinding RoleRef mismatch: %q", keyRB.RoleRef.Name)
	}
	if len(keyRB.Subjects) != 1 || keyRB.Subjects[0].Name != cartographerSvcName {
		t.Errorf("key-reader RoleBinding must reference the operator ServiceAccount, got %+v", keyRB.Subjects)
	}

	// Remote-auth Role created for the referenced secret.
	var remoteRole rbacv1.Role
	if err := fakeCli.Get(ctx, client.ObjectKey{Name: "cartographer-flow-graph-remote-auth", Namespace: ns}, &remoteRole); err != nil {
		t.Fatalf("expected remote-auth Role to be created when secretRef is set: %v", err)
	}
	if len(remoteRole.Rules) != 1 || len(remoteRole.Rules[0].ResourceNames) != 1 || remoteRole.Rules[0].ResourceNames[0] != "remote-secret" {
		t.Errorf("remote-auth Role must reference the secretRef, got %+v", remoteRole.Rules)
	}

	// Remote-auth RoleBinding.
	var remoteRB rbacv1.RoleBinding
	if err := fakeCli.Get(ctx, client.ObjectKey{Name: "cartographer-flow-graph-remote-auth", Namespace: ns}, &remoteRB); err != nil {
		t.Fatalf("expected remote-auth RoleBinding to be created: %v", err)
	}
}

// TestReconcileRBACRemoteAuthSecretChanged exercises the mutable-remote-auth path
// (SPEC R6: "If versioning.remote.auth.secretRef changes to a different Secret name, the
// Operator updates the remote-auth-specific Role and RoleBinding to reference the new
// Secret name"). The existing remote-auth Role's ResourceNames must be rewritten from the
// old to the new Secret name, and the RoleBinding must remain bound.
func TestReconcileRBACRemoteAuthSecretChanged(t *testing.T) {
	s := scheme.Scheme
	_ = flowv1.AddToScheme(s)
	_ = corev1.AddToScheme(s)
	_ = rbacv1.AddToScheme(s)

	ns := testNS
	fg := &flowv1.FoundryGraph{
		ObjectMeta: metav1.ObjectMeta{Name: defaultGraphName, Namespace: ns},
		Spec: flowv1.FoundryGraphSpec{
			Versioning: &flowv1.VersioningSpec{
				Remote: &flowv1.RemoteConfig{Auth: &flowv1.RemoteAuth{SecretRef: "new-secret"}},
			},
		},
	}

	// Pre-seed the remote-auth Role and RoleBinding as they would be after an earlier
	// reconcile with secretRef="old-secret".
	remoteRole := &rbacv1.Role{
		ObjectMeta: metav1.ObjectMeta{Name: "cartographer-flow-graph-remote-auth", Namespace: ns},
		Rules: []rbacv1.PolicyRule{{
			APIGroups:     []string{""},
			Resources:     []string{"secrets"},
			ResourceNames: []string{"old-secret"},
			Verbs:         []string{"get"},
		}},
	}
	remoteRB := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: "cartographer-flow-graph-remote-auth", Namespace: ns},
		RoleRef:    rbacv1.RoleRef{APIGroup: "rbac.authorization.k8s.io", Kind: "Role", Name: "cartographer-flow-graph-remote-auth"},
	}

	fakeCli := fake.NewClientBuilder().WithScheme(s).WithObjects(fg, remoteRole, remoteRB).Build()
	r := &FoundryGraphReconciler{Client: fakeCli, Scheme: s}

	ctx := context.Background()
	if err := r.reconcileRBAC(ctx, fg); err != nil {
		t.Fatalf("reconcileRBAC: %v", err)
	}

	// The remote-auth Role's ResourceNames must now reference the new Secret name.
	var gotRole rbacv1.Role
	if err := fakeCli.Get(ctx, client.ObjectKey{Name: "cartographer-flow-graph-remote-auth", Namespace: ns}, &gotRole); err != nil {
		t.Fatalf("get remote-auth Role: %v", err)
	}
	if len(gotRole.Rules) != 1 || len(gotRole.Rules[0].ResourceNames) != 1 || gotRole.Rules[0].ResourceNames[0] != "new-secret" {
		t.Errorf("expected remote-auth Role updated to reference %q, got %+v", "new-secret", gotRole.Rules)
	}

	// The remote-auth RoleBinding must not be torn down or re-created pointing elsewhere.
	var gotRB rbacv1.RoleBinding
	if err := fakeCli.Get(ctx, client.ObjectKey{Name: "cartographer-flow-graph-remote-auth", Namespace: ns}, &gotRB); err != nil {
		t.Fatalf("get remote-auth RoleBinding: %v", err)
	}
	if gotRB.RoleRef.Name != "cartographer-flow-graph-remote-auth" {
		t.Errorf("remote-auth RoleBinding RoleRef changed unexpectedly: %q", gotRB.RoleRef.Name)
	}

	// SPEC R6: "The verification key Role and RoleBinding are unaffected by either
	// change." Updating the remote-auth set for a new Secret name must leave the
	// key-reader Role and RoleBinding untouched, still referencing both verification-key
	// Secrets.
	var keyRole rbacv1.Role
	if err := fakeCli.Get(ctx, client.ObjectKey{Name: keyReaderRoleName, Namespace: ns}, &keyRole); err != nil {
		t.Fatalf("expected the key-reader Role to survive the remote-auth Secret change: %v", err)
	}
	if len(keyRole.Rules) != 1 || len(keyRole.Rules[0].ResourceNames) != 2 ||
		keyRole.Rules[0].ResourceNames[0] != operatorKeySecretName || keyRole.Rules[0].ResourceNames[1] != sidecarKeySecretName {
		t.Errorf("key-reader Role must still reference both verification key Secrets, got %+v", keyRole.Rules)
	}
	var keyRB rbacv1.RoleBinding
	if err := fakeCli.Get(ctx, client.ObjectKey{Name: keyReaderRoleName, Namespace: ns}, &keyRB); err != nil {
		t.Fatalf("expected the key-reader RoleBinding to survive the remote-auth Secret change: %v", err)
	}
	if keyRB.RoleRef.Name != keyReaderRoleName {
		t.Errorf("key-reader RoleBinding RoleRef mismatch after Secret change: %q", keyRB.RoleRef.Name)
	}
}

// TestDeploymentStorageSizeForcesRollout pins the SPEC R6 requirement that a storage.size
// change redeploys the Cartographer (so the readiness → re-apply-schema sequence runs on
// the new pod) rather than only patching the PVC in place. The desired storage size is
// encoded in the pod template, so a size increase changes the template and thus the
// Deployment rollout hash, while an unchanged size leaves the template stable.
func TestDeploymentStorageSizeForcesRollout(t *testing.T) {
	s := scheme.Scheme
	_ = flowv1.AddToScheme(s)
	_ = appsv1.AddToScheme(s)
	_ = corev1.AddToScheme(s)

	ns := testNS
	small := resource.MustParse("1Gi")
	large := resource.MustParse("4Gi")
	fg := &flowv1.FoundryGraph{
		ObjectMeta: metav1.ObjectMeta{Name: defaultGraphName, Namespace: ns},
		Spec:       flowv1.FoundryGraphSpec{Storage: &flowv1.StorageSpec{Size: &small}},
	}
	fakeCli := fake.NewClientBuilder().WithScheme(s).WithObjects(fg).Build()
	r := &FoundryGraphReconciler{
		Client:            fakeCli,
		Scheme:            s,
		CartographerPort:  50051,
		CartographerImage: "flow-cartographer:latest",
	}

	ctx := context.Background()
	if err := r.reconcileDeployment(ctx, fg); err != nil {
		t.Fatalf("reconcileDeployment (first): %v", err)
	}
	var deploy appsv1.Deployment
	if err := fakeCli.Get(ctx, client.ObjectKey{Name: cartographerSvcName, Namespace: ns}, &deploy); err != nil {
		t.Fatalf("get Deployment: %v", err)
	}
	first := deploy.Spec.Template.Annotations[cartographerStorageSizeAnnotation]
	if first == "" {
		t.Fatal("expected the storage-size pod-template annotation to be set")
	}

	// Increase spec.storage.size → the pod template's storage annotation must change,
	// which is what rolls a new Deployment pod (SPEC R6 redeploy on non-schema change).
	fg.Spec.Storage.Size = &large
	if err := r.reconcileDeployment(ctx, fg); err != nil {
		t.Fatalf("reconcileDeployment (second): %v", err)
	}
	if err := fakeCli.Get(ctx, client.ObjectKey{Namespace: ns, Name: cartographerSvcName}, &deploy); err != nil {
		t.Fatalf("get Deployment after resize: %v", err)
	}
	second := deploy.Spec.Template.Annotations[cartographerStorageSizeAnnotation]
	if second == first {
		t.Errorf("expected the storage template annotation to change after storage.size increase, got %q", second)
	}

	// A subsequent reconcile with the same size must leave the template (and rollout hash)
	// unchanged — i.e. the annotation is stable when storage.size is stable.
	if err := r.reconcileDeployment(ctx, fg); err != nil {
		t.Fatalf("reconcileDeployment (third, unchanged): %v", err)
	}
	if err := fakeCli.Get(ctx, client.ObjectKey{Namespace: ns, Name: cartographerSvcName}, &deploy); err != nil {
		t.Fatalf("get Deployment after stable reconcile: %v", err)
	}
	if got := deploy.Spec.Template.Annotations[cartographerStorageSizeAnnotation]; got != second {
		t.Errorf("expected stable storage template annotation across unchanged reconciles, got %q, want %q", got, second)
	}
}

// TestApplySchemaReFetchNotFoundSurfacesError pins the item's SPEC-R6 fix for the
// applySchema re-fetch: an IsNotFound on the re-fetch must NOT be swallowed. A
// concurrently-deleted CR must not flow through as a zero-valued object whose empty schema
// gets dialed and applied against a live Cartographer. The re-fetch error must surface so
// the schema-apply aborts; the next reconcile's initial Get drops the now-absent request.
func TestApplySchemaReFetchNotFoundSurfacesError(t *testing.T) {
	s := scheme.Scheme
	_ = flowv1.AddToScheme(s)
	_ = corev1.AddToScheme(s)

	// No FoundryGraph object is seeded, so the re-fetch inside applySchema returns NotFound.
	fakeCli := fake.NewClientBuilder().WithScheme(s).Build()
	dialed := false
	r := &FoundryGraphReconciler{
		Client: fakeCli,
		Scheme: s,
		CartographerDialer: func(ctx context.Context, endpoint string) (CartographerClient, error) {
			dialed = true
			return &mockCartographerClient{}, nil
		},
	}

	// The in-memory zero-valued object stands in for the stale object the reconciler holds.
	fg := &flowv1.FoundryGraph{ObjectMeta: metav1.ObjectMeta{Name: defaultGraphName, Namespace: testNS}}
	err := r.applySchema(context.Background(), fg, &fg.Spec)
	if err == nil {
		t.Fatal("expected applySchema to surface the NotFound re-fetch, got nil")
	}
	if dialed {
		t.Fatal("applySchema must not dial a Cartographer when the re-fetch CR is absent")
	}
}
