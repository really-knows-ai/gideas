package controller

import (
	"context"
	"testing"

	"github.com/go-logr/logr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	flowv1 "github.com/gideas/flow/operator/api/v1"
)

func TestLawGroupReconciler_StubDoesNotCrash(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	if err := flowv1.AddToScheme(scheme); err != nil {
		t.Fatalf("add flowv1 scheme: %v", err)
	}

	lg := &flowv1.LawGroup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-group",
			Namespace: "default",
		},
		Spec: flowv1.LawGroupSpec{
			Mode:   "bundle",
			Passes: 1,
		},
	}

	client := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&flowv1.LawGroup{}).
		WithObjects(lg).
		Build()

	r := &LawGroupReconciler{
		Client: client,
		Scheme: scheme,
	}

	req := ctrl.Request{
		NamespacedName: types.NamespacedName{
			Name:      "test-group",
			Namespace: "default",
		},
	}

	result, err := r.Reconcile(context.Background(), req)
	if err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}
	if result.RequeueAfter != 0 || result.Requeue {
		t.Fatalf("expected empty result, got %+v", result)
	}
}

func TestLawGroupReconciler_MissingObject(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	if err := flowv1.AddToScheme(scheme); err != nil {
		t.Fatalf("add flowv1 scheme: %v", err)
	}

	client := fake.NewClientBuilder().
		WithScheme(scheme).
		Build()

	// Suppress expected log output during test.
	ctrl.SetLogger(logr.Discard())
	t.Cleanup(func() { ctrl.SetLogger(logr.Discard()) })

	r := &LawGroupReconciler{
		Client: client,
		Scheme: scheme,
	}

	req := ctrl.Request{
		NamespacedName: types.NamespacedName{
			Name:      "nonexistent",
			Namespace: "default",
		},
	}

	result, err := r.Reconcile(context.Background(), req)
	if err != nil {
		t.Fatalf("Reconcile returned error for missing object: %v", err)
	}
	if result.RequeueAfter != 0 || result.Requeue {
		t.Fatalf("expected empty result for missing object, got %+v", result)
	}
}
