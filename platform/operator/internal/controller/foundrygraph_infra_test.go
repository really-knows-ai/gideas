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
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

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
