package manager

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/biox-dev/gobrave/internal/config"
	containerruntime "github.com/biox-dev/gobrave/internal/container_runtime"
	"github.com/biox-dev/gobrave/internal/fsm"
	"github.com/biox-dev/gobrave/internal/types"
	"github.com/biox-dev/gobrave/internal/types/interfaces"
)

// ContainerLifecycle 收敛 ContainerManager 与 ContainerCreateWorker 共同依赖的
// 无状态基础设施，避免两者互相持有指针。
//
// 它只负责「实例状态迁移 + 运行时解析 + 运行 spec 组装」这三类地基层能力：
//   - 状态迁移：transition/TransitionAndEnqueue（FSM 校验 + 落库 + outbox）；
//   - 运行时解析：GetRuntime / GetRuntimeByInstance；
//   - spec 组装：ApplyRuntimeUser / ApplyDagNodeRuntimeSpec。
//
// 依赖方向被固定为单向：ContainerManager → ContainerCreateWorker → ContainerLifecycle，
// ContainerLifecycle 不依赖任何一方。ContainerManager 仍保留同名导出方法作为薄委托，
// 对外 API 保持不变。
type ContainerLifecycle struct {
	repo interfaces.ContainerRepository
	reg  *containerruntime.Registry
	cfg  *config.Config
}

// NewContainerLifecycle 构造共享生命周期基础设施。
func NewContainerLifecycle(
	repo interfaces.ContainerRepository,
	reg *containerruntime.Registry,
	cfg *config.Config,
) *ContainerLifecycle {
	return &ContainerLifecycle{repo: repo, reg: reg, cfg: cfg}
}

// Transition 是容器实例状态迁移的唯一实现：读取最新状态 → FSM 校验 →
// 落库 → （可选）写 outbox 事件。
//
// enforceConcurrency 控制是否把本次迁移计入创建并发额度；终端容器这类长期占用
// running 的实例不参与该额度（false）。enqueueOutbox 控制是否写 outbox 事件；
// 由调用方同步驱动的内部迁移不需要 outbox（false）。
func (l *ContainerLifecycle) Transition(ctx context.Context, inst *types.ContainerInstance, to types.ContainerStatus, eventType string, enforceConcurrency bool, enqueueOutbox bool) error {
	if inst == nil {
		return errors.New("container instance is nil")
	}

	return l.repo.WithTransaction(ctx, func(tx interfaces.ContainerRepository) error {
		latest, err := tx.GetContainerInstanceByID(ctx, inst.ID)
		if err != nil {
			return err
		}
		if enforceConcurrency && (to == types.ContainerCreating || to == types.ContainerStarting) {
			if max := l.MaxConcurrency(); max > 0 {
				active, err := tx.CountContainerInstanceByStatuses(ctx, concurrencyOccupiedStatuses())
				if err != nil {
					return err
				}
				if active >= int64(max) {
					return fmt.Errorf("%w: active=%d max=%d", errWorkerConcurrencyLimit, active, max)
				}
			}
		}

		if latest.Status == to {
			inst.Status = latest.Status
			return nil
		}

		f := &fsm.FSM{}
		if err := f.Transition(latest.Status, to); err != nil {
			return err
		}

		inst.Status = to
		if err := tx.UpdateContainerInstance(ctx, inst); err != nil {
			return err
		}

		if !enqueueOutbox {
			return nil
		}

		domainEvent := &types.ContainerEvent{
			ContainerInstanceID: inst.ID,
			Event:               eventType,
			Message:             string(to),
		}

		payload, err := json.Marshal(domainEvent)
		if err != nil {
			return err
		}

		return tx.CreateOutboxEvent(ctx, &types.OutboxEvent{
			Type:    eventType,
			Payload: payload,
			Status:  "pending",
		})
	})
}

// TransitionAndEnqueue 以「计入并发额度 + 写 outbox」的默认语义迁移实例状态。
func (l *ContainerLifecycle) TransitionAndEnqueue(ctx context.Context, inst *types.ContainerInstance, to types.ContainerStatus, eventType string) error {
	return l.Transition(ctx, inst, to, eventType, true, true)
}

// MaxConcurrency 返回创建并发额度（未配置时默认 3）。
func (l *ContainerLifecycle) MaxConcurrency() int {
	maxConcurrency := 3
	if l.cfg != nil && l.cfg.Container != nil && l.cfg.Container.CreateQueueMaxConcurrency > 0 {
		maxConcurrency = l.cfg.Container.CreateQueueMaxConcurrency
	}
	if maxConcurrency <= 0 {
		maxConcurrency = 3
	}
	return maxConcurrency
}

// GetRuntime 按当前配置解析默认运行时。
func (l *ContainerLifecycle) GetRuntime() (containerruntime.Runtime, error) {
	runtimeName := l.ResolveRuntimeName()
	if runtimeName == "" {
		return nil, errors.New("runtime name is required")
	}
	if l.reg == nil {
		return nil, errors.New("container runtime registry is not configured")
	}
	rt := l.reg.Get(runtimeName)
	if rt == nil {
		return nil, fmt.Errorf("runtime not found: %s", runtimeName)
	}
	return rt, nil
}

