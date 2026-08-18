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

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	flowv1 "github.com/foundry/flow/operator/api/v1"
)

// TestUpdateStatusPopulatesStorageSize verifies updateStatus publishes the PVC's actual
// capacity as status.storageSize (SPEC R6 step 7: "status.storageSize" reflects the PVC's
// status.capacity.storage). The reconciler's earlier happy-path tests never provision a PVC
// with status.capacity, so the storageSize write path (controller.go) is otherwise untested.
func TestUpdateStatusPopulatesStorageSize(t *testing.T) {
	s := scheme.Scheme
	_ = flowv1.AddToScheme(s)
	_ = corev1.AddToScheme(s)

	ns := testNS
	cap := resource.MustParse("5Gi")
	// Seed a PVC with a bound capacity so updateStatus's storageSize read sees a real
	// allocation diverging from any spec default.
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "data-flow-graph", Namespace: ns},
		Status: corev1.PersistentVolumeClaimStatus{
			Capacity: corev1.ResourceList{corev1.ResourceStorage: cap},
		},
	}
	fg := &flowv1.FoundryGraph{ObjectMeta: metav1.ObjectMeta{Name: defaultGraphName, Namespace: ns}}
	fakeCli := fake.NewClientBuilder().WithScheme(s).WithObjects(fg, pvc).WithStatusSubresource(fg).Build()
	r := &FoundryGraphReconciler{Client: fakeCli, Scheme: s, CartographerPort: 50051}

	ctx := context.Background()
	if err := r.updateStatus(ctx, fg, &flowv1.FoundryGraphSpec{}); err != nil {
		t.Fatalf("updateStatus: %v", err)
	}

	var got flowv1.FoundryGraph
	if err := fakeCli.Get(ctx, types.NamespacedName{Name: defaultGraphName, Namespace: ns}, &got); err != nil {
		t.Fatalf("get FoundryGraph: %v", err)
	}
	if got.Status.StorageSize == nil {
		t.Fatal("expected status.storageSize to be populated from the PVC's status.capacity.storage")
	}
	if got.Status.StorageSize.Value() != cap.Value() {
		t.Errorf("expected status.storageSize=%v, got %v", cap.Value(), got.Status.StorageSize.Value())
	}
}

// TestReadinessRateLimiterPinsSpecParameters pins the SPEC R6 step-5 backoff parameters
// (exponential backoff with "initial delay ~5s, doubling per attempt, capped at 5m"):
// the first failure waits 5s, each subsequent failure doubles the wait, and the wait is
// capped at 5m. This is the rate limiter the FoundryGraph controller is configured with
// in SetupWithManager, replacing controller-runtime's default (5ms initial, 1000s cap).
func TestReadinessRateLimiterPinsSpecParameters(t *testing.T) {
	limiter := readinessRateLimiter()
	req := reconcile.Request{NamespacedName: types.NamespacedName{Name: defaultGraphName, Namespace: testNS}}

	// Initial delay ~5s.
	if got := limiter.When(req); got != 5*time.Second {
		t.Errorf("expected initial backoff of 5s (SPEC R6 step 5), got %v", got)
	}
	// Doubling per attempt: 10s, then 20s, then 40s.
	for _, want := range []time.Duration{10 * time.Second, 20 * time.Second, 40 * time.Second} {
		if got := limiter.When(req); got != want {
			t.Errorf("expected backoff %v (doubling per attempt), got %v", want, got)
		}
	}
	// Capped at 5m: keep doubling (80s, 160s, 320s → 5m20s) until the cap holds, then
	// assert every further attempt waits exactly 5m — the SPEC R6 step-5 cap, not
	// controller-runtime's 1000s default.
	got := limiter.When(req)
	for got < 5*time.Minute {
		want := 2 * got
		got = limiter.When(req)
		if got > 5*time.Minute {
			t.Errorf("expected backoff capped at 5m (SPEC R6 step 5), got %v", got)
			break
		}
		if want < 5*time.Minute && got != want {
			t.Errorf("expected backoff %v (doubling per attempt), got %v", want, got)
		}
	}
	for range 20 {
		if got := limiter.When(req); got != 5*time.Minute {
			t.Errorf("expected backoff capped at 5m (SPEC R6 step 5), got %v", got)
		}
	}
	// A distinct item must restart from the 5s base delay (per-item limiting).
	other := reconcile.Request{NamespacedName: types.NamespacedName{Name: "other-graph", Namespace: testNS}}
	if got := limiter.When(other); got != 5*time.Second {
		t.Errorf("expected a new item to start at the 5s base delay, got %v", got)
	}
}

