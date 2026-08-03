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

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func TestProxyServerExtractRoutingMetadata(t *testing.T) {
	t.Run("missing namespace", func(t *testing.T) {
		s := &ProxyServer{}
		ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs())
		_, _, err := s.extractRoutingMetadata(ctx)
		if err == nil {
			t.Fatal("expected error for missing namespace")
		}
		if status.Code(err) != codes.InvalidArgument {
			t.Errorf("expected InvalidArgument, got %v", status.Code(err))
		}
	})

	t.Run("valid namespace with default graph name", func(t *testing.T) {
		s := &ProxyServer{}
		ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs(
			"x-flow-namespace", "test-ns",
		))
		gotNS, name, err := s.extractRoutingMetadata(ctx)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if gotNS != "test-ns" {
			t.Errorf("expected namespace test-ns, got %q", gotNS)
		}
		if name != "flow-graph" {
			t.Errorf("expected default graph name flow-graph, got %q", name)
		}
	})

	t.Run("valid namespace with custom graph name", func(t *testing.T) {
		s := &ProxyServer{}
		ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs(
			"x-flow-namespace", "test-ns",
			"x-flow-graph-name", "my-graph",
		))
		_, name, err := s.extractRoutingMetadata(ctx)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if name != "my-graph" {
			t.Errorf("expected graph name my-graph, got %q", name)
		}
	})
}

func TestAuthCache(t *testing.T) {
	cache := newAuthCache(0) // zero TTL means entries expire immediately

	// Set and immediately get should work for >0 TTL
	cache2 := newAuthCache(0)
	cache2.Set("test-key")
	if cache2.Get("test-key") {
		t.Fatal("expected expired entry to return false")
	}

	// Verify empty cache returns false
	if cache.Get("nonexistent") {
		t.Fatal("expected nonexistent entry to return false")
	}
}

func TestProxyUnimplemented(t *testing.T) {
	s := &ProxyServer{}
	err := s.proxyUnimplemented("TestMethod")
	if err == nil {
		t.Fatal("expected non-nil error")
	}
	if status.Code(err) != codes.Unimplemented {
		t.Errorf("expected Unimplemented, got %v", status.Code(err))
	}
}
