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
	"testing"

	flowv1 "github.com/foundry/flow/operator/api/v1"
)

// TestDiffSchemaConnectionRules pins diffSchema's connection-rule classification (SPEC
// R6 endpoint-pair semantics) plus the required-toggle and deduplication rules: a rule
// change that alters an already-applied edge type's FROM/TO endpoint set is destructive,
// while one that preserves every endpoint pair is non-destructive; required toggles are
// DDL-neutral (non-destructive) and duplicate members/entries are semantically inert.
func TestDiffSchemaConnectionRules(t *testing.T) {
	tests := []struct {
		name     string
		old      *flowv1.FoundryGraphSpec
		new      *flowv1.FoundryGraphSpec
		expected SchemaDiffResult
	}{
		{
			name: "reordered rules - no diff",
			old: &flowv1.FoundryGraphSpec{
				EntityTypes: []flowv1.EntityTypeSpec{
					{
						Name: "Component",
						Rules: []flowv1.ConnectionRule{
							{CanConnectTo: []string{"Service"}, Using: []string{"DEPENDS_ON"}},
							{CanConnectTo: []string{"Database"}, Using: []string{"CONTAINS"}},
						},
					},
				},
			},
			new: &flowv1.FoundryGraphSpec{
				EntityTypes: []flowv1.EntityTypeSpec{
					{
						Name: "Component",
						// Rules reversed — SPEC R1 treats the list as OR-ed, order is
						// semantically irrelevant, so this must be SchemaDiffNone.
						Rules: []flowv1.ConnectionRule{
							{CanConnectTo: []string{"Database"}, Using: []string{"CONTAINS"}},
							{CanConnectTo: []string{"Service"}, Using: []string{"DEPENDS_ON"}},
						},
					},
				},
			},
			expected: SchemaDiffNone,
		},
		{
			name: "rule adds FROM/TO pair to applied edge type - destructive",
			// SPEC R6: a rule modification that adds a FROM/TO pair on an
			// already-applied edge type is destructive — LadybugDB fixes the rel
			// table's endpoint clauses at CREATE time. The store's
			// diffSchemaAgainstCatalog rejects this with ErrDestructiveSchemaChange,
			// so the operator must classify it destructive instead of pushing the
			// schema directly and wedging on the store rejection.
			old: &flowv1.FoundryGraphSpec{
				EntityTypes: []flowv1.EntityTypeSpec{
					{
						Name: "Component",
						Rules: []flowv1.ConnectionRule{
							{CanConnectTo: []string{"Service"}, Using: []string{"DEPENDS_ON"}},
						},
					},
				},
				EdgeTypes: []flowv1.EdgeTypeSpec{
					{Name: "DEPENDS_ON"},
				},
			},
			new: &flowv1.FoundryGraphSpec{
				EntityTypes: []flowv1.EntityTypeSpec{
					{
						Name: "Component",
						Rules: []flowv1.ConnectionRule{
							{CanConnectTo: []string{"Service", "Database"}, Using: []string{"DEPENDS_ON"}},
						},
					},
				},
				EdgeTypes: []flowv1.EdgeTypeSpec{
					{Name: "DEPENDS_ON"},
				},
			},
			expected: SchemaDiffDestructive,
		},
		{
			name: "canConnectTo entry removed - destructive (endpoint pair removed)",
			// Removing a target from an OR membership list removes a FROM/TO pair
			// on the applied edge type, so it is destructive (SPEC R6); the store
			// rejects it with ErrDestructiveSchemaChange.
			old: &flowv1.FoundryGraphSpec{
				EntityTypes: []flowv1.EntityTypeSpec{
					{Name: "Component", Rules: []flowv1.ConnectionRule{{CanConnectTo: []string{"Service", "Database"}, Using: []string{"DEPENDS_ON"}}}},
				},
				EdgeTypes: []flowv1.EdgeTypeSpec{
					{Name: "DEPENDS_ON"},
				},
			},
			new: &flowv1.FoundryGraphSpec{
				EntityTypes: []flowv1.EntityTypeSpec{
					{Name: "Component", Rules: []flowv1.ConnectionRule{{CanConnectTo: []string{"Database"}, Using: []string{"DEPENDS_ON"}}}},
				},
				EdgeTypes: []flowv1.EdgeTypeSpec{
					{Name: "DEPENDS_ON"},
				},
			},
			expected: SchemaDiffDestructive,
		},
		{
			name: "using entry removed - destructive (edge type loses endpoint pair)",
			// Removing a `using` reference drops the edge type's FROM/TO pair set:
			// the applied edge type's rel endpoints would change (the store
			// normalizes an edgeless type to its reserved placeholder pair and
			// rejects the change), so it is destructive (SPEC R6).
			old: &flowv1.FoundryGraphSpec{
				EntityTypes: []flowv1.EntityTypeSpec{
					{Name: "Component", Rules: []flowv1.ConnectionRule{{CanConnectTo: []string{"Service"}, Using: []string{"DEPENDS_ON", "CONTAINS"}}}},
				},
				EdgeTypes: []flowv1.EdgeTypeSpec{
					{Name: "DEPENDS_ON"},
					{Name: "CONTAINS"},
				},
			},
			new: &flowv1.FoundryGraphSpec{
				EntityTypes: []flowv1.EntityTypeSpec{
					{Name: "Component", Rules: []flowv1.ConnectionRule{{CanConnectTo: []string{"Service"}, Using: []string{"DEPENDS_ON"}}}},
				},
				EdgeTypes: []flowv1.EdgeTypeSpec{
					{Name: "DEPENDS_ON"},
					{Name: "CONTAINS"},
				},
			},
			expected: SchemaDiffDestructive,
		},
		{
			name: "whole rule dropped - destructive (edge type loses endpoint pair)",
			// Dropping an entire rule removes the FROM/TO pairs it contributed,
			// changing an applied edge type's endpoint set → destructive (SPEC R6);
			// the store rejects the resulting edge-endpoint change.
			old: &flowv1.FoundryGraphSpec{
				EntityTypes: []flowv1.EntityTypeSpec{
					{Name: "Component", Rules: []flowv1.ConnectionRule{
						{CanConnectTo: []string{"Service"}, Using: []string{"DEPENDS_ON"}},
						{CanConnectTo: []string{"Database"}, Using: []string{"CONTAINS"}},
					}},
				},
				EdgeTypes: []flowv1.EdgeTypeSpec{
					{Name: "DEPENDS_ON"},
					{Name: "CONTAINS"},
				},
			},
			new: &flowv1.FoundryGraphSpec{
				EntityTypes: []flowv1.EntityTypeSpec{
					{Name: "Component", Rules: []flowv1.ConnectionRule{{CanConnectTo: []string{"Service"}, Using: []string{"DEPENDS_ON"}}}},
				},
				EdgeTypes: []flowv1.EdgeTypeSpec{
					{Name: "DEPENDS_ON"},
					{Name: "CONTAINS"},
				},
			},
			expected: SchemaDiffDestructive,
		},
		{
			name: "rule change preserving endpoint pairs - non-destructive",
			// SPEC R6: a rule modification that preserves every edge type's FROM/TO
			// endpoint set is non-destructive — the rel table's endpoint clauses are
			// unchanged, so the schema pushes directly without WipeGraph. Splitting
			// one rule into two OR-ed rules over the same pairs changes the rule set
			// but keeps the endpoint pairs identical.
			old: &flowv1.FoundryGraphSpec{
				EntityTypes: []flowv1.EntityTypeSpec{
					{
						Name: "Component",
						Rules: []flowv1.ConnectionRule{
							{CanConnectTo: []string{"Service", "Database"}, Using: []string{"DEPENDS_ON"}},
						},
					},
				},
				EdgeTypes: []flowv1.EdgeTypeSpec{
					{Name: "DEPENDS_ON"},
				},
			},
			new: &flowv1.FoundryGraphSpec{
				EntityTypes: []flowv1.EntityTypeSpec{
					{
						Name: "Component",
						Rules: []flowv1.ConnectionRule{
							{CanConnectTo: []string{"Service"}, Using: []string{"DEPENDS_ON"}},
							{CanConnectTo: []string{"Database"}, Using: []string{"DEPENDS_ON"}},
						},
					},
				},
				EdgeTypes: []flowv1.EdgeTypeSpec{
					{Name: "DEPENDS_ON"},
				},
			},
			expected: SchemaDiffNonDestructive,
		},
		{
			name: "new entity type with brand-new edge type - non-destructive",
			// SPEC R2: an added entity type carrying a brand-new edge type remains
			// additive — the new rel table is created with its endpoint clauses at
			// CREATE time. The endpoint-set comparison only covers already-applied
			// edge types, so this must not be classified destructive.
			old: &flowv1.FoundryGraphSpec{
				EntityTypes: []flowv1.EntityTypeSpec{
					{Name: "Component"},
				},
				EdgeTypes: []flowv1.EdgeTypeSpec{
					{Name: "DEPENDS_ON"},
				},
			},
			new: &flowv1.FoundryGraphSpec{
				EntityTypes: []flowv1.EntityTypeSpec{
					{Name: "Component"},
					{Name: "Service", Rules: []flowv1.ConnectionRule{
						{CanConnectTo: []string{"Component"}, Using: []string{"CONTAINS"}},
					}},
				},
				EdgeTypes: []flowv1.EdgeTypeSpec{
					{Name: "DEPENDS_ON"},
					{Name: "CONTAINS"},
				},
			},
			expected: SchemaDiffNonDestructive,
		},
		{
			name: "new entity type adds FROM/TO pair to applied edge type - destructive",
			// A new entity type is additive by itself, but if its rules add a
			// FROM/TO pair to an edge type that is already applied, the rel table's
			// endpoints would change → destructive (SPEC R6), matching the store's
			// catalog diff. The endpoint-set comparison therefore runs even when no
			// surviving entity type's rules changed.
			old: &flowv1.FoundryGraphSpec{
				EntityTypes: []flowv1.EntityTypeSpec{
					{Name: "Component", Rules: []flowv1.ConnectionRule{
						{CanConnectTo: []string{"Service"}, Using: []string{"DEPENDS_ON"}},
					}},
				},
				EdgeTypes: []flowv1.EdgeTypeSpec{
					{Name: "DEPENDS_ON"},
				},
			},
			new: &flowv1.FoundryGraphSpec{
				EntityTypes: []flowv1.EntityTypeSpec{
					{Name: "Component", Rules: []flowv1.ConnectionRule{
						{CanConnectTo: []string{"Service"}, Using: []string{"DEPENDS_ON"}},
					}},
					{Name: "Database", Rules: []flowv1.ConnectionRule{
						{CanConnectTo: []string{"Service"}, Using: []string{"DEPENDS_ON"}},
					}},
				},
				EdgeTypes: []flowv1.EdgeTypeSpec{
					{Name: "DEPENDS_ON"},
				},
			},
			expected: SchemaDiffDestructive,
		},
		{
			name: "properties reorder - no diff (non-destructive is NOT implied or destructive)",
			// A mere reordering of the properties list is semantically irrelevant (the
			// property schema is a name→{type,required} set) and must not be classified as
			// destructive (or any diff) — ordering does not change the underlying columns.
			old: &flowv1.FoundryGraphSpec{
				EntityTypes: []flowv1.EntityTypeSpec{
					{Name: "Component", Properties: []flowv1.PropertySpec{{Name: "z", Type: "string"}, {Name: "a", Type: "int", Required: true}}},
				},
			},
			new: &flowv1.FoundryGraphSpec{
				EntityTypes: []flowv1.EntityTypeSpec{
					{Name: "Component", Properties: []flowv1.PropertySpec{{Name: "a", Type: "int", Required: true}, {Name: "z", Type: "string"}}},
				},
			},
			expected: SchemaDiffNone,
		},
		{
			name: "entity property required toggled - non-destructive",
			// The Required flag is application-only write-enforcement metadata: the
			// store persists it in schema.json, never in the catalog/DDL
			// (diffSchemaAgainstCatalog compares only the mapped column type), and
			// SPEC R6 makes its enforcement forward-only. A bare toggle is
			// DDL-neutral and safe — ApplySchema accepts it without WipeGraph — so
			// the operator classifies it non-destructive (matching the store) and
			// pushes it via ApplySchema to refresh the in-memory Required metadata.
			// It must never be destructive, which would WipeGraph the whole graph.
			old: &flowv1.FoundryGraphSpec{
				EntityTypes: []flowv1.EntityTypeSpec{
					{Name: "Component", Properties: []flowv1.PropertySpec{{Name: "name", Type: "string", Required: false}}},
				},
			},
			new: &flowv1.FoundryGraphSpec{
				EntityTypes: []flowv1.EntityTypeSpec{
					{Name: "Component", Properties: []flowv1.PropertySpec{{Name: "name", Type: "string", Required: true}}},
				},
			},
			expected: SchemaDiffNonDestructive,
		},
		{
			name: "entity property required toggled false - non-destructive",
			// Same branch as above, reverse direction: the required-toggle
			// classification is symmetric (the comparison is inequality-based), so a
			// true→false toggle must also be non-destructive.
			old: &flowv1.FoundryGraphSpec{
				EntityTypes: []flowv1.EntityTypeSpec{
					{Name: "Component", Properties: []flowv1.PropertySpec{{Name: "name", Type: "string", Required: true}}},
				},
			},
			new: &flowv1.FoundryGraphSpec{
				EntityTypes: []flowv1.EntityTypeSpec{
					{Name: "Component", Properties: []flowv1.PropertySpec{{Name: "name", Type: "string", Required: false}}},
				},
			},
			expected: SchemaDiffNonDestructive,
		},
		{
			name: "edge type property required toggled - non-destructive",
			// Same classification as the entity-property toggle (the shared
			// diffProperties branch): a bare Required toggle on an existing edge
			// property is DDL-neutral application-only metadata — non-destructive,
			// matching the store's diffSchemaAgainstCatalog edge-property check.
			old: &flowv1.FoundryGraphSpec{
				EdgeTypes: []flowv1.EdgeTypeSpec{
					{Name: "DEPENDS_ON", Properties: []flowv1.PropertySpec{{Name: "weight", Type: "int", Required: false}}},
				},
			},
			new: &flowv1.FoundryGraphSpec{
				EdgeTypes: []flowv1.EdgeTypeSpec{
					{Name: "DEPENDS_ON", Properties: []flowv1.PropertySpec{{Name: "weight", Type: "int", Required: true}}},
				},
			},
			expected: SchemaDiffNonDestructive,
		},
		{
			name: "canConnectTo duplicate members - no diff (set-based membership)",
			// SPEC R1 matches each list by set membership, so [A,A] ≡ [A]: a redundant
			// duplicate must not surface as a schema change (it is SchemaDiffNone, not
			// non-destructive/destructive).
			old: &flowv1.FoundryGraphSpec{
				EntityTypes: []flowv1.EntityTypeSpec{
					{Name: "Component", Rules: []flowv1.ConnectionRule{{CanConnectTo: []string{"A", "A"}, Using: []string{"DEPENDS_ON"}}}},
				},
			},
			new: &flowv1.FoundryGraphSpec{
				EntityTypes: []flowv1.EntityTypeSpec{
					{Name: "Component", Rules: []flowv1.ConnectionRule{{CanConnectTo: []string{"A"}, Using: []string{"DEPENDS_ON"}}}},
				},
			},
			expected: SchemaDiffNone,
		},
		{
			name: "using duplicate members - no diff (set-based membership)",
			old: &flowv1.FoundryGraphSpec{
				EntityTypes: []flowv1.EntityTypeSpec{
					{Name: "Component", Rules: []flowv1.ConnectionRule{{CanConnectTo: []string{"A"}, Using: []string{"E", "E"}}}},
				},
			},
			new: &flowv1.FoundryGraphSpec{
				EntityTypes: []flowv1.EntityTypeSpec{
					{Name: "Component", Rules: []flowv1.ConnectionRule{{CanConnectTo: []string{"A"}, Using: []string{"E"}}}},
				},
			},
			expected: SchemaDiffNone,
		},
		{
			name: "duplicated rule entry - no diff (rules are OR-ed, duplicates are no-ops)",
			old: &flowv1.FoundryGraphSpec{
				EntityTypes: []flowv1.EntityTypeSpec{
					{Name: "Component", Rules: []flowv1.ConnectionRule{
						{CanConnectTo: []string{"A"}, Using: []string{"E"}},
						{CanConnectTo: []string{"A"}, Using: []string{"E"}},
					}},
				},
			},
			new: &flowv1.FoundryGraphSpec{
				EntityTypes: []flowv1.EntityTypeSpec{
					{Name: "Component", Rules: []flowv1.ConnectionRule{
						{CanConnectTo: []string{"A"}, Using: []string{"E"}},
					}},
				},
			},
			expected: SchemaDiffNone,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := diffSchema(tt.old, tt.new)
			if result != tt.expected {
				t.Errorf("diffSchema(%q) = %d, want %d", tt.name, result, tt.expected)
			}
		})
	}
}
