package compiler

import (
	"fmt"
	"strings"
)

type TopologyStage struct{}

func (s *TopologyStage) Name() string { return "TopologyStage" }

func (s *TopologyStage) Run(ctx *CompileContext) error {
	ctx.Nodes = asMapSlice(ctx.Dag["nodes"])
	ctx.Edges = asMapSlice(ctx.Dag["edges"])

	ctx.NodeMap = map[string]map[string]any{}
	aliasToCanonical := map[string]string{}
	scriptAliasToCanonical := map[string]string{}
	scriptAliasConflict := map[string]struct{}{}
	for _, node := range ctx.Nodes {
		nid := nodeKey(node)
		if nid == "" {
			return fmt.Errorf("dag node id is required")
		}
		if _, exists := ctx.NodeMap[nid]; exists {
			return fmt.Errorf("duplicate dag node id: %s", nid)
		}
		ctx.NodeMap[nid] = node
		registerNodeAliases(aliasToCanonical, nid, node)

		scriptID := strings.TrimSpace(fmt.Sprintf("%v", firstNonNil(node["script_id"], "")))
		if scriptID != "" {
			if existing, ok := scriptAliasToCanonical[scriptID]; ok && existing != nid {
				scriptAliasConflict[scriptID] = struct{}{}
			} else {
				scriptAliasToCanonical[scriptID] = nid
			}
		}
	}
	for scriptID, canonical := range scriptAliasToCanonical {
		if _, conflict := scriptAliasConflict[scriptID]; conflict {
			continue
		}
		aliasToCanonical[scriptID] = canonical
	}

	ctx.Incoming = map[string][]map[string]any{}
	ctx.Outgoing = map[string][]map[string]any{}
	for _, edge := range ctx.Edges {
		src := canonicalNodeRef(aliasToCanonical, fmt.Sprintf("%v", firstNonNil(edge["source"], edge["source_node"], "")))
		dst := canonicalNodeRef(aliasToCanonical, fmt.Sprintf("%v", firstNonNil(edge["target"], edge["target_node"], "")))
		if src == "" || dst == "" {
			continue
		}
		edge["source"] = src
		edge["target"] = dst
		ctx.Incoming[dst] = append(ctx.Incoming[dst], edge)
		ctx.Outgoing[src] = append(ctx.Outgoing[src], edge)
	}

	ctx.Order = topology(ctx.Nodes, ctx.Edges)
	return nil
}

func registerNodeAliases(aliasToCanonical map[string]string, canonical string, node map[string]any) {
	for _, key := range []string{"id", "node_id", "name"} {
		val := strings.TrimSpace(fmt.Sprintf("%v", firstNonNil(node[key], "")))
		if val == "" {
			continue
		}
		aliasToCanonical[val] = canonical
	}
	aliasToCanonical[canonical] = canonical
}

func canonicalNodeRef(aliasToCanonical map[string]string, raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if canonical, ok := aliasToCanonical[raw]; ok {
		return canonical
	}
	return raw
}

func topology(nodes []map[string]any, edges []map[string]any) []string {
	nodeIDs := make([]string, 0, len(nodes))
	indegree := map[string]int{}
	graph := map[string][]string{}

	for _, node := range nodes {
		nid := nodeKey(node)
		nodeIDs = append(nodeIDs, nid)
		indegree[nid] = 0
	}

	for _, edge := range edges {
		src := fmt.Sprintf("%v", firstNonNil(edge["source"], ""))
		dst := fmt.Sprintf("%v", firstNonNil(edge["target"], ""))
		if _, ok := indegree[src]; !ok {
			continue
		}
		if _, ok := indegree[dst]; !ok {
			continue
		}
		graph[src] = append(graph[src], dst)
		indegree[dst]++
	}

	queue := make([]string, 0)
	for nid, degree := range indegree {
		if degree == 0 {
			queue = append(queue, nid)
		}
	}

	order := make([]string, 0, len(nodeIDs))
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		order = append(order, cur)
		for _, next := range graph[cur] {
			indegree[next]--
			if indegree[next] == 0 {
				queue = append(queue, next)
			}
		}
	}

	if len(order) != len(nodeIDs) {
		return nodeIDs
	}
	return order
}
