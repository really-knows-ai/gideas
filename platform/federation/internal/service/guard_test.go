package service

import flowv1 "github.com/gideas/flow/gen/flow/v1"

// Compile-time guard: FederationServer must implement the generated
// FederationServiceServer interface. If any method is removed or its
// signature changes, this line fails to compile.
var _ flowv1.FederationServiceServer = (*FederationServer)(nil)
