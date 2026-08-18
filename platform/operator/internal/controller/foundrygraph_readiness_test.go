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
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	flowv1 "github.com/foundry/flow/operator/api/v1"
)

// TestWaitForReadinessTimeout exercises the SPEC R6 readiness-timeout boundary: a
// Deployment that never becomes ready must make waitForReadiness return the timeout error
// (the ctx.Done / time.After loop exits via the deadline). Without ready replicas, the poll
// runs until the ReadinessTimeout is crossed; we assert the error names the timeout, not a
// context cancel.
func TestWaitForReadinessTimeout(t *testing.T) {
	s := scheme.Scheme
	_ = appsv1.AddToScheme(s)
	_ = flowv1.AddToScheme(s)

	// A Deployment whose desired replicas are never ready → the poll cannot succeed.
	replicas := int32(1)
	notReady := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: cartographerSvcName, Namespace: testNS},
		Spec:       appsv1.DeploymentSpec{Replicas: &replicas},
		Status:     appsv1.DeploymentStatus{ReadyReplicas: 0},
	}
	fakeCli := fake.NewClientBuilder().WithScheme(s).WithObjects(notReady).Build()
	r := &FoundryGraphReconciler{Client: fakeCli, ReadinessTimeout: 100 * time.Millisecond}

	fg := &flowv1.FoundryGraph{ObjectMeta: metav1.ObjectMeta{Name: defaultGraphName, Namespace: testNS}}
	err := r.waitForReadiness(context.Background(), fg)
	if err == nil {
		t.Fatal("expected a readiness timeout error when the pod never becomes ready")
	}
	if apierrors.IsConflict(err) {
		t.Fatalf("expected a timeout error, got a conflict: %v", err)
	}
	if !strings.Contains(err.Error(), "readiness timeout") {
		t.Errorf("expected a readiness-timeout error, got: %v", err)
	}
}

// TestWaitForReadinessCtxCancellation exercises the ctx.Done branch of waitForReadiness: a
// cancellation surfaced mid-poll must return the context error, not the readiness-timeout
// error.
func TestWaitForReadinessCtxCancellation(t *testing.T) {
	s := scheme.Scheme
	_ = appsv1.AddToScheme(s)
	_ = flowv1.AddToScheme(s)

	replicas := int32(1)
	notReady := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: cartographerSvcName, Namespace: testNS},
		Spec:       appsv1.DeploymentSpec{Replicas: &replicas},
		Status:     appsv1.DeploymentStatus{ReadyReplicas: 0},
	}
	fakeCli := fake.NewClientBuilder().WithScheme(s).WithObjects(notReady).Build()
	// A context that is cancelled before the poll's 5s sleep elapses → ctx.Done fires.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r := &FoundryGraphReconciler{Client: fakeCli, ReadinessTimeout: time.Minute}
	fg := &flowv1.FoundryGraph{ObjectMeta: metav1.ObjectMeta{Name: defaultGraphName, Namespace: testNS}}
	err := r.waitForReadiness(ctx, fg)
	if err == nil {
		t.Fatal("expected a context-cancellation error")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled, got %v", err)
	}
}

// TestAllReplicasReady asserts allReplicasReady's guard branches: nil replicas, zero
// replicas, partial readiness, and ready-but-not-updated (the old ReplicaSet still
// serving during a rollout) all report not-ready; equal readiness AND updated-replicas
// reports ready. The UpdatedReplicas requirement is what guarantees the ready pod is the
// NEW pod before the schema re-apply dials the Service (SPEC R6).
func TestAllReplicasReady(t *testing.T) {
	if allReplicasReady(&appsv1.Deployment{}) {
		t.Error("expected nil replicas to be not ready")
	}
	zero := int32(0)
	if allReplicasReady(&appsv1.Deployment{Spec: appsv1.DeploymentSpec{Replicas: &zero}}) {
		t.Error("expected zero replicas to be not ready")
	}
	one := int32(1)
	if allReplicasReady(&appsv1.Deployment{Spec: appsv1.DeploymentSpec{Replicas: &one}, Status: appsv1.DeploymentStatus{ReadyReplicas: 0}}) {
		t.Error("expected fewer ready replicas than desired to be not ready")
	}
	// The old pod is ready but the new pod (matching the updated template) is not yet
	// running: ReadyReplicas alone must NOT satisfy the check.
	if allReplicasReady(&appsv1.Deployment{Spec: appsv1.DeploymentSpec{Replicas: &one}, Status: appsv1.DeploymentStatus{ReadyReplicas: 1, UpdatedReplicas: 0}}) {
		t.Error("expected a ready-but-not-updated replica (old ReplicaSet) to be not ready")
	}
	if !allReplicasReady(&appsv1.Deployment{Spec: appsv1.DeploymentSpec{Replicas: &one}, Status: appsv1.DeploymentStatus{ReadyReplicas: 1, UpdatedReplicas: 1}}) {
		t.Error("expected all-replicas-ready with the current template to be ready")
	}
}

