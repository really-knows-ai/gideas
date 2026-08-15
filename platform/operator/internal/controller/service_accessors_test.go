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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	flowv1 "github.com/foundry/flow/operator/api/v1"
)

// Compile-time pins: both service CRDs must keep satisfying the reconciler's
// ServiceObject and statusUpdater interfaces, and CodificationService must keep
// satisfying ServiceWithOutput, after the shared-base accessor collapse.
var (
	_ ServiceObject     = &flowv1.CodificationService{}
	_ ServiceObject     = &flowv1.FlowSupportService{}
	_ statusUpdater     = &flowv1.CodificationService{}
	_ statusUpdater     = &flowv1.FlowSupportService{}
	_ ServiceWithOutput = &flowv1.CodificationService{}
)

func TestServiceAccessorsThroughInterfaces(t *testing.T) {
	resources := &corev1.ResourceRequirements{
		Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("100m")},
	}
	storage := &flowv1.StorageConfig{
		Volumes: []flowv1.VolumeMount{{Name: "data", MountPath: "/data"}},
	}
	conds := []metav1.Condition{
		{Type: "Ready", Status: metav1.ConditionTrue, Reason: "Reconciled"},
	}

	svcs := []ServiceObject{
		&flowv1.CodificationService{Spec: flowv1.CodificationServiceSpec{
			ServiceSpecBase: flowv1.ServiceSpecBase{
				Image:              "codify:latest",
				MinReplicas:        int32Ptr(2),
				DeploymentStrategy: "StatefulSet",
				Storage:            storage,
				Resources:          resources,
			},
			OutputFormat: "application/rego",
		}},
		&flowv1.FlowSupportService{Spec: flowv1.FlowSupportServiceSpec{
			ServiceSpecBase: flowv1.ServiceSpecBase{
				Image:              "support:latest",
				MinReplicas:        int32Ptr(1),
				DeploymentStrategy: "ReplicaSet",
				Storage:            storage,
				Resources:          resources,
			},
			ProvidesCapabilities: []string{"encode"},
		}},
	}

	for _, svc := range svcs {
		if got := svc.GetSpecImage(); got == "" {
			t.Errorf("%T.GetSpecImage() returned empty image", svc)
		}
		if got := svc.GetSpecMinReplicas(); got == nil || *got < 1 {
			t.Errorf("%T.GetSpecMinReplicas() = %v, want >= 1", svc, got)
		}
		if got := svc.GetSpecDeploymentStrategy(); got == "" {
			t.Errorf("%T.GetSpecDeploymentStrategy() returned empty", svc)
		}
		if svc.GetSpecResources() == nil {
			t.Errorf("%T.GetSpecResources() returned nil", svc)
		}
		if svc.GetSpecStorage() == nil {
			t.Errorf("%T.GetSpecStorage() returned nil", svc)
		}

		updater, ok := svc.(statusUpdater)
		if !ok {
			t.Fatalf("%T does not implement statusUpdater", svc)
		}
		if got := updater.GetPhase(); got != "" {
			t.Errorf("%T.GetPhase() = %q, want empty before SetPhase", svc, got)
		}
		updater.SetPhase("Ready")
		if got := updater.GetPhase(); got != "Ready" {
			t.Errorf("%T.GetPhase() after SetPhase = %q, want Ready", svc, got)
		}
		updater.SetAvailableReplicas(2)
		updater.SetConditions(conds)
		got := updater.GetConditions()
		if len(got) != 1 || got[0].Type != "Ready" {
			t.Errorf("%T.GetConditions() = %v, want Ready condition", svc, got)
		}
	}
}

func TestCodificationServiceOutputFormatAccessor(t *testing.T) {
	svc := &flowv1.CodificationService{
		Spec: flowv1.CodificationServiceSpec{OutputFormat: "application/rego"},
	}
	var withOutput ServiceWithOutput = svc
	if got := withOutput.GetSpecOutputFormat(); got != "application/rego" {
		t.Errorf("GetSpecOutputFormat() = %q, want application/rego", got)
	}
}

//go:fix inline
func int32Ptr(v int32) *int32 { return new(v) }
