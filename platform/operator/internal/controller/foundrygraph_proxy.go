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
)

// ProxyRoutingTable is a thread-safe map of namespace/name to gRPC endpoint.
type ProxyRoutingTable struct {
	mu     sync.RWMutex
	routes map[string]string // key: "namespace/name", value: endpoint
}

// NewProxyRoutingTable creates a new ProxyRoutingTable.
func NewProxyRoutingTable() *ProxyRoutingTable {
	return &ProxyRoutingTable{
		routes: make(map[string]string),
	}
}

// routeKey builds the internal map key from namespace and name.
func routeKey(namespace, name string) string {
	return namespace + "/" + name
}

// Register adds or updates a route.
func (t *ProxyRoutingTable) Register(namespace, name, endpoint string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.routes[routeKey(namespace, name)] = endpoint
}

// Deregister removes a route.
func (t *ProxyRoutingTable) Deregister(namespace, name string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.routes, routeKey(namespace, name))
}

// Lookup returns the endpoint for a given namespace/name.
func (t *ProxyRoutingTable) Lookup(namespace, name string) (endpoint string, ok bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	endpoint, ok = t.routes[routeKey(namespace, name)]
	return
}