// TestWaitForReadinessRequiresUpdatedReplicas pins the SPEC R6 "new pod passes its
// readiness probe" guarantee (item 3): during a spec-change rollout the old ReplicaSet
// keeps ReadyReplicas>=1 while the new pod is still starting, so a readiness check based
// on ReadyReplicas alone returns immediately and the step-10 ApplySchema dials the
// ClusterIP Service, which may still serve the old pod. waitForReadiness must therefore
// require the Deployment's UpdatedReplicas to cover every desired replica — only then is
// the ready pod the NEW one.
func TestWaitForReadinessRequiresUpdatedReplicas(t *testing.T) {
	s := scheme.Scheme
	_ = appsv1.AddToScheme(s)

	t.Run("old replicaset ready but new pod not updated times out", func(t *testing.T) {
		replicas := int32(1)
		rolling := &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Name: cartographerSvcName, Namespace: testNS},
			Spec:       appsv1.DeploymentSpec{Replicas: &replicas},
			// The old pod is ready (ReadyReplicas=1) but the new pod matching the
			// updated template is not running yet (UpdatedReplicas=0).
			Status: appsv1.DeploymentStatus{ReadyReplicas: 1, UpdatedReplicas: 0},
		}
		fakeCli := fake.NewClientBuilder().WithScheme(s).WithObjects(rolling).Build()
		r := &FoundryGraphReconciler{Client: fakeCli, ReadinessTimeout: 100 * time.Millisecond}
		fg := &flowv1.FoundryGraph{ObjectMeta: metav1.ObjectMeta{Name: defaultGraphName, Namespace: testNS}}
		if err := r.waitForReadiness(context.Background(), fg); err == nil {
			t.Fatal("expected readiness to wait for the new (updated) pod, got immediate success")
		}
	})

	t.Run("succeeds once the new pod is updated and ready", func(t *testing.T) {
		// The Deployment starts in the rolling state (first Get: not ready); the second
		// Get reflects the new pod having passed readiness (UpdatedReplicas=1,
		// ReadyReplicas=1).
		calls := 0
		interceptorFuncs := interceptor.Funcs{
			Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
				calls++
				if calls == 1 {
					return nil // zero-value Deployment: not ready, keep polling
				}
				if d, ok := obj.(*appsv1.Deployment); ok {
					replicas := int32(1)
					d.Spec.Replicas = &replicas
					d.Status = appsv1.DeploymentStatus{ReadyReplicas: 1, UpdatedReplicas: 1}
				}
				return nil
			},
		}
		fakeCli := fake.NewClientBuilder().WithScheme(s).WithInterceptorFuncs(interceptorFuncs).Build()
		r := &FoundryGraphReconciler{Client: fakeCli, ReadinessTimeout: 2 * time.Second}
		fg := &flowv1.FoundryGraph{ObjectMeta: metav1.ObjectMeta{Name: defaultGraphName, Namespace: testNS}}
		if err := r.waitForReadiness(context.Background(), fg); err != nil {
			t.Fatalf("expected readiness once the new pod is updated and ready, got: %v", err)
		}
		if calls < 2 {
			t.Errorf("expected the poll to continue until the new pod is updated, got %d Get calls", calls)
		}
	})
}

// TestWaitForReadinessTransientGetErrorKeepsPolling covers the transient Deployment-Get
// error branch of waitForReadiness (item 8): a momentary Get failure (e.g. the
// Deployment not yet visible after CreateOrUpdate) must not short-circuit the poll — the
// loop keeps polling and succeeds once the Deployment is visible, updated, and ready.
func TestWaitForReadinessTransientGetErrorKeepsPolling(t *testing.T) {
	s := scheme.Scheme
	_ = appsv1.AddToScheme(s)

	calls := 0
	interceptorFuncs := interceptor.Funcs{
		Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			calls++
			if calls == 1 {
				return errors.New("transient: deployment not yet visible")
			}
			if d, ok := obj.(*appsv1.Deployment); ok {
				replicas := int32(1)
				d.Spec.Replicas = &replicas
				d.Status = appsv1.DeploymentStatus{ReadyReplicas: 1, UpdatedReplicas: 1}
			}
			return nil
		},
	}
	fakeCli := fake.NewClientBuilder().WithScheme(s).WithInterceptorFuncs(interceptorFuncs).Build()
	r := &FoundryGraphReconciler{Client: fakeCli, ReadinessTimeout: 2 * time.Second}
	fg := &flowv1.FoundryGraph{ObjectMeta: metav1.ObjectMeta{Name: defaultGraphName, Namespace: testNS}}
	if err := r.waitForReadiness(context.Background(), fg); err != nil {
		t.Fatalf("expected a transient Get error to keep polling until ready, got: %v", err)
	}
	if calls < 2 {
		t.Errorf("expected the poll to continue past the transient Get error, got %d Get calls", calls)
	}
}
