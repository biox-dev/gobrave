package dag

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	// "github.com/gobravedev/gobrave/internal/dag"
	// "github.com/gobravedev/gobrave/internal/dag"
	"github.com/gobravedev/gobrave/internal/event"
	"github.com/gobravedev/gobrave/internal/logger"
	"github.com/gobravedev/gobrave/internal/realtime"
	"github.com/gobravedev/gobrave/internal/types"
	"github.com/gobravedev/gobrave/internal/types/interfaces"
	"gorm.io/gorm"
)

// DagRuntimeEventNotifier subscribes DAG runtime events and pushes UI action messages
// to project users through the unified realtime hub.
type DagRuntimeEventNotifier struct {
	db           *gorm.DB
	analysisRepo interfaces.AnalysisRepository
	projectRepo  interfaces.ProjectRepository
	hub          *realtime.Hub
}

func NewDagRuntimeEventNotifier(db *gorm.DB, projectRepo interfaces.ProjectRepository, analysisRepo interfaces.AnalysisRepository, hub *realtime.Hub) *DagRuntimeEventNotifier {
	return &DagRuntimeEventNotifier{
		db:           db,
		analysisRepo: analysisRepo,
		projectRepo:  projectRepo,
		hub:          hub,
	}
}

func (n *DagRuntimeEventNotifier) Handle(evt event.Event) {
	runtimeEvent, ok := evt.(RuntimeEvent)
	if !ok {
		return
	}

	if !n.shouldNotify(runtimeEvent.Name) {
		return
	}

	ctx := context.Background()
	var projectID int64
	if runtimeEvent.AnalysisNodeID == 0 {
		analysis, err := n.analysisRepo.GetAnalysisByID(ctx, runtimeEvent.AnalysisID)
		if err != nil {
			return
		}
		projectID = analysis.ProjectID
	} else {
		analsyisNode, err := n.analysisRepo.GetAnalysisNodeByID(ctx, runtimeEvent.AnalysisNodeID)
		if err != nil {
			return
		}
		projectID = analsyisNode.ProjectID
		if projectID == 0 {
			analysis, err := n.analysisRepo.GetAnalysisByID(ctx, runtimeEvent.AnalysisID)
			if err != nil {
				logger.Warnf(ctx, "[Realtime] skip dag event notify: load analysis failed analysis_id=%d event=%s err=%v", runtimeEvent.AnalysisID, runtimeEvent.Name, err)
				return
			}
			projectID = analysis.ProjectID
		}
	}

	if projectID == 0 {
		return
	}
	project, err := n.projectRepo.GetProjectByID(context.Background(), projectID)
	if err != nil {
		return
	}
	userIDs, err := n.listProjectUserIDs(ctx, project.ProjectID)
	if err != nil {
		logger.Warnf(ctx, "[Realtime] skip dag event notify: load project users failed project_id=%d event=%s err=%v", projectID, runtimeEvent.Name, err)
		return
	}
	if len(userIDs) == 0 {
		return
	}

	msg, ok := n.buildRealtimeMessage(ctx, runtimeEvent)
	if !ok {
		return
	}

	for _, userID := range userIDs {
		if err := n.hub.PushMessage(userID, msg); err != nil {
			if strings.Contains(err.Error(), "no_client_for_user:") {
				continue
			}
			logger.Warnf(ctx, "[Realtime] push dag event failed user_id=%s analysis_id=%s event=%s err=%v", userID, runtimeEvent.AnalysisID, runtimeEvent.Name, err)
		}
	}
}

func (n *DagRuntimeEventNotifier) shouldNotify(eventName string) bool {
	switch eventName {
	case EventDagStarted,
		EventDagCompleted,
		EventDagFailed,
		EventNodeSubmitted,
		EventNodeRunning,
		EventNodeStateChange,
		EventNodeCompleted,
		EventNodeFailed:
		return true
	default:
		return false
	}
}

func (n *DagRuntimeEventNotifier) listProjectUserIDs(ctx context.Context, projectID string) ([]string, error) {
	rows := make([]string, 0)
	err := n.db.WithContext(ctx).
		Model(&types.UserProject{}).
		Distinct("user_id").
		Where("project_id = ?", projectID).
		Pluck("user_id", &rows).Error
	if err != nil {
		return nil, err
	}
	return rows, nil
}

