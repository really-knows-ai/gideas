package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html"
	"maps"
	"sort"

	"github.com/foundry/flow/cartographer/internal/store"
)

const (
	ExportFormatJSON    = "json"
	ExportFormatGraphML = "graphml"
)

type graphNode struct {
	ID         string            `json:"id"`
	Type       string            `json:"type"`
	Properties map[string]string `json:"properties"`
}

type graphEdge struct {
	ID         string            `json:"id"`
	Type       string            `json:"type"`
	From       string            `json:"from"`
	To         string            `json:"to"`
	Properties map[string]string `json:"properties"`
}

type graphJSON struct {
	Nodes []graphNode `json:"nodes"`
	Edges []graphEdge `json:"edges"`
}

// collectExportData collects and serialises the full graph.
// acceptCtx is the optional gRPC context for cancellation; uses context.Background() if nil.
// ponytail: a nil acceptCtx silently loses cancellation/timing. The handler always passes a
// valid context today, so this is harmless. If that invariant ever changes, either require
// a non-nil context or derive one with a timeout before the fallback.
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
	case ExportFormatJSON:
		return serializeJSON(entities, edges)
	case ExportFormatGraphML:
		return serializeGraphML(entities, edges)
	default:
		return nil, errUnsupportedExportFormat(format)
	}
}

func serializeJSON(entities []store.Entity, edges []store.Edge) ([]byte, error) {
	g := graphJSON{Nodes: []graphNode{}, Edges: []graphEdge{}}
	for _, e := range entities {
		node := graphNode{ID: e.Id, Type: e.Type, Properties: map[string]string{}}
		maps.Copy(node.Properties, e.Properties)
		g.Nodes = append(g.Nodes, node)
	}
	for _, e := range edges {
		edge := graphEdge{ID: e.Id, Type: e.Type, From: e.FromEntityID, To: e.ToEntityID, Properties: map[string]string{}}
		maps.Copy(edge.Properties, e.Properties)
		g.Edges = append(g.Edges, edge)
	}
	return json.Marshal(g)
}

func serializeGraphML(entities []store.Entity, edges []store.Edge) ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	buf.WriteString(`<graphml xmlns="http://graphml.graphdrawing.org/xmlns">` + "\n")

	// Collect unique property keys used on nodes and edges.
	nodeKeys := map[string]struct{}{}
	edgeKeys := map[string]struct{}{}
	for _, e := range entities {
		for k := range e.Properties {
			nodeKeys[k] = struct{}{}
		}
	}
	for _, e := range edges {
		for k := range e.Properties {
			edgeKeys[k] = struct{}{}
		}
	}

	// Emit <key> declarations before <graph>. Sorted for deterministic output.
	nodeKeyList := make([]string, 0, len(nodeKeys))
	for k := range nodeKeys {
		nodeKeyList = append(nodeKeyList, k)
	}
	sort.Strings(nodeKeyList)
	for _, k := range nodeKeyList {
		fmt.Fprintf(&buf, `  <key id="%s" for="node" attr.name="%s" attr.type="string"/>`+"\n",
			html.EscapeString(k), html.EscapeString(k))
	}

	edgeKeyList := make([]string, 0, len(edgeKeys))
	for k := range edgeKeys {
		edgeKeyList = append(edgeKeyList, k)
	}
	sort.Strings(edgeKeyList)
	for _, k := range edgeKeyList {
		fmt.Fprintf(&buf, `  <key id="%s" for="edge" attr.name="%s" attr.type="string"/>`+"\n",
			html.EscapeString(k), html.EscapeString(k))
	}

	buf.WriteString(`  <graph id="G" edgedefault="directed">` + "\n")
	for _, e := range entities {
		fmt.Fprintf(&buf, `    <node id="%s">`, e.Id)
		for _, k := range sortedKeys(e.Properties) {
			v := e.Properties[k]
			fmt.Fprintf(&buf, `<data key="%s">%s</data>`, html.EscapeString(k), html.EscapeString(v))
		}
		buf.WriteString("</node>\n")
	}
	for _, e := range edges {
		fmt.Fprintf(&buf, `    <edge id="%s" source="%s" target="%s">`, e.Id, e.FromEntityID, e.ToEntityID)
		for _, k := range sortedKeys(e.Properties) {
			v := e.Properties[k]
			fmt.Fprintf(&buf, `<data key="%s">%s</data>`, html.EscapeString(k), html.EscapeString(v))
		}
		buf.WriteString("</edge>\n")
	}
	buf.WriteString("  </graph>\n")
	buf.WriteString("</graphml>\n")
	return buf.Bytes(), nil
}

// sortedKeys returns the map's keys in sorted order. The <key> declarations and
// every <data> element are emitted in this order, keeping GraphML serialisation
// deterministic (map-iteration order is randomised by the runtime).
func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
