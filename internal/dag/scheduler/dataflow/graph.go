package dataflow

import (
	"context"
	"strings"

	"github.com/biox-dev/gobrave/internal/logger"
	"github.com/biox-dev/gobrave/internal/types/interfaces"
)

// prepareAnalysisByCacheTypeV3 handles cache-type driven pre-run behavior.
//
// The decision is delegated to the shared cache policy in internal/dag, which is
// also what the dynamic scheduler uses:
//   - CacheTypeRerunAll: clear the persisted runtime graph and rebuild from scratch.
//   - Every reuse policy: keep the persisted runtime graph; the per-node decision
//     (script / params fingerprints) is deferred to node reuse time.
func (o *dataflowDagOrchestratorV3) prepareAnalysisByCacheTypeV3(ctx context.Context, analysisID int64) error {
	if analysisID <= 0 {
		return nil
	}

	analysis, err := o.repo.GetAnalysisByID(ctx, analysisID)
	if err != nil {
		return err
	}
	if analysis == nil {
		return nil
	}

	// With no materialized node in hand only rerun_all reports a reset, so reuse
	// cache types keep whatever the previous run persisted.
	if !o.cachePolicies.ShouldResetGraph(analysis.CacheType) {
		logger.Infof(ctx,
			"[DataflowDagOrchestratorV3] cache_type %s, keep persisted graph, analysis_id=%d",
			o.cachePolicies.Resolve(analysis.CacheType).Name(),
			analysisID,
		)
		return nil
	}

	if err := o.repo.WithTransaction(ctx, func(tx interfaces.AnalysisRepository) error {
		if err := tx.DeleteAnalysisNodesByAnalysisID(ctx, analysisID); err != nil {
			return err
		}
		if err := tx.DeleteAnalysisEdgesByAnalysisID(ctx, analysisID); err != nil {
			return err
		}
		return nil
	}); err != nil {
		return err
	}
	logger.Infof(ctx,
		"[DataflowDagOrchestratorV3] cache_type rerun_all, cleared persisted graph, analysis_id=%d",
		analysisID,
	)

	return nil
}

func (o *dataflowDagOrchestratorV3) buildGraphSpec(analysisID int64, dagDefinition map[string]any) DataflowGraphSpec {
	spec := DataflowGraphSpec{AnalysisID: analysisID}

	edges := dynamicToMapSlice(dagDefinition["edges"])
	for _, edge := range edges {
		channelID := dataflowChannelID(
			strings.TrimSpace(dynamicToString(edge["source"])),
			strings.TrimSpace(dynamicToString(edge["sourceHandle"])),
			strings.TrimSpace(dynamicToString(edge["target"])),
			strings.TrimSpace(dynamicToString(edge["targetHandle"])),
		)
		spec.Channels = append(spec.Channels, DataflowChannelSpec{
			ChannelID:  channelID,
			FromNodeID: strings.TrimSpace(dynamicToString(edge["source"])),
			ToNodeID:   strings.TrimSpace(dynamicToString(edge["target"])),
			FromPort:   strings.TrimSpace(dynamicToString(edge["sourceHandle"])),
			ToPort:     strings.TrimSpace(dynamicToString(edge["targetHandle"])),
		})
	}

	nodeByID := map[string]*DataflowProcessSpec{}
	nodes := dynamicToMapSlice(dagDefinition["nodes"])
	for _, node := range nodes {
		nodeID := strings.TrimSpace(dynamicToString(node["id"]))
		if nodeID == "" {
			continue
		}
		opType, scatterField, scatterMode, gatherField, gatherMode := resolveDataflowOperatorConfig(node)
		inputKeys := extractNodeInputKeys(node)
		if _, exists := nodeByID[nodeID]; !exists {
			nodeName := strings.TrimSpace(dynamicToString(resolveNodeField(node, "node_name")))
			if nodeName == "" {
				nodeName = strings.TrimSpace(dynamicToString(resolveNodeField(node, "name")))
			}
			nodeByID[nodeID] = &DataflowProcessSpec{
				NodeID:       nodeID,
				NodeName:     nodeName,
				SampleID:     strings.TrimSpace(dynamicToString(resolveNodeField(node, "sample_id"))),
				ScriptID:     strings.TrimSpace(dynamicToString(resolveNodeField(node, "script_id"))),
				InputKeys:    inputKeys,
				Inputs:       dynamicToMap(resolveNodeField(node, "inputs")),
				Outputs:      dynamicToMap(resolveNodeField(node, "outputs")),
				Params:       dynamicToMap(resolveNodeField(node, "params")),
				ResolvedIn:   dynamicToMap(resolveNodeField(node, "resolved_inputs")),
				ResolvedOut:  dynamicToMap(resolveNodeField(node, "resolved_outputs")),
				Executor:     strings.TrimSpace(dynamicToString(resolveNodeField(node, "executor"))),
				Retry:        dynamicIntFromAny(resolveNodeField(node, "retry"), 0),
				MaxRetry:     dynamicIntFromAny(resolveNodeField(node, "max_retry"), 3),
				RerunReason:  strings.TrimSpace(dynamicToString(resolveNodeField(node, "rerun_reason"))),
				OperatorType: opType,
				ScatterField: scatterField,
				ScatterMode:  scatterMode,
				GatherField:  gatherField,
				GatherMode:   gatherMode,
			}
		}
	}
	for _, ch := range spec.Channels {
		if ch.FromNodeID == "" || ch.ToNodeID == "" {
			continue
		}
		up, ok := nodeByID[ch.ToNodeID]
		if ok {
			up.UpstreamIDs = appendUniqueString(up.UpstreamIDs, ch.FromNodeID)
		}
		down, ok := nodeByID[ch.FromNodeID]
		if ok {
			down.Downstream = appendUniqueString(down.Downstream, ch.ToNodeID)
		}
	}
	for _, proc := range nodeByID {
		spec.Processes = append(spec.Processes, *proc)
	}
	return spec
}

func (o *dataflowDagOrchestratorV3) logFrameworkPhase(ctx context.Context, spec DataflowGraphSpec) {
	logger.Infof(ctx,
		"[DataflowDagOrchestratorV3] framework bootstrap, analysis_id=%d processes=%d channels=%d",
		spec.AnalysisID,
		len(spec.Processes),
		len(spec.Channels),
	)
}

func resolveDataflowOperatorConfig(node map[string]any) (operatorType string, scatterField string, scatterMode string, gatherField string, gatherMode string) {
	gather := dynamicToMap(node["gather"])
	gatherMode = strings.ToLower(strings.TrimSpace(dynamicToString(gather["mode"])))
	gatherField = strings.TrimSpace(dynamicToString(gather["field"]))
	if gatherMode == "list" && gatherField != "" {
		return string(dataflowOperatorTypeGather), "", "", gatherField, gatherMode
	}

	scatter := dynamicToMap(node["scatter"])
	scatterMode = strings.ToLower(strings.TrimSpace(dynamicToString(scatter["mode"])))
	scatterField = strings.TrimSpace(dynamicToString(scatter["field"]))
	if (scatterMode == "each" || scatterMode == "list") && scatterField != "" {
		return string(dataflowOperatorTypeScatter), scatterField, scatterMode, "", ""
	}

	return string(dataflowOperatorTypeInput), "", "", "", ""
}
