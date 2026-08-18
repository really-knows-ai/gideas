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
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	ctrl "sigs.k8s.io/controller-runtime"

	flowv1 "github.com/foundry/flow/operator/api/v1"
)

func TestFoundryGraphReconciler_CartographerServiceName(t *testing.T) {
	r := &FoundryGraphReconciler{}
	fg := &flowv1.FoundryGraph{}
	fg.Name = defaultGraphName
	fg.Namespace = testNS

	name := r.cartographerServiceName(fg)
	if name != cartographerSvcName {
		t.Errorf("expected cartographer-flow-graph, got %q", name)
	}
}

func TestFoundryGraphReconciler_RegisterProxyRoute(t *testing.T) {
	rt := NewProxyRoutingTable()
	r := &FoundryGraphReconciler{
		CartographerPort:  50051,
		ProxyRoutingTable: rt,
	}

	fg := &flowv1.FoundryGraph{}
	fg.Name = defaultGraphName
	fg.Namespace = testNS

	r.registerProxyRoute(fg)

	endpoint, ok := rt.Lookup(testNS, defaultGraphName)
	if !ok {
		t.Fatal("expected route to be registered")
	}
	expected := "cartographer-flow-graph.test-ns.svc.cluster.local:50051"
	if endpoint != expected {
		t.Errorf("expected %q, got %q", expected, endpoint)
	}
}

func TestFoundryGraphReconciler_TearDown(t *testing.T) {
	s := scheme.Scheme
	_ = flowv1.AddToScheme(s)
	_ = appsv1.AddToScheme(s)
	_ = corev1.AddToScheme(s)
	_ = rbacv1.AddToScheme(s)

	ns := testNS
	fg := &flowv1.FoundryGraph{
		ObjectMeta: metav1.ObjectMeta{
			Name:      defaultGraphName,
			Namespace: ns,
		},
	}

	// Seed every resource class tearDown deletes (SPEC R6 deletion flow: Deployment,
	// Service, ServiceAccount, both Roles, both RoleBindings, and the PVC) so each
	// deletion branch executes its real delete path rather than only the IsNotFound path.
	deploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      cartographerSvcName,
			Namespace: ns,
		},
	}
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      cartographerSvcName,
			Namespace: ns,
		},
	}
	sa := &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      cartographerSvcName,
			Namespace: ns,
		},
	}
	keyRole := &rbacv1.Role{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "cartographer-flow-graph-key-reader",
			Namespace: ns,
		},
	}
	keyRB := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "cartographer-flow-graph-key-reader",
			Namespace: ns,
		},
	}
	remoteRole := &rbacv1.Role{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "cartographer-flow-graph-remote-auth",
			Namespace: ns,
		},
	}
	remoteRB := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "cartographer-flow-graph-remote-auth",
			Namespace: ns,
		},
	}
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "data-flow-graph",
			Namespace: ns,
		},
	}

	fakeCli := fake.NewClientBuilder().WithScheme(s).WithObjects(deploy, svc, sa, keyRole, keyRB, remoteRole, remoteRB, pvc).Build()
	rt := NewProxyRoutingTable()
	r := &FoundryGraphReconciler{
		Client:            fakeCli,
		Scheme:            s,
		ProxyRoutingTable: rt,
	}
	r.registerProxyRoute(fg)

	ctx := context.Background()
	if err := r.tearDown(ctx, fg); err != nil {
		t.Fatalf("tearDown returned error: %v", err)
	}

	// Every seeded resource class must be deleted.
	checks := []struct {
		name string
		obj  client.Object
	}{
		{"Deployment", deploy},
		{"Service", svc},
		{"ServiceAccount", sa},
		{"key-reader Role", keyRole},
		{"key-reader RoleBinding", keyRB},
		{"remote-auth Role", remoteRole},
		{"remote-auth RoleBinding", remoteRB},
		{"PersistentVolumeClaim", pvc},
	}
	for _, c := range checks {
		key := client.ObjectKeyFromObject(c.obj)
		obj := c.obj.DeepCopyObject().(client.Object)
		if err := fakeCli.Get(ctx, key, obj); err == nil {
			t.Errorf("expected %s to be deleted", c.name)
		} else if !apierrors.IsNotFound(err) {
			t.Errorf("get %s after tearDown: %v", c.name, err)
		}
	}

	if _, ok := rt.Lookup(testNS, defaultGraphName); ok {
		t.Fatal("expected route to be deregistered")
	}
}

// TestReconcileDeletionFinalizerRemoval drives the deletion reconcile branch: a
// FoundryGraph carrying the finalizer and a DeletionTimestamp is torn down, its finalizer
// is removed, and the object is updated — no error is returned.
func TestReconcileDeletionFinalizesRemoval(t *testing.T) {
	s := scheme.Scheme
	_ = flowv1.AddToScheme(s)
	_ = appsv1.AddToScheme(s)
	_ = corev1.AddToScheme(s)
	_ = rbacv1.AddToScheme(s)

	now := metav1.Now()
	fg := &flowv1.FoundryGraph{
		ObjectMeta: metav1.ObjectMeta{
			Name:              defaultGraphName,
			Namespace:         testNS,
			DeletionTimestamp: &now,
			Finalizers:        []string{finalizerName},
		},
	}
	// A deployment exists so the tear-down actually removes something.
	deploy := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: cartographerSvcName, Namespace: testNS}}

	fakeCli := fake.NewClientBuilder().WithScheme(s).WithObjects(fg, deploy).WithStatusSubresource(fg).Build()
	r := &FoundryGraphReconciler{
		Client:            fakeCli,
		Scheme:            s,
		ProxyRoutingTable: NewProxyRoutingTable(),
	}

	ctx := context.Background()
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: defaultGraphName, Namespace: testNS}}); err != nil {
		t.Fatalf("Reconcile on deletion returned error: %v", err)
	}

	// Tear-down must have deleted the Deployment.
	var d appsv1.Deployment
	if err := fakeCli.Get(ctx, types.NamespacedName{Name: cartographerSvcName, Namespace: testNS}, &d); err == nil {
		t.Error("expected Deployment to be deleted during tear-down")
	}
}
