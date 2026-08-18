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
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

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

// TestDeploymentEnvVarsValueFromShapes pins the SPEC R6 step 3 env-var SHAPES for the three
// reference-based env vars (foundrygraph_infra.go:305-332): OPERATOR_VERIFICATION_KEY and
// SIDECAR_VERIFICATION_KEY must be rendered as valueFrom.secretKeyRef references to the
// per-namespace verification-key Secrets using the data key "key" (never as literal values),
// and POD_NAMESPACE must be rendered as valueFrom.fieldRef with fieldPath metadata.namespace
// (SPEC R6 step 3: "OPERATOR_VERIFICATION_KEY and SIDECAR_VERIFICATION_KEY set from the
// corresponding Secrets via valueFrom.secretKeyRef using the data key key" and "POD_NAMESPACE
// from the Downward API (fieldRef: metadata.namespace)"). A regression that flattens any of
// the three references into a literal Value must fail the test.
func TestDeploymentEnvVarsValueFromShapes(t *testing.T) {
	r := &FoundryGraphReconciler{CartographerPort: 50051, CapabilityStalenessWindow: "30s"}
	fg := &flowv1.FoundryGraph{ObjectMeta: metav1.ObjectMeta{Name: defaultGraphName}}

	env := r.deploymentEnvVars(fg)
	envMap := make(map[string]corev1.EnvVar, len(env))
	for _, e := range env {
		envMap[e.Name] = e
	}

	for _, tc := range []struct {
		name       string
		secretName string // expected secretKeyRef.name; empty for the fieldRef var
		secretKey  string // expected secretKeyRef.key
		fieldPath  string // expected fieldRef.fieldPath; empty for the secretKeyRef vars
	}{
		{name: "OPERATOR_VERIFICATION_KEY", secretName: operatorKeySecretName, secretKey: "key"},
		{name: "SIDECAR_VERIFICATION_KEY", secretName: sidecarKeySecretName, secretKey: "key"},
		{name: "POD_NAMESPACE", fieldPath: "metadata.namespace"},
	} {
		e, ok := envMap[tc.name]
		if !ok {
			t.Fatalf("expected env var %q to be rendered", tc.name)
		}
		if e.Value != "" {
			t.Errorf("%q must not carry a literal Value (SPEC R6 step 3 requires a reference), got %q", tc.name, e.Value)
		}
		if e.ValueFrom == nil {
			t.Fatalf("expected %q to set ValueFrom, got %+v", tc.name, e)
		}
		switch {
		case tc.secretName != "":
			if e.ValueFrom.SecretKeyRef == nil {
				t.Fatalf("expected %q to be a secretKeyRef (SPEC R6 step 3), got %+v", tc.name, e.ValueFrom)
			}
			if e.ValueFrom.SecretKeyRef.Name != tc.secretName {
				t.Errorf("expected %q secretKeyRef.name=%q, got %q", tc.name, tc.secretName, e.ValueFrom.SecretKeyRef.Name)
			}
			if e.ValueFrom.SecretKeyRef.Key != tc.secretKey {
				t.Errorf("expected %q secretKeyRef.key=%q, got %q", tc.name, tc.secretKey, e.ValueFrom.SecretKeyRef.Key)
			}
		case tc.fieldPath != "":
			if e.ValueFrom.FieldRef == nil {
				t.Fatalf("expected %q to be a fieldRef (SPEC R6 step 3), got %+v", tc.name, e.ValueFrom)
			}
			if e.ValueFrom.FieldRef.FieldPath != tc.fieldPath {
				t.Errorf("expected %q fieldRef.fieldPath=%q, got %q", tc.name, tc.fieldPath, e.ValueFrom.FieldRef.FieldPath)
			}
		}
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
		if e, ok := envMap["TRANSACTION_TIMEOUT"]; !ok || e.Value != DefaultTransactionTimeout {
			t.Errorf("expected TRANSACTION_TIMEOUT to fall back to the SPEC R5 default 30m, got %+v (present=%v)", e, ok)
		}
	})
}
