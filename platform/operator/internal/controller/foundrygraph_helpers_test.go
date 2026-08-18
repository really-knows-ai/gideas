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

	"google.golang.org/grpc"
	"k8s.io/apimachinery/pkg/api/resource"

	flowv1gen "github.com/foundry/flow/gen/flow/v1"
)

// Shared test constants for the controller package's foundrygraph tests (goconst: the
// repeated literals are hoisted to a single source of truth).
const (
	testNS              = "test-ns"
	cartographerSvcName = "cartographer-flow-graph"
	operatorTestNS      = "operator-system"
	graphTestNS         = "graph-ns"
	rpcApply            = "apply"
	rpcHealth           = "health"
	rpcWipe             = "wipe"
	// keyReaderRoleName is the verification-key Role/RoleBinding name rendered for the
	// conventional flow-graph name ("cartographer-<fg-name>-key-reader").
	keyReaderRoleName = "cartographer-flow-graph-key-reader"
	// remoteAuthRoleName is the remote-auth Role/RoleBinding name rendered for the
	// conventional flow-graph name ("cartographer-<fg-name>-remote-auth").
	remoteAuthRoleName = "cartographer-flow-graph-remote-auth"
)

// resourcePtr returns a pointer to a resource.Quantity parsed from the given string.
func resourcePtr(s string) *resource.Quantity {
	q := resource.MustParse(s)
	return &q
}

// fullSuccessDialer always returns a healthy mock client for the post-readiness
// ApplySchema at Reconcile step 10.
func fullSuccessDialer(ctx context.Context, endpoint string) (CartographerClient, error) {
	return &mockCartographerClient{}, nil
}

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
	return nil, errors.New("mock CartographerClient.ExportGraph not configured")
}

func (m *mockCartographerClient) Close() error {
	if m.closeFn != nil {
		return m.closeFn()
	}
	return nil
}