// ResolveRuntimeName 解析当前生效的运行时名称。
func (l *ContainerLifecycle) ResolveRuntimeName() string {
	if l == nil {
		return "docker"
	}
	if l.cfg != nil {
		resolved := config.ResolveContainerRuntime(l.cfg)
		if strings.TrimSpace(resolved) != "" {
			return resolved
		}
	}
	if l.reg != nil {
		runtimes := l.reg.List()
		if len(runtimes) == 1 {
			return runtimes[0].Name()
		}
	}
	return "docker"
}

// GetRuntimeByInstance 优先按实例的 RuntimeName 解析运行时，找不到时回退到默认运行时。
func (l *ContainerLifecycle) GetRuntimeByInstance(inst *types.ContainerInstance) (containerruntime.Runtime, error) {
	if inst == nil {
		return nil, errors.New("container instance is nil")
	}
	if l.reg != nil {
		for _, item := range l.reg.List() {
			if inst.RuntimeName == item.Name() {
				return item, nil
			}
		}
	}
	return l.GetRuntime()
}

// ApplyDagNodeRuntimeSpec 按 DAG 节点的约定覆盖容器启动命令（bash ./run.sh）与运行用户。
func (l *ContainerLifecycle) ApplyDagNodeRuntimeSpec(spec *types.ContainerSpec, resolveVars map[string]string) {
	l.applyTaskRuntimeSpec(spec, resolveVars)
}

// applyTaskRuntimeSpec 是任务型容器（DAG 节点）的启动约定：
// 统一入口为 bash ./run.sh、输出 tee 到 LOG_PATH，并按运行时映射运行用户。
func (l *ContainerLifecycle) applyTaskRuntimeSpec(spec *types.ContainerSpec, resolveVars map[string]string) {
	if spec == nil {
		return
	}

	logPath := strings.TrimSpace(resolveVars["LOG_PATH"])
	if logPath == "" {
		if envLogPath, ok := os.LookupEnv("LOG_PATH"); ok {
			logPath = strings.TrimSpace(envLogPath)
		}
	}
	applyDagNodeTaskSpec(spec, logPath)

	if strings.TrimSpace(spec.WorkDir) == "" {
		if workspacePath := strings.TrimSpace(resolveVars["WORKSPACE_PATH"]); workspacePath != "" {
			spec.WorkDir = workspacePath
		}
	}

	l.ApplyRuntimeUser(spec, resolveVars)
}

// ApplyRuntimeUser maps USERID/GROUPID to the runtime's user field. Docker uses
// uid:gid, while Kubernetes currently maps ContainerSpec.User to runAsUser.
//
// 以非 root 用户运行时，镜像里的 HOME（常见是 /root）对该用户不可写，
// 直接跑会失败（R / python / pip 都要写家目录）。因此这里等价于：
//
//	docker run --user "$UID:$GID" \
//	  -e HOME=/home/app \
//	  --tmpfs /home/app:rw,uid=$UID,gid=$GID,mode=700
//
// 即：User=uid[:gid]、HomeDir=/home/app、Tmpfs 挂一个属主为该用户的 700 内存盘。
// 如果调用方 / 模板已经显式设置了 HomeDir 或为同一路径配置了 tmpfs，则不再覆盖。
func (l *ContainerLifecycle) ApplyRuntimeUser(spec *types.ContainerSpec, resolveVars map[string]string) {
	if spec == nil {
		return
	}
	uid := strings.TrimSpace(resolveVars["USERID"])
	if uid == "" {
		return
	}
	gid := strings.TrimSpace(resolveVars["GROUPID"])
	switch strings.ToLower(strings.TrimSpace(l.ResolveRuntimeName())) {
	case "docker":
		if gid != "" {
			spec.User = uid + ":" + gid
		} else {
			spec.User = uid
		}
	case "k8s", "k3s", "kubernetes":
		spec.User = uid
	default:
		return
	}

	l.applyRuntimeHome(spec, uid, gid)
}

// applyRuntimeHome 为非 root 运行的容器准备可写的 HOME：
// 设置 spec.HomeDir，并把该路径挂成属主为用户的内存盘。
func (l *ContainerLifecycle) applyRuntimeHome(spec *types.ContainerSpec, uid, gid string) {
	if spec == nil {
		return
	}

	home := strings.TrimSpace(spec.HomeDir)
	if home == "" {
		home = defaultRuntimeHomeDir
		spec.HomeDir = home
	}

	for _, mount := range spec.Tmpfs {
		if strings.TrimSpace(mount.Target) == home {
			// 已显式配置同路径的内存盘，尊重调用方的 UID/GID/Mode。
			return
		}
	}

	spec.Tmpfs = append(spec.Tmpfs, types.ContainerTmpfs{
		Target: home,
		Mode:   "700",
		UID:    uid,
		GID:    gid,
	})
}
