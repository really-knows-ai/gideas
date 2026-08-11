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
	"time"

	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

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
			name: "entity property required toggled - destructive",
			// SPEC R6 lists "removed or changed existing type properties" as destructive:
			// a bare toggle of an existing property's Required flag changes the existing
			// property's schema metadata (not just forward-only runtime enforcement), so it
			// requires WipeGraph → destructive.
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
			expected: SchemaDiffDestructive,
		},
		{
			name: "edge type property required toggled - destructive",
			// Same branch as above (foundrygraph_schema.go:175) for edge-type properties: a
			// bare Required toggle on an existing edge property is a changed property
			// → destructive.
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
			expected: SchemaDiffDestructive,
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
		{
			// These storage/versioning equality and inequality paths gate the SPEC R6
			// redeploy-vs-apply decision: a change to storage.size or any versioning field
			// triggers a Cartographer redeployment (patched PVC / updated env) without
			// WipeGraph, so specSemanticallyEqual must be able to distinguish them.
			name: "storage.size equal - same",
			a:    specWithStorage("2Gi"),
			b:    specWithStorage("2Gi"),
			want: true,
		},
		{
			name: "storage.size different - not equal (R6 redeploy)",
			a:    specWithStorage("1Gi"),
			b:    specWithStorage("3Gi"),
			want: false,
		},
		{
			name: "storage nil vs set - not equal",
			a:    &flowv1.FoundryGraphSpec{},
			b:    specWithStorage("1Gi"),
			want: false,
		},
		{
			name: "versioning.transactionTimeout equal",
			a:    specWithVersioning(30),
			b:    specWithVersioning(30),
			want: true,
		},
		{
			name: "versioning.transactionTimeout different - not equal (R6 redeploy)",
			a:    specWithVersioning(30),
			b:    specWithVersioning(45),
			want: false,
		},
		{
			name: "versioning.remote equal",
			a: &flowv1.FoundryGraphSpec{Versioning: &flowv1.VersioningSpec{
				Remote: &flowv1.RemoteConfig{URL: "https://github.com/org/repo.git", PullOnInit: true, Auth: &flowv1.RemoteAuth{SecretRef: "secret-a"}},
			}},
			b: &flowv1.FoundryGraphSpec{Versioning: &flowv1.VersioningSpec{
				Remote: &flowv1.RemoteConfig{URL: "https://github.com/org/repo.git", PullOnInit: true, Auth: &flowv1.RemoteAuth{SecretRef: "secret-a"}},
			}},
			want: true,
		},
		{
			name: "versioning.remote.url different - not equal (R6 redeploy)",
			a: &flowv1.FoundryGraphSpec{Versioning: &flowv1.VersioningSpec{
				Remote: &flowv1.RemoteConfig{URL: "https://github.com/org/a.git"},
			}},
			b: &flowv1.FoundryGraphSpec{Versioning: &flowv1.VersioningSpec{
				Remote: &flowv1.RemoteConfig{URL: "https://github.com/org/b.git"},
			}},
			want: false,
		},
		{
			name: "versioning.remote.pullOnInit different - not equal (R6 redeploy)",
			a: &flowv1.FoundryGraphSpec{Versioning: &flowv1.VersioningSpec{
				Remote: &flowv1.RemoteConfig{PullOnInit: false},
			}},
			b: &flowv1.FoundryGraphSpec{Versioning: &flowv1.VersioningSpec{
				Remote: &flowv1.RemoteConfig{PullOnInit: true},
			}},
			want: false,
		},
		{
			name: "versioning.remote.auth.secretRef different - not equal (R6 redeploy/teardown)",
			a: &flowv1.FoundryGraphSpec{Versioning: &flowv1.VersioningSpec{
				Remote: &flowv1.RemoteConfig{Auth: &flowv1.RemoteAuth{SecretRef: "secret-a"}},
			}},
			b: &flowv1.FoundryGraphSpec{Versioning: &flowv1.VersioningSpec{
				Remote: &flowv1.RemoteConfig{Auth: &flowv1.RemoteAuth{SecretRef: "secret-b"}},
			}},
			want: false,
		},
		// nil-vs-set subfield branches inside non-nil containers (foundrygraph_schema.go):
		// each is a distinct false-returning branch that gates the SPEC R6
		// redeploy-vs-apply decision and must be pinned directly — a set subfield on one
		// side and nil on the other is a semantic difference even when the container is
		// non-nil on both sides.
		{
			name: "storage.size nil vs set within non-nil storage - not equal",
			a:    &flowv1.FoundryGraphSpec{Storage: &flowv1.StorageSpec{}},
			b:    specWithStorage("1Gi"),
			want: false,
		},
		{
			name: "versioning.transactionTimeout nil vs set within non-nil versioning - not equal (R6 redeploy)",
			a:    &flowv1.FoundryGraphSpec{Versioning: &flowv1.VersioningSpec{}},
			b:    specWithVersioning(30),
			want: false,
		},
		{
			name: "versioning.remote nil vs set within non-nil versioning - not equal (R6 redeploy)",
			a:    &flowv1.FoundryGraphSpec{Versioning: &flowv1.VersioningSpec{}},
			b: &flowv1.FoundryGraphSpec{Versioning: &flowv1.VersioningSpec{
				Remote: &flowv1.RemoteConfig{URL: "https://github.com/org/repo.git"},
			}},
			want: false,
		},
		{
			name: "versioning.remote.auth nil vs set within non-nil remote - not equal (R6 redeploy/teardown)",
			a: &flowv1.FoundryGraphSpec{Versioning: &flowv1.VersioningSpec{
				Remote: &flowv1.RemoteConfig{},
			}},
			b: &flowv1.FoundryGraphSpec{Versioning: &flowv1.VersioningSpec{
				Remote: &flowv1.RemoteConfig{Auth: &flowv1.RemoteAuth{SecretRef: "secret-a"}},
			}},
			want: false,
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

// qPtr parses a Kubernetes resource.Quantity string to a *Quantity.
func qPtr(v string) *resource.Quantity {
	q := resource.MustParse(v)
	return &q
}

// specWithStorage returns a FoundryGraphSpec with storage.size set.
func specWithStorage(size string) *flowv1.FoundryGraphSpec {
	return &flowv1.FoundryGraphSpec{Storage: &flowv1.StorageSpec{Size: qPtr(size)}}
}

// specWithVersioning returns a FoundryGraphSpec with a TransactionTimeout in minutes.
func specWithVersioning(timeoutMinutes int) *flowv1.FoundryGraphSpec {
	return &flowv1.FoundryGraphSpec{
		Versioning: &flowv1.VersioningSpec{
			TransactionTimeout: &metav1.Duration{Duration: time.Duration(timeoutMinutes) * time.Minute},
		},
	}
}
