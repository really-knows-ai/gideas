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
	"fmt"
	"sort"
	"strings"

	flowv1 "github.com/foundry/flow/operator/api/v1"
)

// SchemaDiffResult represents the type of schema change detected.
type SchemaDiffResult int

const (
	SchemaDiffNone           SchemaDiffResult = iota // no schema change
	SchemaDiffNonDestructive                         // additive-only: new types, new properties, rule changes preserving every edge type's FROM/TO endpoint set, enableVectorIndex false->true
	SchemaDiffDestructive                            // removed types, removed/changed properties, edge-type FROM/TO endpoint-set changes, enableVectorIndex true->false
)

// diffSchema compares old and new FoundryGraphSpec and returns the type of schema change.
// The old spec is from the last-applied annotation; new is the current CR spec.
// Only compares EntityTypes and EdgeTypes (schema-relevant fields).
func diffSchema(oldSpec, newSpec *flowv1.FoundryGraphSpec) SchemaDiffResult {
	if oldSpec == nil || newSpec == nil {
		return SchemaDiffNone
	}

	hasDestructive, hasNonDestructive := diffEntityTypes(oldSpec, newSpec)
	d, nd := diffEdgeTypes(oldSpec, newSpec)
	hasDestructive = hasDestructive || d
	hasNonDestructive = hasNonDestructive || nd

	if hasDestructive {
		return SchemaDiffDestructive
	}
	if hasNonDestructive {
		return SchemaDiffNonDestructive
	}
	return SchemaDiffNone
}

// diffEntityTypes compares the entity-type sets between old and new specs. Removed
// entity types, removed or changed existing type properties, enableVectorIndex
// true→false, and rule changes that add or remove a FROM/TO pair on an already-applied
// edge type are destructive; added types, added properties, and rule changes that
// preserve every edge type's FROM/TO endpoint set are non-destructive (SPEC R6).
func diffEntityTypes(oldSpec, newSpec *flowv1.FoundryGraphSpec) (destructive, nonDestructive bool) {
	oldTypes := make(map[string]flowv1.EntityTypeSpec, len(oldSpec.EntityTypes))
	for _, et := range oldSpec.EntityTypes {
		oldTypes[et.Name] = et
	}
	newTypes := make(map[string]flowv1.EntityTypeSpec, len(newSpec.EntityTypes))
	for _, et := range newSpec.EntityTypes {
		newTypes[et.Name] = et
	}

	// Removed entity types are destructive.
	for name := range oldTypes {
		if _, exists := newTypes[name]; !exists {
			destructive = true
		}
	}

	// Added types, property changes, and rule changes per surviving type.
	rulesChanged := false
	for name, newET := range newTypes {
		oldET, exists := oldTypes[name]
		if !exists {
			nonDestructive = true
			continue
		}

		if oldET.EnableVectorIndex && !newET.EnableVectorIndex {
			destructive = true
		}
		if !oldET.EnableVectorIndex && newET.EnableVectorIndex {
			nonDestructive = true
		}

		d, nd := diffProperties(oldET.Properties, newET.Properties)
		destructive = destructive || d
		nonDestructive = nonDestructive || nd

		if !rulesEqual(oldET.Rules, newET.Rules) {
			rulesChanged = true
		}
	}

	// SPEC R6: a rule modification that adds or removes a FROM/TO pair on an
	// already-applied edge type is destructive — LadybugDB fixes a rel table's
	// endpoint clauses at CREATE time and cannot express a change through ALTER —
	// so the operator must take the WipeGraph path, mirroring the store's
	// diffSchemaAgainstCatalog rejection (ErrDestructiveSchemaChange). A rule
	// change that preserves every edge type's endpoint set (e.g. one rule split
	// into OR-ed rules over the same pairs) is non-destructive. The endpoint sets
	// are derived from ENTITY rules (each entity type's rules' using ×
	// canConnectTo), so this comparison belongs here where the rules are compared.
	// It runs unconditionally — a new entity type's rules can also add pairs to an
	// already-applied edge type.
	if edgeEndpointSetsChanged(edgeEndpointSets(oldSpec.EntityTypes), edgeEndpointSets(newSpec.EntityTypes), oldSpec.EdgeTypes) {
		destructive = true
	} else if rulesChanged {
		nonDestructive = true
	}
	return destructive, nonDestructive
}

// diffEdgeTypes compares the edge-type sets between old and new specs with the same
// semantics as diffEntityTypes (edge types carry no enableVectorIndex flag).
func diffEdgeTypes(oldSpec, newSpec *flowv1.FoundryGraphSpec) (destructive, nonDestructive bool) {
	oldTypes := make(map[string]flowv1.EdgeTypeSpec, len(oldSpec.EdgeTypes))
	for _, et := range oldSpec.EdgeTypes {
		oldTypes[et.Name] = et
	}
	newTypes := make(map[string]flowv1.EdgeTypeSpec, len(newSpec.EdgeTypes))
	for _, et := range newSpec.EdgeTypes {
		newTypes[et.Name] = et
	}

	for name := range oldTypes {
		if _, exists := newTypes[name]; !exists {
			destructive = true
		}
	}
	for name, newET := range newTypes {
		oldET, exists := oldTypes[name]
		if !exists {
			nonDestructive = true
			continue
		}
		d, nd := diffProperties(oldET.Properties, newET.Properties)
		destructive = destructive || d
		nonDestructive = nonDestructive || nd
	}
	return destructive, nonDestructive
}

