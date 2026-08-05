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
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	flowv1 "github.com/foundry/flow/operator/api/v1"
)

func TestDeploymentEnvVars(t *testing.T) {
	r := &FoundryGraphReconciler{
		CartographerPort:          50051,
		CartographerImage:         "flow-cartographer:latest",
		CapabilityStalenessWindow: "30s",
	}

	fg := &flowv1.FoundryGraph{}
	fg.Name = "flow-graph"
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

func TestLabelsForCartographer(t *testing.T) {
	r := &FoundryGraphReconciler{}
	fg := &flowv1.FoundryGraph{}
	fg.Name = "flow-graph"

	labels := r.labelsForCartographer(fg)
	if labels["app.kubernetes.io/component"] != "cartographer" {
		t.Errorf("expected component=cartographer, got %q", labels["app.kubernetes.io/component"])
	}
	if labels["app.kubernetes.io/instance"] != "flow-graph" {
		t.Errorf("expected instance=flow-graph, got %q", labels["app.kubernetes.io/instance"])
	}
	if labels["app.kubernetes.io/managed-by"] != managedByOperator {
		t.Errorf("expected managed-by=%q, got %q", managedByOperator, labels["app.kubernetes.io/managed-by"])
	}
}

func TestCartographerServiceName(t *testing.T) {
	r := &FoundryGraphReconciler{}
	fg := &flowv1.FoundryGraph{}
	fg.Name = "flow-graph"

	name := r.cartographerServiceName(fg)
	if name != "cartographer-flow-graph" {
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
			Name:      "flow-graph",
			Namespace: "test-ns",
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
	if err := fakeCli.Get(ctx, client.ObjectKey{Name: "cartographer-flow-graph", Namespace: "test-ns"}, &deploy); err != nil {
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

	fg := &flowv1.FoundryGraph{ObjectMeta: metav1.ObjectMeta{Name: "flow-graph", Namespace: "test-ns"}}
	fakeCli := fake.NewClientBuilder().WithScheme(s).WithObjects(fg).Build()
	r := &FoundryGraphReconciler{Client: fakeCli, Scheme: s}

	ctx := context.Background()
	if err := r.reconcilePVC(ctx, fg); err != nil {
		t.Fatalf("reconcilePVC: %v", err)
	}

	var pvc corev1.PersistentVolumeClaim
	if err := fakeCli.Get(ctx, client.ObjectKey{Name: "data-flow-graph", Namespace: "test-ns"}, &pvc); err != nil {
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
		ObjectMeta: metav1.ObjectMeta{Name: "flow-graph", Namespace: "test-ns"},
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
	if err := fakeCli.Get(ctx, client.ObjectKey{Name: "data-flow-graph", Namespace: "test-ns"}, &pvc); err != nil {
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
		ObjectMeta: metav1.ObjectMeta{Name: "flow-graph", Namespace: "test-ns"},
		Spec: flowv1.FoundryGraphSpec{
			Storage: &flowv1.StorageSpec{Size: &large},
		},
	}

	// Pre-seed an existing PVC already allocated at 5Gi.
	existing := corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "data-flow-graph", Namespace: "test-ns"},
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
	if err := fakeCli.Get(ctx, client.ObjectKey{Name: "data-flow-graph", Namespace: "test-ns"}, &pvc); err != nil {
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

	ns := "test-ns"
	fg := &flowv1.FoundryGraph{
		ObjectMeta: metav1.ObjectMeta{Name: "flow-graph", Namespace: ns},
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
}

// TestReconcileRBACCreation verifies the CREATE path (item 8): reconcileRBAC creates the
// ServiceAccount, the verification-key Role/RoleBinding, and, when secretRef is set, the
// remote-auth Role and RoleBinding.
func TestReconcileRBACCreation(t *testing.T) {
	s := scheme.Scheme
	_ = flowv1.AddToScheme(s)
	_ = corev1.AddToScheme(s)
	_ = rbacv1.AddToScheme(s)

	ns := "test-ns"
	fg := &flowv1.FoundryGraph{
		ObjectMeta: metav1.ObjectMeta{Name: "flow-graph", Namespace: ns},
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
	if err := fakeCli.Get(ctx, client.ObjectKey{Name: "cartographer-flow-graph", Namespace: ns}, &sa); err != nil {
		t.Fatalf("expected ServiceAccount to be created: %v", err)
	}

	// Key-reader Role created with get on the verification-key Secrets.
	var keyRole rbacv1.Role
	if err := fakeCli.Get(ctx, client.ObjectKey{Name: "cartographer-flow-graph-key-reader", Namespace: ns}, &keyRole); err != nil {
		t.Fatalf("expected key-reader Role to be created: %v", err)
	}
	if len(keyRole.Rules) != 1 {
		t.Fatalf("expected 1 rule on key-reader Role, got %d", len(keyRole.Rules))
	}
	names := keyRole.Rules[0].ResourceNames
	if len(names) != 2 || names[0] != "cartographer-operator-key" || names[1] != "cartographer-sidecar-key" {
		t.Errorf("key-reader Role must reference the two verification key Secrets, got %v", names)
	}

	// Key-reader RoleBinding bound to the ServiceAccount.
	var keyRB rbacv1.RoleBinding
	if err := fakeCli.Get(ctx, client.ObjectKey{Name: "cartographer-flow-graph-key-reader", Namespace: ns}, &keyRB); err != nil {
		t.Fatalf("expected key-reader RoleBinding to be created: %v", err)
	}
	if keyRB.RoleRef.Name != "cartographer-flow-graph-key-reader" {
		t.Errorf("key-reader RoleBinding RoleRef mismatch: %q", keyRB.RoleRef.Name)
	}
	if len(keyRB.Subjects) != 1 || keyRB.Subjects[0].Name != "cartographer-flow-graph" {
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

	ns := "test-ns"
	fg := &flowv1.FoundryGraph{
		ObjectMeta: metav1.ObjectMeta{Name: "flow-graph", Namespace: ns},
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
}
