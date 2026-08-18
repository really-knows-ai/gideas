package rpc

import (
	"context"
	"testing"
	"time"

	apiv1 "github.com/foundry/flow/operator/api/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
)

// Routing type string constants for test assertions.
const (
	riComplete      = "complete"
	riRouteToOutput = "route_to_output"
	riRouteTo       = "route_to"
)

func newScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	_ = apiv1.AddToScheme(s)
	return s
}

// nsName is a test helper to construct a NamespacedName in the default namespace.
func nsName(name string) types.NamespacedName {
	return types.NamespacedName{Namespace: "default", Name: name}
}

// nsCtx returns a context carrying x-flow-namespace=default metadata.
// Used by tests that call RPCs requiring namespace for CRD lookups.
func nsCtx() context.Context {
	md := metadata.Pairs("x-flow-namespace", "default")
	return metadata.NewIncomingContext(context.Background(), md)
}

// topoCtx creates a context with Sidecar-injected namespace and node identity metadata.
func topoCtx(namespace, nodeID string) context.Context {
	md := metadata.Pairs(
		"x-flow-namespace", namespace,
		"x-flow-node-id", nodeID,
		"x-flow-capabilities", "READ:flow",
	)
	return metadata.NewIncomingContext(context.Background(), md)
}

// fixedTime overrides timeNow for deterministic test output.
func fixedTime(t *testing.T) {
	t.Helper()
	orig := timeNow
	t.Cleanup(func() { timeNow = orig })
	timeNow = func() metav1.Time {
		return metav1.NewTime(time.Date(2026, 1, 1, 0, 0, 0, 123456789, time.UTC))
	}
}

// assertGRPCCode checks that the error has the expected gRPC status code.
func assertGRPCCode(t *testing.T, err error, expected codes.Code) {
	t.Helper()
	if err == nil {
		t.Fatalf("Expected gRPC error with code %s, got nil", expected)
	}
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("Expected gRPC status error, got: %v", err)
	}
	if st.Code() != expected {
		t.Fatalf("Expected gRPC code %s, got %s: %s", expected, st.Code(), st.Message())
	}
}

// childCtx creates a context with Sidecar-injected metadata for child Workitem
// operations. The caller has CREATE:workitem/child capability.
func childCtx(namespace, nodeID, workitemID string) context.Context {
	md := metadata.Pairs(
		"x-flow-namespace", namespace,
		"x-flow-node-id", nodeID,
		"x-flow-workitem-id", workitemID,
		"x-flow-capabilities", "CREATE:workitem/child,READ:flow",
	)
	return metadata.NewIncomingContext(context.Background(), md)
}

// workitemCtx creates a context with Sidecar-injected metadata that carries
// the workitem identity and namespace but no special capabilities.
func workitemCtx(workitemID string) context.Context {
	md := metadata.Pairs(
		"x-flow-workitem-id", workitemID,
		"x-flow-namespace", "default",
	)
	return metadata.NewIncomingContext(context.Background(), md)
}
