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