// TestUpdateStatusPvcGetErrors distinguishes the two PVC Get outcomes in updateStatus
// (item 1): an IsNotFound (no PVC provisioned yet) leaves storageSize absent while reconcile
// still succeeds; any other Get error (RBAC/apiserver/transient) must surface to the requeue
// path instead of being silently swallowed.
func TestUpdateStatusPvcGetErrors(t *testing.T) {
	s := scheme.Scheme
	_ = flowv1.AddToScheme(s)
	_ = corev1.AddToScheme(s)
	ctx := context.Background()
	ns := testNS

	t.Run("pvc not found leaves storageSize absent", func(t *testing.T) {
		fg := &flowv1.FoundryGraph{ObjectMeta: metav1.ObjectMeta{Name: defaultGraphName, Namespace: ns}}
		fakeCli := fake.NewClientBuilder().WithScheme(s).WithObjects(fg).WithStatusSubresource(fg).Build()
		r := &FoundryGraphReconciler{Client: fakeCli, Scheme: s, CartographerPort: 50051}

		if err := r.updateStatus(ctx, fg, &flowv1.FoundryGraphSpec{}); err != nil {
			t.Fatalf("updateStatus must succeed when the PVC is not found, got: %v", err)
		}
		var got flowv1.FoundryGraph
		if err := fakeCli.Get(ctx, types.NamespacedName{Name: defaultGraphName, Namespace: ns}, &got); err != nil {
			t.Fatalf("get FoundryGraph: %v", err)
		}
		if got.Status.StorageSize != nil {
			t.Error("expected status.storageSize to remain absent when the PVC is not found")
		}
	})

	t.Run("pvc get error surfaces to the caller", func(t *testing.T) {
		fg := &flowv1.FoundryGraph{ObjectMeta: metav1.ObjectMeta{Name: defaultGraphName, Namespace: ns}}
		interceptorFuncs := interceptor.Funcs{
			Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
				if _, ok := obj.(*corev1.PersistentVolumeClaim); ok {
					return errors.New("apiserver unavailable")
				}
				return nil
			},
		}
		fakeCli := fake.NewClientBuilder().WithScheme(s).WithObjects(fg).WithStatusSubresource(fg).WithInterceptorFuncs(interceptorFuncs).Build()
		r := &FoundryGraphReconciler{Client: fakeCli, Scheme: s, CartographerPort: 50051}

		if err := r.updateStatus(ctx, fg, &flowv1.FoundryGraphSpec{}); err == nil {
			t.Fatal("expected updateStatus to surface the PVC Get error, not swallow it")
		} else if !strings.Contains(err.Error(), "read pvc") {
			t.Errorf("expected the PVC read error to be surfaced with context, got: %v", err)
		}
	})
}

// TestUpdateStatusUpdateAndStatusErrors covers the two remaining updateStatus failure
// branches (reconcile step 7): the main r.Update error (the last-applied-spec annotation
// write) and the r.Status().Update error (the endpoint/storageSize status-block write).
// The sibling Get-error branches are pinned by TestUpdateStatusPvcGetErrors.
func TestUpdateStatusUpdateAndStatusErrors(t *testing.T) {
	s := scheme.Scheme
	_ = flowv1.AddToScheme(s)
	_ = corev1.AddToScheme(s)
	ctx := context.Background()
	ns := testNS

	t.Run("main update error surfaces to the caller", func(t *testing.T) {
		fg := &flowv1.FoundryGraph{ObjectMeta: metav1.ObjectMeta{Name: defaultGraphName, Namespace: ns}}
		interceptorFuncs := interceptor.Funcs{
			Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
				if _, ok := obj.(*flowv1.FoundryGraph); ok {
					return errors.New("apiserver unavailable")
				}
				return nil
			},
		}
		fakeCli := fake.NewClientBuilder().WithScheme(s).WithObjects(fg).WithStatusSubresource(fg).WithInterceptorFuncs(interceptorFuncs).Build()
		r := &FoundryGraphReconciler{Client: fakeCli, Scheme: s, CartographerPort: 50051}

		if err := r.updateStatus(ctx, fg, &flowv1.FoundryGraphSpec{}); err == nil {
			t.Fatal("expected updateStatus to surface the main Update (annotation write) error")
		} else if !strings.Contains(err.Error(), "update FoundryGraph") {
			t.Errorf("expected the main Update error to be surfaced with context, got: %v", err)
		}
	})

	t.Run("status update error surfaces to the caller", func(t *testing.T) {
		fg := &flowv1.FoundryGraph{ObjectMeta: metav1.ObjectMeta{Name: defaultGraphName, Namespace: ns}}
		interceptorFuncs := interceptor.Funcs{
			SubResourceUpdate: func(ctx context.Context, c client.Client, subResourceName string, obj client.Object, opts ...client.SubResourceUpdateOption) error {
				return errors.New("apiserver unavailable")
			},
		}
		fakeCli := fake.NewClientBuilder().WithScheme(s).WithObjects(fg).WithStatusSubresource(fg).WithInterceptorFuncs(interceptorFuncs).Build()
		r := &FoundryGraphReconciler{Client: fakeCli, Scheme: s, CartographerPort: 50051}

		if err := r.updateStatus(ctx, fg, &flowv1.FoundryGraphSpec{}); err == nil {
			t.Fatal("expected updateStatus to surface the Status().Update (status block) error")
		} else if !strings.Contains(err.Error(), "update FoundryGraph status") {
			t.Errorf("expected the Status().Update error to be surfaced with context, got: %v", err)
		}
	})
}
