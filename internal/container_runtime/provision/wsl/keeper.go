package wsl

import (
	"context"
	"encoding/json"
	"os"
	"time"

	"github.com/biox-dev/gobrave/internal/logger"
)

// keeperStartSlop 是「记录时间」与「真实创建时间」之间允许的偏差。
//
// 记录写在 cmd.Start() 之后，两者相差毫秒级；留出余量只是为了容错。
const keeperStartSlop = 30 * time.Second

// keeper 是一个常驻会话（`sh -lc "sleep infinity"` 所在的 wsl.exe）的身份。
//
// 单看 pid 不够：pid 会被系统复用，退出后被别的进程顶上就会把无关进程
// 误认成自己的常驻会话，因此连创建时间一起记，做二次校验。
type keeper struct {
	// PID 是 wsl.exe 的进程 id。
	PID int `json:"pid"`
	// Detach 表示该会话是否脱离 gobrave（true 时 gobrave 退出后它继续运行）。
	Detach bool `json:"detach"`
	// Owner 是拉起它的 gobrave 进程 id。
	Owner int `json:"owner"`
	// StartedAt 是拉起它的本地时间，用于识别 pid 复用。
	StartedAt time.Time `json:"started_at"`
}

// alive 报告该常驻会话是否仍在运行。
//
// 判断在 pid 之外还核对创建时间；平台无法提供创建时间时按「不可确认」处理，
// 宁可可重复拉起一个会话，也不把无关进程当成自己的。
func (k *keeper) alive() bool {
	if k == nil || k.PID <= 0 || !processAlive(k.PID) {
		return false
	}
	if k.StartedAt.IsZero() {
		return true
	}
	started, ok := processStartedAt(k.PID)
	if !ok {
		return false
	}
	return started.Sub(k.StartedAt).Abs() < keeperStartSlop
}

// readKeeper 读取持久化的常驻会话记录；文件不存在或损坏时返回 nil。
//
// 记录只在 detach 模式下跨进程有效：随 gobrave 退出的会话必然已经结束。
func (p *WSLProvisioner) readKeeper(ctx context.Context) *keeper {
	path := p.layout.KeeperPath()
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var k keeper
	if err := json.Unmarshal(data, &k); err != nil {
		logger.Warnf(ctx, "[wsl] read keeper record %s failed: %v", path, err)
		return nil
	}
	return &k
}

// writeKeeper 持久化常驻会话记录（尽力而为：写失败只影响下次启动能否复用）。
func (p *WSLProvisioner) writeKeeper(ctx context.Context, k *keeper) {
	path := p.layout.KeeperPath()
	data, err := json.Marshal(k)
	if err == nil {
		if err = os.MkdirAll(p.layout.Root(), 0o755); err == nil {
			if err = os.WriteFile(path, data, 0o644); err == nil {
				return
			}
		}
	}
	logger.Warnf(ctx, "[wsl] write keeper record %s failed: %v", path, err)
}