// diffProperties reports whether a property-set change is destructive (a property was
// removed or an existing property's Type/Required declaration changed) or
// non-destructive (properties were added). SPEC R6 defines destructive as "removed or
// changed existing type properties"; adding new properties is additive-only.
func diffProperties(oldProps, newProps []flowv1.PropertySpec) (destructive, nonDestructive bool) {
	oldMap := make(map[string]flowv1.PropertySpec, len(oldProps))
	for _, p := range oldProps {
		oldMap[p.Name] = p
	}
	newMap := make(map[string]flowv1.PropertySpec, len(newProps))
	for _, p := range newProps {
		newMap[p.Name] = p
	}

	for name, oldP := range oldMap {
		newP, exists := newMap[name]
		if !exists {
			destructive = true
		} else if oldP.Type != newP.Type || oldP.Required != newP.Required {
			destructive = true
		}
	}
	for name := range newMap {
		if _, exists := oldMap[name]; !exists {
			nonDestructive = true
		}
	}
	return destructive, nonDestructive
}

// rulesEqual compares two ConnectionRule slices. Per SPEC R1, both the `rules` list entries
// (OR-ed) and the members within each canConnectTo/using list (implicit OR) are matched by
// set membership — order AND duplicates within a list are semantically irrelevant. So
// `canConnectTo: [A]` ≡ `[A, A]` and a duplicated rule entry is a no-op. The comparison is
// therefore set-based at every level: each rule is canonically the sorted (de-duplicated)
// membership of its canConnectTo/using lists, and the rule sets are compared as unordered,
// deduplicated sets.
func rulesEqual(a, b []flowv1.ConnectionRule) bool {
	na, nb := ruleSet(a), ruleSet(b)
	if len(na) != len(nb) {
		return false
	}
	for k := range na {
		if !nb[k] {
			return false
		}
	}
	return true
}

// ruleSet canonises a ConnectionRule slice into an unordered set of canonical rule strings.
func ruleSet(rules []flowv1.ConnectionRule) map[string]bool {
	set := make(map[string]bool, len(rules))
	for _, r := range rules {
		// De-duplicate within each list (membership is implicit OR, so [A,A] ≡ [A]) and sort
		// so equal memberships compare equal regardless of declaration order.
		set[fmt.Sprintf("%s|%s", strings.Join(dedupeSorted(r.CanConnectTo), "\x00"), strings.Join(dedupeSorted(r.Using), "\x00"))] = true
	}
	return set
}

// dedupeSorted returns the de-duplicated, sorted form of a membership list.
func dedupeSorted(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, v := range in {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	sort.Strings(out)
	return out
}

// edgeEndpointSets derives each edge type's FROM/TO endpoint set from the
// entity-type rules, mirroring the store's collectFromToPairs (SPEC R2): for each
// entity type, for each rule, for each `using` edge reference, for each
// `canConnectTo` target, the edge type gains a (from=entityType, to=target) pair.
// Each endpoint set is a set of canonical "from\x00to" keys, so comparison is
// set-based — order and duplicates within the lists are semantically irrelevant
// (SPEC R1). An edge type absent from the map has an empty endpoint set.
func edgeEndpointSets(types []flowv1.EntityTypeSpec) map[string]map[string]bool {
	sets := make(map[string]map[string]bool)
	for _, et := range types {
		for _, rule := range et.Rules {
			for _, edgeRef := range rule.Using {
				if sets[edgeRef] == nil {
					sets[edgeRef] = make(map[string]bool)
				}
				for _, target := range rule.CanConnectTo {
					sets[edgeRef][et.Name+"\x00"+target] = true
				}
			}
		}
	}
	return sets
}

// edgeEndpointSetsChanged reports whether any already-applied edge type's FROM/TO
// endpoint set differs between the old and new spec. Only edge types present in the
// old spec (the applied schema) are compared: a brand-new edge type is additive and
// creates its rel table with its endpoints at CREATE time (SPEC R2), so it can never
// make the diff destructive regardless of the pairs it carries.
func edgeEndpointSetsChanged(oldSets, newSets map[string]map[string]bool, appliedEdgeTypes []flowv1.EdgeTypeSpec) bool {
	for _, et := range appliedEdgeTypes {
		oldSet, newSet := oldSets[et.Name], newSets[et.Name]
		if len(oldSet) != len(newSet) {
			return true
		}
		for k := range oldSet {
			if !newSet[k] {
				return true
			}
		}
	}
	return false
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