func (n *DagRuntimeEventNotifier) buildRealtimeMessage(ctx context.Context, runtimeEvent RuntimeEvent) (map[string]any, bool) {
	message := map[string]any{
		"action": "component.invoke",
		"payload": map[string]any{
			"category": "analysis",
		},
	}
	payload := message["payload"].(map[string]any)

	switch runtimeEvent.Name {
	case EventDagStarted:
		payload["id"] = fmt.Sprint(runtimeEvent.AnalysisID)
		payload["method"] = "dagStarted"
		payload["args"] = map[string]any{"status": "running", "id": runtimeEvent.AnalysisID}
		return message, true
	case EventDagCompleted:
		payload["id"] = fmt.Sprint(runtimeEvent.AnalysisID)
		payload["method"] = "dagDone"
		payload["args"] = map[string]any{"status": "done", "id": runtimeEvent.AnalysisID}
		return message, true
	case EventDagFailed:
		payload["id"] = fmt.Sprint(runtimeEvent.AnalysisID)
		payload["method"] = "dagDone"
		payload["args"] = map[string]any{"status": "failed", "id": runtimeEvent.AnalysisID}
		return message, true
	case EventNodeSubmitted, EventNodeRunning, EventNodeStateChange, EventNodeCompleted, EventNodeFailed:
		analysisNodeID, err := n.resolveAnalysisNodeID(ctx, runtimeEvent)
		if err != nil {
			logger.Warnf(ctx, "[Realtime] resolve analysis node id failed analysis_id=%s node_id=%s event=%s err=%v", runtimeEvent.AnalysisID, runtimeEvent.NodeID, runtimeEvent.Name, err)
			return nil, false
		}

		status, method := n.resolveNodeRealtimeStatusAndMethod(runtimeEvent)

		payload["id"] = analysisNodeID
		payload["parentId"] = fmt.Sprint(runtimeEvent.AnalysisID)
		payload["method"] = method
		payload["args"] = map[string]any{"status": status, "id": analysisNodeID}
		return message, true
	default:
		return nil, false
	}
}

func (n *DagRuntimeEventNotifier) resolveAnalysisNodeID(ctx context.Context, runtimeEvent RuntimeEvent) (string, error) {
	if runtimeEvent.AnalysisNodeID > 0 {
		return strconv.FormatInt(runtimeEvent.AnalysisNodeID, 10), nil
	}
	return "", errors.New("AnalysisNodeID is required")
	// analysisID := runtimeEvent.AnalysisID
	// nodeID := runtimeEvent.NodeID
	// if analysisID == 0 || nodeID == "" {
	// 	return "", errors.New("analysis_id and node_id are required")
	// }
	// node, err := n.analysisRepo.GetAnalysisNodeByNodeID(ctx, analysisID, nodeID)
	// if err != nil {
	// 	return "", err
	// }
	// if node == nil || node.ID <= 0 {
	// 	return "", errors.New("analysis node id not found")
	// }
	// return strconv.FormatInt(node.ID, 10), nil
}

func (n *DagRuntimeEventNotifier) resolveNodeRealtimeStatusAndMethod(runtimeEvent RuntimeEvent) (string, string) {
	payloadStatus := strings.TrimSpace(strings.ToLower(n.payloadString(runtimeEvent, "status")))

	switch runtimeEvent.Name {
	case EventNodeSubmitted:
		if payloadStatus == "" {
			payloadStatus = StatusSubmitted
		}
		return payloadStatus, "analysisSubmitted"
	case EventNodeRunning:
		if payloadStatus == "" {
			payloadStatus = StatusRunning
		}
		return payloadStatus, "analysisStarted"
	case EventNodeFailed:
		if payloadStatus == "" {
			payloadStatus = StatusFailed
		}
		return payloadStatus, "analysisDone"
	case EventNodeCompleted:
		if payloadStatus == "" {
			payloadStatus = StatusDone
		}
		return payloadStatus, "analysisDone"
	case EventNodeStateChange:
		if payloadStatus == "" {
			payloadStatus = StatusRunning
		}
		switch payloadStatus {
		case StatusDone, StatusFailed, StatusStopped, StatusSkipped, StatusCached:
			return payloadStatus, "analysisDone"
		case StatusSubmitted:
			return payloadStatus, "analysisSubmitted"
		default:
			return payloadStatus, "analysisStarted"
		}
	default:
		return StatusRunning, "analysisStarted"
	}
}

func (n *DagRuntimeEventNotifier) analysisNodeIDFromPayload(runtimeEvent RuntimeEvent) string {
	return n.payloadString(runtimeEvent, "analysis_node_id")
}

func (n *DagRuntimeEventNotifier) payloadString(runtimeEvent RuntimeEvent, key string) string {
	if runtimeEvent.Payload == nil {
		return ""
	}
	raw, ok := runtimeEvent.Payload[key]
	if !ok {
		return ""
	}
	switch v := raw.(type) {
	case string:
		return v
	case int:
		return strconv.Itoa(v)
	case int8:
		return strconv.FormatInt(int64(v), 10)
	case int16:
		return strconv.FormatInt(int64(v), 10)
	case int32:
		return strconv.FormatInt(int64(v), 10)
	case int64:
		return strconv.FormatInt(v, 10)
	case uint:
		return strconv.FormatUint(uint64(v), 10)
	case uint8:
		return strconv.FormatUint(uint64(v), 10)
	case uint16:
		return strconv.FormatUint(uint64(v), 10)
	case uint32:
		return strconv.FormatUint(uint64(v), 10)
	case uint64:
		return strconv.FormatUint(v, 10)
	default:
		return ""
	}
}
