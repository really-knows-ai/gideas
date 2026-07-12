package controller

import (
	"context"
	"fmt"
	"testing"

	"github.com/go-logr/logr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	flowv1 "github.com/foundry/flow/operator/api/v1"
)

func TestLawReconciler_SyncsLawToLibrarian(t *testing.T) {
	t.Parallel()

	s := newScheme(t)
	mock := &mockLibrarianClient{}

	ga := &flowv1.GovernedArtefact{
		ObjectMeta: metav1.ObjectMeta{Name: "haiku", Namespace: "default"},
	}
	law := &flowv1.Law{
		ObjectMeta: metav1.ObjectMeta{Name: "no-weather", Namespace: "default"},
		Spec: flowv1.LawSpec{
			Goal:      "No weather",
			Tier:      3,
			AppliesTo: []string{"haiku"},
			Group:     "content",
			Representations: []flowv1.Representation{{
				Type:    "text/markdown",
				Content: "No rain.",
			}},
		},
	}

	client := fake.NewClientBuilder().
		WithScheme(s).
		WithStatusSubresource(&flowv1.Law{}).
		WithObjects(ga, law).
		Build()

	r := &LawReconciler{Client: client, Scheme: s, Librarian: mock}
	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "no-weather", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}

	if len(mock.SyncedLaws) != 1 || mock.SyncedLaws[0] != "no-weather" {
		t.Fatalf("expected ReplicateLaws called with no-weather, got %v", mock.SyncedLaws)
	}
}

func TestLawReconciler_NilLibrarianRequeues(t *testing.T) {
	t.Parallel()

	s := newScheme(t)
	law := &flowv1.Law{
		ObjectMeta: metav1.ObjectMeta{Name: "no-weather", Namespace: "default"},
		Spec: flowv1.LawSpec{
			Goal: "No weather",
			Tier: 3,
			Representations: []flowv1.Representation{{
				Type:    "text/markdown",
				Content: "No rain.",
			}},
		},
	}

	client := fake.NewClientBuilder().
		WithScheme(s).
		WithStatusSubresource(&flowv1.Law{}).
		WithObjects(law).
		Build()

	ctrl.SetLogger(logr.Discard())
	t.Cleanup(func() { ctrl.SetLogger(logr.Discard()) })

	r := &LawReconciler{Client: client, Scheme: s}
	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "no-weather", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}
	if result.RequeueAfter == 0 {
		t.Fatal("expected requeue when Librarian is nil")
	}
}

func TestLawReconciler_LibrarianErrorRequeues(t *testing.T) {
	t.Parallel()

	s := newScheme(t)
	mock := &mockLibrarianClient{SyncLawError: fmt.Errorf("librarian unavailable")}
	law := &flowv1.Law{
		ObjectMeta: metav1.ObjectMeta{Name: "no-weather", Namespace: "default"},
		Spec: flowv1.LawSpec{
			Goal: "No weather",
			Tier: 3,
			Representations: []flowv1.Representation{{
				Type:    "text/markdown",
				Content: "No rain.",
			}},
		},
	}

	client := fake.NewClientBuilder().
		WithScheme(s).
		WithStatusSubresource(&flowv1.Law{}).
		WithObjects(law).
		Build()

	ctrl.SetLogger(logr.Discard())
	t.Cleanup(func() { ctrl.SetLogger(logr.Discard()) })

	r := &LawReconciler{Client: client, Scheme: s, Librarian: mock}
	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "no-weather", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}
	if result.RequeueAfter == 0 {
		t.Fatal("expected requeue on Librarian error")
	}
}
