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

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	flowv1gen "github.com/foundry/flow/gen/flow/v1"
	flowv1 "github.com/foundry/flow/operator/api/v1"
)

// CartographerClient is the interface for calling Cartographer gRPC methods.
type CartographerClient interface {
	ApplySchema(ctx context.Context, in *flowv1gen.ApplySchemaRequest, opts ...grpc.CallOption) (*flowv1gen.ApplySchemaResponse, error)
	WipeGraph(ctx context.Context, in *flowv1gen.WipeGraphRequest, opts ...grpc.CallOption) (*flowv1gen.WipeGraphResponse, error)
	HealthCheck(ctx context.Context, in *flowv1gen.HealthCheckRequest, opts ...grpc.CallOption) (*flowv1gen.HealthCheckResponse, error)
	ExportGraph(ctx context.Context, in *flowv1gen.ExportGraphRequest, opts ...grpc.CallOption) (flowv1gen.CartographerService_ExportGraphClient, error)
	Close() error
}

// cartographerClient implements CartographerClient by wrapping a gRPC connection.
type cartographerClient struct {
	conn *grpc.ClientConn
	stub flowv1gen.CartographerServiceClient
}

func (c *cartographerClient) ApplySchema(ctx context.Context, in *flowv1gen.ApplySchemaRequest, opts ...grpc.CallOption) (*flowv1gen.ApplySchemaResponse, error) {
	return c.stub.ApplySchema(ctx, in, opts...)
}

func (c *cartographerClient) WipeGraph(ctx context.Context, in *flowv1gen.WipeGraphRequest, opts ...grpc.CallOption) (*flowv1gen.WipeGraphResponse, error) {
	return c.stub.WipeGraph(ctx, in, opts...)
}

func (c *cartographerClient) HealthCheck(ctx context.Context, in *flowv1gen.HealthCheckRequest, opts ...grpc.CallOption) (*flowv1gen.HealthCheckResponse, error) {
	return c.stub.HealthCheck(ctx, in, opts...)
}

func (c *cartographerClient) ExportGraph(ctx context.Context, in *flowv1gen.ExportGraphRequest, opts ...grpc.CallOption) (flowv1gen.CartographerService_ExportGraphClient, error) {
	return c.stub.ExportGraph(ctx, in, opts...)
}

func (c *cartographerClient) Close() error { return c.conn.Close() }

// DialCartographer dials the Cartographer gRPC endpoint and returns a CartographerClient.
func DialCartographer(ctx context.Context, addr string) (CartographerClient, error) {
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, err
	}
	return &cartographerClient{
		conn: conn,
		stub: flowv1gen.NewCartographerServiceClient(conn),
	}, nil
}

// schemaFromCRD converts FoundryGraphSpec fields to the proto Schema message.
func (r *FoundryGraphReconciler) schemaFromCRD(spec *flowv1.FoundryGraphSpec) *flowv1gen.Schema {
	if spec == nil {
		return &flowv1gen.Schema{}
	}
	entityTypes := make([]*flowv1gen.EntityType, len(spec.EntityTypes))
	for i, et := range spec.EntityTypes {
		props := make([]*flowv1gen.Property, len(et.Properties))
		for j, p := range et.Properties {
			props[j] = &flowv1gen.Property{
				Name:     p.Name,
				Type:     p.Type,
				Required: p.Required,
			}
		}
		rules := make([]*flowv1gen.ConnectionRule, len(et.Rules))
		for k, rule := range et.Rules {
			rules[k] = &flowv1gen.ConnectionRule{
				CanConnectTo: rule.CanConnectTo,
				Using:        rule.Using,
			}
		}
		entityTypes[i] = &flowv1gen.EntityType{
			Name:              et.Name,
			Properties:        props,
			EnableVectorIndex: et.EnableVectorIndex,
			Rules:             rules,
		}
	}
	edgeTypes := make([]*flowv1gen.EdgeType, len(spec.EdgeTypes))
	for i, et := range spec.EdgeTypes {
		props := make([]*flowv1gen.Property, len(et.Properties))
		for j, p := range et.Properties {
			props[j] = &flowv1gen.Property{
				Name:     p.Name,
				Type:     p.Type,
				Required: p.Required,
			}
		}
		edgeTypes[i] = &flowv1gen.EdgeType{
			Name:       et.Name,
			Properties: props,
		}
	}
	return &flowv1gen.Schema{
		EntityTypes: entityTypes,
		EdgeTypes:   edgeTypes,
	}
}
