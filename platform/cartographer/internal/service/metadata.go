// Package service implements the Cartographer gRPC service with capability
// verification, transaction management, and error mapping.
//
// The capability-attestation metadata keys previously defined in this file
// now live in the shared wire-metadata package
// (github.com/foundry/flow/pkg/metadata) and are imported by capability.go and
// the service tests.
package service
