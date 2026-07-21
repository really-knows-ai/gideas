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
func collectExportData(s *CartographerServer, format string) ([]byte, error) {
	ctx := context.Background()
	var allEntities []store.Entity
	entityTypes, err := s.store.ListMainEntityTypes()
	if err != nil {
		return nil, err
	}
	for _, et := range entityTypes {
		entities, _, err := s.store.ListEntities(ctx, et, 1000, "", "")
		if err != nil {
			return nil, err
		}
		allEntities = append(allEntities, entities...)
	}
	// ponytail: Edge export is not yet implemented for the main DB.
	// The store interface does not expose a DumpAllEdges method for main.
	// Upgrade path: add DumpAllMainEdges to the store interface.
	var allEdges []store.Edge

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

// ponytail: GraphML serialisation uses encoding/xml indirectly via fmt.Sprintf.
// Upgrade path: use xml.MarshalIndent with a complete struct hierarchy.
func serializeGraphML(entities []store.Entity, _ []store.Edge) ([]byte, error) {
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
	buf.WriteString("  </graph>\n")
	buf.WriteString("</graphml>\n")
	return buf.Bytes(), nil
}
