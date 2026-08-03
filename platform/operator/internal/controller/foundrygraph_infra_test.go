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
