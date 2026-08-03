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
	"sort"

	flowv1 "github.com/foundry/flow/operator/api/v1"
)

// SchemaDiffResult represents the type of schema change detected.
type SchemaDiffResult int

const (
	SchemaDiffNone           SchemaDiffResult = iota // no schema change
	SchemaDiffNonDestructive                         // additive-only: new types, new properties, rule changes, enableVectorIndex false->true
	SchemaDiffDestructive                            // removed types, removed/changed properties, enableVectorIndex true->false
)

// diffSchema compares old and new FoundryGraphSpec and returns the type of schema change.
// The old spec is from the last-applied annotation; new is the current CR spec.
// Only compares EntityTypes and EdgeTypes (schema-relevant fields).
func diffSchema(oldSpec, newSpec *flowv1.FoundryGraphSpec) SchemaDiffResult {
	if oldSpec == nil || newSpec == nil {
		return SchemaDiffNone
	}

	// Build maps for entity types by name.
	oldEntityTypes := make(map[string]flowv1.EntityTypeSpec, len(oldSpec.EntityTypes))
	for _, et := range oldSpec.EntityTypes {
		oldEntityTypes[et.Name] = et
	}
	newEntityTypes := make(map[string]flowv1.EntityTypeSpec, len(newSpec.EntityTypes))
	for _, et := range newSpec.EntityTypes {
		newEntityTypes[et.Name] = et
	}

	hasDestructive := false
	hasNonDestructive := false

	// Check for removed entity types (destructive).
	for name := range oldEntityTypes {
		if _, exists := newEntityTypes[name]; !exists {
			hasDestructive = true
		}
	}

	// Check for added entity types (non-destructive) and property-level changes.
	for name, newET := range newEntityTypes {
		oldET, exists := oldEntityTypes[name]
		if !exists {
			hasNonDestructive = true
			continue
		}

		// Check enableVectorIndex change.
		if oldET.EnableVectorIndex && !newET.EnableVectorIndex {
			hasDestructive = true
		}
		if !oldET.EnableVectorIndex && newET.EnableVectorIndex {
			hasNonDestructive = true
		}

		// Build property maps by name.
		oldProps := make(map[string]flowv1.PropertySpec, len(oldET.Properties))
		for _, p := range oldET.Properties {
			oldProps[p.Name] = p
		}
		newProps := make(map[string]flowv1.PropertySpec, len(newET.Properties))
		for _, p := range newET.Properties {
			newProps[p.Name] = p
		}

		// Check for removed or changed properties (destructive).
		for name, oldP := range oldProps {
			newP, exists := newProps[name]
			if !exists {
				hasDestructive = true
			} else if oldP.Type != newP.Type || oldP.Required != newP.Required {
				hasDestructive = true
			}
		}

		// Check for added properties (non-destructive).
		for name := range newProps {
			if _, exists := oldProps[name]; !exists {
				hasNonDestructive = true
			}
		}

		// Check for rule changes (non-destructive).
		if !rulesEqual(oldET.Rules, newET.Rules) {
			hasNonDestructive = true
		}
	}

	// Build maps for edge types by name.
	oldEdgeTypes := make(map[string]flowv1.EdgeTypeSpec, len(oldSpec.EdgeTypes))
	for _, et := range oldSpec.EdgeTypes {
		oldEdgeTypes[et.Name] = et
	}
	newEdgeTypes := make(map[string]flowv1.EdgeTypeSpec, len(newSpec.EdgeTypes))
	for _, et := range newSpec.EdgeTypes {
		newEdgeTypes[et.Name] = et
	}

	// Check for removed edge types (destructive).
	for name := range oldEdgeTypes {
		if _, exists := newEdgeTypes[name]; !exists {
			hasDestructive = true
		}
	}

	// Check for added edge types (non-destructive) and property changes.
	for name, newET := range newEdgeTypes {
		oldET, exists := oldEdgeTypes[name]
		if !exists {
			hasNonDestructive = true
			continue
		}

		// Build property maps.
		oldProps := make(map[string]flowv1.PropertySpec, len(oldET.Properties))
		for _, p := range oldET.Properties {
			oldProps[p.Name] = p
		}
		newProps := make(map[string]flowv1.PropertySpec, len(newET.Properties))
		for _, p := range newET.Properties {
			newProps[p.Name] = p
		}

		// Check for removed or changed properties.
		for name, oldP := range oldProps {
			newP, exists := newProps[name]
			if !exists {
				hasDestructive = true
			} else if oldP.Type != newP.Type || oldP.Required != newP.Required {
				hasDestructive = true
			}
		}

		// Check for added properties.
		for name := range newProps {
			if _, exists := oldProps[name]; !exists {
				hasNonDestructive = true
			}
		}
	}

	if hasDestructive {
		return SchemaDiffDestructive
	}
	if hasNonDestructive {
		return SchemaDiffNonDestructive
	}
	return SchemaDiffNone
}

