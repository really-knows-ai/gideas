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

// TestDiffSchema pins the entity-type / property / edge-type diff classification of
// diffSchema: added entity/edge types and added properties are non-destructive, while a
// removed entity/edge type, removed property, or changed property type is destructive. The
// connection-rule endpoint-pair cases live in foundrygraph_schema_rule_test.go.
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
			name: "edge type added - non-destructive",
			old: &flowv1.FoundryGraphSpec{
				EdgeTypes: []flowv1.EdgeTypeSpec{
					{Name: "DEPENDS_ON"},
				},
			},
			new: &flowv1.FoundryGraphSpec{
				EdgeTypes: []flowv1.EdgeTypeSpec{
					{Name: "DEPENDS_ON"},
					{Name: "CONTAINS"},
				},
			},
			expected: SchemaDiffNonDestructive,
		},
		{
			name: "edge type property added - non-destructive",
			old: &flowv1.FoundryGraphSpec{
				EdgeTypes: []flowv1.EdgeTypeSpec{
					{Name: "DEPENDS_ON", Properties: []flowv1.PropertySpec{{Name: "weight", Type: "int"}}},
				},
			},
			new: &flowv1.FoundryGraphSpec{
				EdgeTypes: []flowv1.EdgeTypeSpec{
					{Name: "DEPENDS_ON", Properties: []flowv1.PropertySpec{
						{Name: "weight", Type: "int"},
						{Name: "note", Type: "string"},
					}},
				},
			},
			expected: SchemaDiffNonDestructive,
		},
		{
			name: "edge type property removed - destructive",
			old: &flowv1.FoundryGraphSpec{
				EdgeTypes: []flowv1.EdgeTypeSpec{
					{Name: "DEPENDS_ON", Properties: []flowv1.PropertySpec{
						{Name: "weight", Type: "int"},
						{Name: "note", Type: "string"},
					}},
				},
			},
			new: &flowv1.FoundryGraphSpec{
				EdgeTypes: []flowv1.EdgeTypeSpec{
					{Name: "DEPENDS_ON", Properties: []flowv1.PropertySpec{{Name: "weight", Type: "int"}}},
				},
			},
			expected: SchemaDiffDestructive,
		},
		{
			name: "edge type property type changed - destructive",
			old: &flowv1.FoundryGraphSpec{
				EdgeTypes: []flowv1.EdgeTypeSpec{
					{Name: "DEPENDS_ON", Properties: []flowv1.PropertySpec{{Name: "weight", Type: "int"}}},
				},
			},
			new: &flowv1.FoundryGraphSpec{
				EdgeTypes: []flowv1.EdgeTypeSpec{
					{Name: "DEPENDS_ON", Properties: []flowv1.PropertySpec{{Name: "weight", Type: "string"}}},
				},
			},
			expected: SchemaDiffDestructive,
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
