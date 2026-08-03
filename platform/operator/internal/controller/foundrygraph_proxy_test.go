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
	"sync"
	"testing"
)

func TestProxyRoutingTable(t *testing.T) {
	t.Run("register and lookup", func(t *testing.T) {
		rt := NewProxyRoutingTable()
		rt.Register("ns1", "graph1", "cartographer-graph1.ns1.svc.cluster.local:50051")

		endpoint, ok := rt.Lookup("ns1", "graph1")
		if !ok {
			t.Fatal("expected route to be found")
		}
		if endpoint != "cartographer-graph1.ns1.svc.cluster.local:50051" {
			t.Errorf("got endpoint %q, want %q", endpoint, "cartographer-graph1.ns1.svc.cluster.local:50051")
		}
	})

	t.Run("deregister", func(t *testing.T) {
		rt := NewProxyRoutingTable()
		rt.Register("ns1", "graph1", "addr:50051")
		rt.Deregister("ns1", "graph1")

		_, ok := rt.Lookup("ns1", "graph1")
		if ok {
			t.Fatal("expected route to be removed after deregister")
		}
	})

	t.Run("lookup non-existent", func(t *testing.T) {
		rt := NewProxyRoutingTable()
		_, ok := rt.Lookup("nonexistent", "graph")
		if ok {
			t.Fatal("expected lookup of non-existent route to return false")
		}
	})

	t.Run("concurrent access", func(t *testing.T) {
		rt := NewProxyRoutingTable()
		var wg sync.WaitGroup

		// Concurrently register, lookup, and deregister.
		for i := range 50 {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				ns := "ns"
				name := "graph"
				rt.Register(ns, name, "addr:50051")
				rt.Lookup(ns, name)
				rt.Deregister(ns, name)
			}(i)
		}
		wg.Wait()
	})
}