// rulesEqual compares two ConnectionRule slices regardless of order.
func rulesEqual(a, b []flowv1.ConnectionRule) bool {
	if len(a) != len(b) {
		return false
	}
	// Normalize by sorting CanConnectTo and Using within each rule, then compare.
	type normalizedRule struct {
		canConnectTo []string
		using        []string
	}
	norm := func(rules []flowv1.ConnectionRule) []normalizedRule {
		result := make([]normalizedRule, len(rules))
		for i, r := range rules {
			cc := make([]string, len(r.CanConnectTo))
			copy(cc, r.CanConnectTo)
			sort.Strings(cc)
			uu := make([]string, len(r.Using))
			copy(uu, r.Using)
			sort.Strings(uu)
			result[i] = normalizedRule{canConnectTo: cc, using: uu}
		}
		return result
	}
	na, nb := norm(a), norm(b)
	for i := range na {
		if !stringSliceEqual(na[i].canConnectTo, nb[i].canConnectTo) {
			return false
		}
		if !stringSliceEqual(na[i].using, nb[i].using) {
			return false
		}
	}
	return true
}

func stringSliceEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// specSemanticallyEqual checks if two FoundryGraphSpecs are semantically identical.
// Uses semantic comparison for resource.Quantity and metav1.Duration.
func specSemanticallyEqual(a, b *flowv1.FoundryGraphSpec) bool {
	if (a == nil) != (b == nil) {
		return false
	}
	if a == nil {
		return true
	}

	// Compare storage.
	if (a.Storage == nil) != (b.Storage == nil) {
		return false
	}
	if a.Storage != nil {
		if (a.Storage.Size == nil) != (b.Storage.Size == nil) {
			return false
		}
		if a.Storage.Size != nil && a.Storage.Size.Cmp(*b.Storage.Size) != 0 {
			return false
		}
	}

	// Compare versioning.
	if (a.Versioning == nil) != (b.Versioning == nil) {
		return false
	}
	if a.Versioning != nil {
		if (a.Versioning.TransactionTimeout == nil) != (b.Versioning.TransactionTimeout == nil) {
			return false
		}
		if a.Versioning.TransactionTimeout != nil && a.Versioning.TransactionTimeout.Duration != b.Versioning.TransactionTimeout.Duration {
			return false
		}
		if (a.Versioning.Remote == nil) != (b.Versioning.Remote == nil) {
			return false
		}
		if a.Versioning.Remote != nil {
			if a.Versioning.Remote.URL != b.Versioning.Remote.URL {
				return false
			}
			if a.Versioning.Remote.PullOnInit != b.Versioning.Remote.PullOnInit {
				return false
			}
			if (a.Versioning.Remote.Auth == nil) != (b.Versioning.Remote.Auth == nil) {
				return false
			}
			if a.Versioning.Remote.Auth != nil && a.Versioning.Remote.Auth.SecretRef != b.Versioning.Remote.Auth.SecretRef {
				return false
			}
		}
	}

	return diffSchema(a, b) == SchemaDiffNone
}
