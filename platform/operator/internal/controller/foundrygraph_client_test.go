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

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	flowv1gen "github.com/foundry/flow/gen/flow/v1"
	flowv1 "github.com/foundry/flow/operator/api/v1"
)

// testPropertyType is the only v1-supported property type ("string"); named so
// the mapper assertions do not trip the goconst linter.
const testPropertyType = "string"

// TestSchemaFromCRDPreservesAllFields covers the SPEC R2/R6 operator→proto schema mapping
// (foundrygraph_client.go): entity/edge types, property Type/Required, EnableVectorIndex,
// and ConnectionRule CanConnectTo/Using must be preserved verbatim into the proto Schema.
func TestSchemaFromCRDPreservesAllFields(t *testing.T) {
	spec := &flowv1.FoundryGraphSpec{
		EntityTypes: []flowv1.EntityTypeSpec{{
			Name:              "Person",
			EnableVectorIndex: true,
			Properties: []flowv1.PropertySpec{
				{Name: "name", Type: testPropertyType, Required: true},
				{Name: "nickname", Type: testPropertyType, Required: false},
			},
			Rules: []flowv1.ConnectionRule{{
				CanConnectTo: []string{"Company"},
				Using:        []string{"WORKS_AT"},
			}},
		}},
		EdgeTypes: []flowv1.EdgeTypeSpec{{
			Name: "WORKS_AT",
			Properties: []flowv1.PropertySpec{
				{Name: "since", Type: testPropertyType, Required: true},
			},
		}},
	}

	r := &FoundryGraphReconciler{}
	schema := r.schemaFromCRD(spec)
	if schema == nil {
		t.Fatal("expected a non-nil schema")
	}

	if len(schema.EntityTypes) != 1 {
		t.Fatalf("expected 1 entity type, got %d", len(schema.EntityTypes))
	}
	et := schema.EntityTypes[0]
	if et.Name != "Person" {
		t.Errorf("expected entity name Person, got %q", et.Name)
	}
	if !et.EnableVectorIndex {
		t.Error("expected EnableVectorIndex to be preserved")
	}
	if len(et.Properties) != 2 {
		t.Fatalf("expected 2 entity properties, got %d", len(et.Properties))
	}
	if et.Properties[0].Name != "name" || et.Properties[0].Type != testPropertyType || !et.Properties[0].Required {
		t.Errorf("expected required prop name/type/required preserved, got %+v", et.Properties[0])
	}
	if et.Properties[1].Required {
		t.Error("expected the optional property Required=false to be preserved")
	}
	if len(et.Rules) != 1 {
		t.Fatalf("expected 1 connection rule, got %d", len(et.Rules))
	}
	rule := et.Rules[0]
	if len(rule.CanConnectTo) != 1 || rule.CanConnectTo[0] != "Company" {
		t.Errorf("expected CanConnectTo=[Company], got %v", rule.CanConnectTo)
	}
	if len(rule.Using) != 1 || rule.Using[0] != "WORKS_AT" {
		t.Errorf("expected Using=[WORKS_AT], got %v", rule.Using)
	}

	if len(schema.EdgeTypes) != 1 {
		t.Fatalf("expected 1 edge type, got %d", len(schema.EdgeTypes))
	}
	edge := schema.EdgeTypes[0]
	if edge.Name != "WORKS_AT" {
		t.Errorf("expected edge name WORKS_AT, got %q", edge.Name)
	}
	if len(edge.Properties) != 1 {
		t.Fatalf("expected 1 edge property, got %d", len(edge.Properties))
	}
	if edge.Properties[0].Name != "since" || edge.Properties[0].Type != testPropertyType || !edge.Properties[0].Required {
		t.Errorf("expected edge prop preserved, got %+v", edge.Properties[0])
	}
}

// TestSchemaFromCRDNilSpec asserts a nil spec maps to an empty (non-nil) schema rather than
// panicking or returning nil.
func TestSchemaFromCRDNilSpec(t *testing.T) {
	r := &FoundryGraphReconciler{}
	schema := r.schemaFromCRD(nil)
	if schema == nil {
		t.Fatal("expected an empty (non-nil) schema for a nil spec")
	}
	if len(schema.EntityTypes) != 0 || len(schema.EdgeTypes) != 0 {
		t.Error("expected an empty schema for a nil spec")
	}
}

// TestApplySchemaPushesCompleteSchema drives the RPC push path (applySchema → ApplySchema)
// and asserts the schema payload actually sent to the Cartographer carries the R2 fields —
// the fields that the R6 diff gates on, pushed over the wire.
func TestApplySchemaPushesCompleteSchema(t *testing.T) {
	spec := &flowv1.FoundryGraphSpec{
		EntityTypes: []flowv1.EntityTypeSpec{{
			Name:              "Widget",
			EnableVectorIndex: true,
			Properties: []flowv1.PropertySpec{
				{Name: "sku", Type: testPropertyType, Required: true},
			},
			Rules: []flowv1.ConnectionRule{{
				CanConnectTo: []string{"Gadget"},
				Using:        []string{"MADE_OF"},
			}},
		}},
	}
	fg := &flowv1.FoundryGraph{
		ObjectMeta: metav1.ObjectMeta{Name: defaultGraphName, Namespace: testNS},
		Spec:       *spec.DeepCopy(),
	}

	var pushed *flowv1gen.Schema
	dialer := func(ctx context.Context, endpoint string) (CartographerClient, error) {
		return &mockCartographerClient{
			applySchemaFn: func(ctx context.Context, req *flowv1gen.ApplySchemaRequest) (*flowv1gen.ApplySchemaResponse, error) {
				pushed = req.Schema
				return &flowv1gen.ApplySchemaResponse{}, nil
			},
		}, nil
	}
	_ = flowv1.AddToScheme(scheme.Scheme)
	fakeCli := fake.NewClientBuilder().WithScheme(scheme.Scheme).WithObjects(fg).Build()
	r := &FoundryGraphReconciler{Client: fakeCli, CartographerDialer: dialer}

	if err := r.applySchema(context.Background(), fg, spec); err != nil {
		t.Fatalf("applySchema: %v", err)
	}
	if pushed == nil {
		t.Fatal("expected the schema to be pushed to the Cartographer")
	}
	if len(pushed.EntityTypes) != 1 || pushed.EntityTypes[0].Name != "Widget" || !pushed.EntityTypes[0].EnableVectorIndex {
		t.Errorf("expected pushed entity to preserve name and vector-index flag, got %+v", pushed.EntityTypes)
	}
	if len(pushed.EntityTypes[0].Properties) != 1 {
		t.Fatalf("expected 1 pushed property, got %d", len(pushed.EntityTypes[0].Properties))
	}
	prop := pushed.EntityTypes[0].Properties[0]
	if prop.Name != "sku" || prop.Type != testPropertyType || !prop.Required {
		t.Errorf("expected pushed property to preserve Type/Required, got %+v", prop)
	}
	if len(pushed.EntityTypes[0].Rules) != 1 || len(pushed.EntityTypes[0].Rules[0].CanConnectTo) != 1 || pushed.EntityTypes[0].Rules[0].CanConnectTo[0] != "Gadget" {
		t.Errorf("expected pushed rule to preserve canConnectTo, got %+v", pushed.EntityTypes[0].Rules)
	}
}
