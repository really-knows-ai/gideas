package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	"github.com/foundry/flow/cartographer/internal/store"
)

type graphNode struct {
	ID         string            `json:"id"`
	Type       string            `json:"type"`
	Properties map[string]string `json:"properties,omitempty"`
}

type graphEdge struct {
	ID         string            `json:"id"`
	Type       string            `json:"type"`
	From       string            `json:"from"`
	To         string            `json:"to"`
	Properties map[string]string `json:"properties,omitempty"`
}

type graphJSON struct {
	Nodes []graphNode `json:"nodes,omitempty"`
	Edges []graphEdge `json:"edges,omitempty"`
}

// collectExportData collects and serialises the full graph.
// acceptCtx is the optional gRPC context for cancellation; uses context.Background() if nil.
func collectExportData(s *CartographerServer, acceptCtx context.Context, format string) ([]byte, error) {
	ctx := acceptCtx
	if ctx == nil {
		ctx = context.Background()
	}
	var allEntities []store.Entity
	entityTypes, err := s.store.ListMainEntityTypes()
	if err != nil {
		return nil, mapStoreError(err)
	}
	for _, et := range entityTypes {
		pageToken := ""
		for {
			entities, nextToken, listErr := s.store.ListEntities(ctx, et, 1000, pageToken, "main")
			if listErr != nil {
				return nil, mapStoreError(listErr)
			}
			allEntities = append(allEntities, entities...)
			if nextToken == "" {
				break
			}
			pageToken = nextToken
		}
	}
	var allEdges []store.Edge
	edgeTypes := s.store.EdgeTypeNames()
	for _, et := range edgeTypes {
		edges, listErr := s.store.ListEdgesOfType(ctx, et, "main")
		if listErr != nil {
			return nil, mapStoreError(listErr)
		}
		allEdges = append(allEdges, edges...)
	}

	return serializeGraph(format, allEntities, allEdges)
}

// serializeGraph serialises entities and edges into the requested format.
func serializeGraph(format string, entities []store.Entity, edges []store.Edge) ([]byte, error) {
	switch format {
	case "json":
		return serializeJSON(entities, edges)
	case "graphml":
		return serializeGraphML(entities, edges)
	default:
		return nil, errUnsupportedExportFormat(format)
	}
}

func serializeJSON(entities []store.Entity, edges []store.Edge) ([]byte, error) {
	g := graphJSON{}
	for _, e := range entities {
		node := graphNode{ID: e.Id, Type: e.Type}
		if len(e.Properties) > 0 {
			props := make(map[string]string)
			for k, v := range e.Properties {
				if v != "" {
					props[k] = v
				}
			}
			if len(props) > 0 {
				node.Properties = props
			}
		}
		g.Nodes = append(g.Nodes, node)
	}
	for _, e := range edges {
		edge := graphEdge{ID: e.Id, Type: e.Type, From: e.FromEntityID, To: e.ToEntityID}
		if len(e.Properties) > 0 {
			props := make(map[string]string)
			for k, v := range e.Properties {
				if v != "" {
					props[k] = v
				}
			}
			if len(props) > 0 {
				edge.Properties = props
			}
		}
		g.Edges = append(g.Edges, edge)
	}
	return json.Marshal(g)
}

func serializeGraphML(entities []store.Entity, edges []store.Edge) ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	buf.WriteString(`<graphml xmlns="http://graphml.graphdrawing.org/xmlns">` + "\n")
	buf.WriteString(`  <graph id="G" edgedefault="directed">` + "\n")
	for _, e := range entities {
		buf.WriteString(fmt.Sprintf(`    <node id="%s">`, e.Id))
		for k, v := range e.Properties {
			if v != "" {
				buf.WriteString(fmt.Sprintf(`<data key="%s">%s</data>`, k, v))
			}
		}
		buf.WriteString("</node>\n")
	}
	for _, e := range edges {
		buf.WriteString(fmt.Sprintf(`    <edge id="%s" source="%s" target="%s">`, e.Id, e.FromEntityID, e.ToEntityID))
		for k, v := range e.Properties {
			if v != "" {
				buf.WriteString(fmt.Sprintf(`<data key="%s">%s</data>`, k, v))
			}
		}
		buf.WriteString("</edge>\n")
	}
	buf.WriteString("  </graph>\n")
	buf.WriteString("</graphml>\n")
	return buf.Bytes(), nil
}
