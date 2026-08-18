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
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	flowv1 "github.com/foundry/flow/operator/api/v1"
)

// TestCapabilityPatternGraphFamily pins the SPEC R3 capability grammar: nodes
// must be able to declare the graph capability families the Sidecar attests
// (READ/WRITE:graph/entity/<type>, READ/WRITE:graph/entity/*, READ/WRITE:graph/tx).
func TestCapabilityPatternGraphFamily(t *testing.T) {
	valid := []string{
		"READ:graph/entity/Component",
		"READ:graph/entity/*",
		"WRITE:graph/entity/Component",
		"WRITE:graph/entity/*",
		"READ:graph/tx",
		"WRITE:graph/tx",
	}
	for _, cap := range valid {
		if !capabilityPattern.MatchString(cap) {
			t.Errorf("expected %q to match the capability grammar", cap)
		}
	}
}

// TestFindGraphNameSelectsProvisionedOwner pins the SPEC R1/R5 node wiring: the
// Sidecar's CARTOGRAPHER_ADDRESS must reference the FoundryGraph the Operator
// actually provisions — the namespace's earliest-created singleton owner, per
// enforceSingleton — not an arbitrary list item. The fake client lists by name,
// so a conflict named to sort before the owner lands at Items[0]; selecting it
// would point CARTOGRAPHER_ADDRESS at a Service the Operator never creates (a
// second FoundryGraph is a conflict and is never provisioned, SPEC R1).
func TestFindGraphNameSelectsProvisionedOwner(t *testing.T) {
	s := scheme.Scheme
	_ = flowv1.AddToScheme(s)

	const ns = "graph-owner-test"
	node := &flowv1.FoundryNode{ObjectMeta: metav1.ObjectMeta{Name: "test-node", Namespace: ns}}

	owner := &flowv1.FoundryGraph{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "flow-graph",
			Namespace:         ns,
			CreationTimestamp: metav1.NewTime(time.Now().Add(-time.Hour)),
		},
	}
	conflict := &flowv1.FoundryGraph{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "a-conflict-graph", // sorts before the owner in list order
			Namespace:         ns,
			CreationTimestamp: metav1.NewTime(time.Now()),
		},
	}

	fakeCli := fake.NewClientBuilder().WithScheme(s).WithObjects(owner, conflict).Build()
	r := &FoundryNodeReconciler{Client: fakeCli, Scheme: s}

	if got := r.findGraphName(context.Background(), node); got != owner.Name {
		t.Fatalf("expected the earliest-created owner %q to be selected, got %q", owner.Name, got)
	}
}

// TestFindGraphNameEqualTimestampNameTiebreak covers the equal-CreationTimestamp
// name-tiebreak branch of findGraphName, mirroring enforceSingleton: when two
// FoundryGraphs share a creation timestamp, the lexicographically-earlier name is
// the owner.
func TestFindGraphNameEqualTimestampNameTiebreak(t *testing.T) {
	s := scheme.Scheme
	_ = flowv1.AddToScheme(s)

	const ns = "graph-tiebreak-test"
	node := &flowv1.FoundryNode{ObjectMeta: metav1.ObjectMeta{Name: "test-node", Namespace: ns}}

	ts := metav1.Now()
	a := &flowv1.FoundryGraph{ObjectMeta: metav1.ObjectMeta{Name: "a-graph", Namespace: ns, CreationTimestamp: ts}}
	z := &flowv1.FoundryGraph{ObjectMeta: metav1.ObjectMeta{Name: "z-graph", Namespace: ns, CreationTimestamp: ts}}

	fakeCli := fake.NewClientBuilder().WithScheme(s).WithObjects(a, z).Build()
	r := &FoundryNodeReconciler{Client: fakeCli, Scheme: s}

	if got := r.findGraphName(context.Background(), node); got != "a-graph" {
		t.Fatalf("expected the lexicographically-earlier name on equal timestamps, got %q", got)
	}
}
