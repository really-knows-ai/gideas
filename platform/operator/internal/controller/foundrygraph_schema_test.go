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

func TestDiffSchema(t *testing.T) {
	tests := []struct {
		name     string
		old      *flowv1.FoundryGraphSpec
		new      *flowv1.FoundryGraphSpec
		expected SchemaDiffResult
	}{
		{
			name: "no change",
			old: &flowv1.FoundryGraphSpec{
				EntityTypes: []flowv1.EntityTypeSpec{
					{Name: "Component", Properties: []flowv1.PropertySpec{{Name: "name", Type: "string"}}},
				},
				EdgeTypes: []flowv1.EdgeTypeSpec{
					{Name: "DEPENDS_ON"},
				},
			},
			new: &flowv1.FoundryGraphSpec{
				EntityTypes: []flowv1.EntityTypeSpec{
					{Name: "Component", Properties: []flowv1.PropertySpec{{Name: "name", Type: "string"}}},
				},
				EdgeTypes: []flowv1.EdgeTypeSpec{
					{Name: "DEPENDS_ON"},
				},
			},
			expected: SchemaDiffNone,
		},
		{
			name: "new entity type added - non-destructive",
			old: &flowv1.FoundryGraphSpec{
				EntityTypes: []flowv1.EntityTypeSpec{
					{Name: "Component"},
				},
			},
			new: &flowv1.FoundryGraphSpec{
				EntityTypes: []flowv1.EntityTypeSpec{
					{Name: "Component"},
					{Name: "Service"},
				},
			},
			expected: SchemaDiffNonDestructive,
		},
		{
			name: "new property added - non-destructive",
			old: &flowv1.FoundryGraphSpec{
				EntityTypes: []flowv1.EntityTypeSpec{
					{Name: "Component", Properties: []flowv1.PropertySpec{{Name: "name", Type: "string"}}},
				},
			},
			new: &flowv1.FoundryGraphSpec{
				EntityTypes: []flowv1.EntityTypeSpec{
					{Name: "Component", Properties: []flowv1.PropertySpec{
						{Name: "name", Type: "string"},
						{Name: "version", Type: "string"},
					}},
				},
			},
			expected: SchemaDiffNonDestructive,
		},
		{
			name: "enableVectorIndex false->true - non-destructive",
			old: &flowv1.FoundryGraphSpec{
				EntityTypes: []flowv1.EntityTypeSpec{
					{Name: "Component", EnableVectorIndex: false},
				},
			},
			new: &flowv1.FoundryGraphSpec{
				EntityTypes: []flowv1.EntityTypeSpec{
					{Name: "Component", EnableVectorIndex: true},
				},
			},
			expected: SchemaDiffNonDestructive,
		},
		{
			name: "entity type removed - destructive",
			old: &flowv1.FoundryGraphSpec{
				EntityTypes: []flowv1.EntityTypeSpec{
					{Name: "Component"},
					{Name: "Service"},
				},
			},
			new: &flowv1.FoundryGraphSpec{
				EntityTypes: []flowv1.EntityTypeSpec{
					{Name: "Component"},
				},
			},
			expected: SchemaDiffDestructive,
		},
		{
			name: "property removed - destructive",
			old: &flowv1.FoundryGraphSpec{
				EntityTypes: []flowv1.EntityTypeSpec{
					{Name: "Component", Properties: []flowv1.PropertySpec{
						{Name: "name", Type: "string"},
						{Name: "version", Type: "string"},
					}},
				},
			},
			new: &flowv1.FoundryGraphSpec{
				EntityTypes: []flowv1.EntityTypeSpec{
					{Name: "Component", Properties: []flowv1.PropertySpec{
						{Name: "name", Type: "string"},
					}},
				},
			},
			expected: SchemaDiffDestructive,
		},
		{
			name: "property type changed - destructive",
			old: &flowv1.FoundryGraphSpec{
				EntityTypes: []flowv1.EntityTypeSpec{
					{Name: "Component", Properties: []flowv1.PropertySpec{
						{Name: "name", Type: "string"},
					}},
				},
			},
			new: &flowv1.FoundryGraphSpec{
				EntityTypes: []flowv1.EntityTypeSpec{
					{Name: "Component", Properties: []flowv1.PropertySpec{
						{Name: "name", Type: "int"},
					}},
				},
			},
			expected: SchemaDiffDestructive,
		},
		{
			name: "enableVectorIndex true->false - destructive",
			old: &flowv1.FoundryGraphSpec{
				EntityTypes: []flowv1.EntityTypeSpec{
					{Name: "Component", EnableVectorIndex: true},
				},
			},
			new: &flowv1.FoundryGraphSpec{
				EntityTypes: []flowv1.EntityTypeSpec{
					{Name: "Component", EnableVectorIndex: false},
				},
			},
			expected: SchemaDiffDestructive,
		},
		{
			name: "edge type removed - destructive",
			old: &flowv1.FoundryGraphSpec{
				EdgeTypes: []flowv1.EdgeTypeSpec{
					{Name: "DEPENDS_ON"},
					{Name: "CONTAINS"},
				},
			},
			new: &flowv1.FoundryGraphSpec{
				EdgeTypes: []flowv1.EdgeTypeSpec{
					{Name: "DEPENDS_ON"},
				},
			},
			expected: SchemaDiffDestructive,
		},
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
			name: "rules modified only - non-destructive",
			old: &flowv1.FoundryGraphSpec{
				EntityTypes: []flowv1.EntityTypeSpec{
					{
						Name: "Component",
						Rules: []flowv1.ConnectionRule{
							{CanConnectTo: []string{"Service"}, Using: []string{"DEPENDS_ON"}},
						},
					},
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
			},
			expected: SchemaDiffNonDestructive,
		},
		{
			name: "canConnectTo entry removed - non-destructive (membership change surfaced)",
			// Removing a member from an OR membership list is a semantically relevant
			// change (it narrows which connections are permitted) that must NOT collapse to
			// SchemaDiffNone — the old spec permitted "Service", the new one no longer does.
			old: &flowv1.FoundryGraphSpec{
				EntityTypes: []flowv1.EntityTypeSpec{
					{Name: "Component", Rules: []flowv1.ConnectionRule{{CanConnectTo: []string{"Service", "Database"}, Using: []string{"DEPENDS_ON"}}}},
				},
			},
			new: &flowv1.FoundryGraphSpec{
				EntityTypes: []flowv1.EntityTypeSpec{
					{Name: "Component", Rules: []flowv1.ConnectionRule{{CanConnectTo: []string{"Database"}, Using: []string{"DEPENDS_ON"}}}},
				},
			},
			expected: SchemaDiffNonDestructive,
		},
		{
			name: "using entry removed - non-destructive (membership change)",
			old: &flowv1.FoundryGraphSpec{
				EntityTypes: []flowv1.EntityTypeSpec{
					{Name: "Component", Rules: []flowv1.ConnectionRule{{CanConnectTo: []string{"Service"}, Using: []string{"DEPENDS_ON", "CONTAINS"}}}},
				},
			},
			new: &flowv1.FoundryGraphSpec{
				EntityTypes: []flowv1.EntityTypeSpec{
					{Name: "Component", Rules: []flowv1.ConnectionRule{{CanConnectTo: []string{"Service"}, Using: []string{"DEPENDS_ON"}}}},
				},
			},
			expected: SchemaDiffNonDestructive,
		},
		{
			name: "whole rule dropped - non-destructive (membership change)",
			old: &flowv1.FoundryGraphSpec{
				EntityTypes: []flowv1.EntityTypeSpec{
					{Name: "Component", Rules: []flowv1.ConnectionRule{
						{CanConnectTo: []string{"Service"}, Using: []string{"DEPENDS_ON"}},
						{CanConnectTo: []string{"Database"}, Using: []string{"CONTAINS"}},
					}},
				},
			},
			new: &flowv1.FoundryGraphSpec{
				EntityTypes: []flowv1.EntityTypeSpec{
					{Name: "Component", Rules: []flowv1.ConnectionRule{{CanConnectTo: []string{"Service"}, Using: []string{"DEPENDS_ON"}}}},
				},
			},
			expected: SchemaDiffNonDestructive,
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

func TestSchemaDuplicateNames(t *testing.T) {
	tests := []struct {
		name string
		spec *flowv1.FoundryGraphSpec
		want string
	}{
		{
			name: "no duplicates",
			spec: &flowv1.FoundryGraphSpec{
				EntityTypes: []flowv1.EntityTypeSpec{{Name: "A"}, {Name: "B"}},
				EdgeTypes:   []flowv1.EdgeTypeSpec{{Name: "C"}},
			},
			want: "",
		},
		{
			name: "duplicate entity name",
			spec: &flowv1.FoundryGraphSpec{
				EntityTypes: []flowv1.EntityTypeSpec{{Name: "A"}, {Name: "A"}},
			},
			want: "duplicate entity type name A",
		},
		{
			name: "duplicate edge name",
			spec: &flowv1.FoundryGraphSpec{
				EdgeTypes: []flowv1.EdgeTypeSpec{{Name: "X"}, {Name: "X"}},
			},
			want: "duplicate edge type name X",
		},
		{
			name: "cross-list overlap allowed",
			spec: &flowv1.FoundryGraphSpec{
				EntityTypes: []flowv1.EntityTypeSpec{{Name: "A"}},
				EdgeTypes:   []flowv1.EdgeTypeSpec{{Name: "A"}},
			},
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := schemaDuplicateNames(tt.spec); got != tt.want {
				t.Errorf("schemaDuplicateNames() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSpecSemanticallyEqual(t *testing.T) {
	tests := []struct {
		name string
		a, b *flowv1.FoundryGraphSpec
		want bool
	}{
		{
			name: "both nil",
			a:    nil,
			b:    nil,
			want: true,
		},
		{
			name: "one nil",
			a:    &flowv1.FoundryGraphSpec{},
			b:    nil,
			want: false,
		},
		{
			name: "same spec",
			a: &flowv1.FoundryGraphSpec{
				EntityTypes: []flowv1.EntityTypeSpec{{Name: "Component"}},
			},
			b: &flowv1.FoundryGraphSpec{
				EntityTypes: []flowv1.EntityTypeSpec{{Name: "Component"}},
			},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := specSemanticallyEqual(tt.a, tt.b)
			if got != tt.want {
				t.Errorf("specSemanticallyEqual(%q) = %v, want %v", tt.name, got, tt.want)
			}
		})
	}
}
