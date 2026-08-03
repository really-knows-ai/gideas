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
	"io"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	flowv1gen "github.com/foundry/flow/gen/flow/v1"
	flowv1 "github.com/foundry/flow/operator/api/v1"
)

func TestFoundryGraphReconciler_CartographerServiceName(t *testing.T) {
	r := &FoundryGraphReconciler{}
	fg := &flowv1.FoundryGraph{}
	fg.Name = "flow-graph"
	fg.Namespace = "test-ns"

	name := r.cartographerServiceName(fg)
	if name != "cartographer-flow-graph" {
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
	fg.Name = "flow-graph"
	fg.Namespace = "test-ns"

	r.registerProxyRoute(fg)

	endpoint, ok := rt.Lookup("test-ns", "flow-graph")
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

	ns := "test-ns"
	fg := &flowv1.FoundryGraph{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "flow-graph",
			Namespace: ns,
		},
	}

	deploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "cartographer-flow-graph",
			Namespace: ns,
		},
	}
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "cartographer-flow-graph",
			Namespace: ns,
		},
	}

	fakeCli := fake.NewClientBuilder().WithScheme(s).WithObjects(deploy, svc).Build()
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

	var d appsv1.Deployment
	if err := fakeCli.Get(ctx, types.NamespacedName{Name: "cartographer-flow-graph", Namespace: ns}, &d); err == nil {
		t.Fatal("expected Deployment to be deleted")
	}

	var srv corev1.Service
	if err := fakeCli.Get(ctx, types.NamespacedName{Name: "cartographer-flow-graph", Namespace: ns}, &srv); err == nil {
		t.Fatal("expected Service to be deleted")
	}

	if _, ok := rt.Lookup("test-ns", "flow-graph"); ok {
		t.Fatal("expected route to be deregistered")
	}
}

func TestIsFailedPrecondition(t *testing.T) {
	result := isFailedPrecondition(nil)
	if result {
		t.Error("expected false for nil error")
	}
}

// mockCartographerClient implements CartographerClient for testing.
type mockCartographerClient struct {
	applySchemaFn func(context.Context, *flowv1gen.ApplySchemaRequest) (*flowv1gen.ApplySchemaResponse, error)
	wipeGraphFn   func(context.Context, *flowv1gen.WipeGraphRequest) (*flowv1gen.WipeGraphResponse, error)
	healthCheckFn func(context.Context, *flowv1gen.HealthCheckRequest) (*flowv1gen.HealthCheckResponse, error)
	exportGraphFn func(context.Context, *flowv1gen.ExportGraphRequest) (flowv1gen.CartographerService_ExportGraphClient, error)
	closeFn       func() error
}

func (m *mockCartographerClient) ApplySchema(ctx context.Context, in *flowv1gen.ApplySchemaRequest, opts ...grpc.CallOption) (*flowv1gen.ApplySchemaResponse, error) {
	if m.applySchemaFn != nil {
		return m.applySchemaFn(ctx, in)
	}
	return &flowv1gen.ApplySchemaResponse{}, nil
}

func (m *mockCartographerClient) WipeGraph(ctx context.Context, in *flowv1gen.WipeGraphRequest, opts ...grpc.CallOption) (*flowv1gen.WipeGraphResponse, error) {
	if m.wipeGraphFn != nil {
		return m.wipeGraphFn(ctx, in)
	}
	return &flowv1gen.WipeGraphResponse{}, nil
}

func (m *mockCartographerClient) HealthCheck(ctx context.Context, in *flowv1gen.HealthCheckRequest, opts ...grpc.CallOption) (*flowv1gen.HealthCheckResponse, error) {
	if m.healthCheckFn != nil {
		return m.healthCheckFn(ctx, in)
	}
	return &flowv1gen.HealthCheckResponse{}, nil
}

func (m *mockCartographerClient) ExportGraph(ctx context.Context, in *flowv1gen.ExportGraphRequest, opts ...grpc.CallOption) (flowv1gen.CartographerService_ExportGraphClient, error) {
	if m.exportGraphFn != nil {
		return m.exportGraphFn(ctx, in)
	}
	return &mockExportGraphClient{}, nil
}

func (m *mockCartographerClient) Close() error {
	if m.closeFn != nil {
		return m.closeFn()
	}
	return nil
}

type mockExportGraphClient struct{}

func (mockExportGraphClient) Recv() (*flowv1gen.ExportGraphResponse, error) { return nil, io.EOF }
func (mockExportGraphClient) Context() context.Context                      { return context.Background() }
func (mockExportGraphClient) Header() (metadata.MD, error)                  { return nil, nil }
func (mockExportGraphClient) Trailer() metadata.MD                          { return nil }
func (mockExportGraphClient) CloseSend() error                              { return nil }
func (mockExportGraphClient) SendMsg(any) error                             { return nil }
func (mockExportGraphClient) RecvMsg(any) error                             { return nil }
