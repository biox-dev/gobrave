package manager

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// terminalScrollback 把会话的环形缓冲快照持久化到单个文件。
//
// 设计取舍：
//   - 每次落盘都写「当前环形缓冲的完整快照」（temp + rename），而不是逐块追加：
//     实现简单、文件永远是自洽的，且天然实现了「滚动覆盖」（只保留最后 limit 字节）。
//   - 快照由会话的 flushLoop 每 2s 触发一次（脏写才落盘），单个会话 ≤1MB/2s 的量级，
//     对本地磁盘可以接受；进程重启后通过 Load 回放进内存缓冲。
//   - 会话被显式关闭/回收时 Remove，避免目录无限增长。
type terminalScrollback struct {
	path  string
	limit int

	mu     sync.Mutex
	closed bool
}

func newTerminalScrollback(dir, sessionID string, limit int) (*terminalScrollback, error) {
	if dir == "" {
		return nil, nil
	}
	if limit <= 0 {
		limit = 1024 * 1024
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create terminal buffer dir %s: %w", dir, err)
	}
	return &terminalScrollback{
		path:  filepath.Join(dir, sessionID+".log"),
		limit: limit,
	}, nil
}

// Load 读取文件尾部（最多 limit 字节）；文件不存在返回 nil。
func (w *terminalScrollback) Load() []byte {
	if w == nil {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return nil
	}
	data, err := os.ReadFile(w.path)
	if err != nil || len(data) == 0 {
		return nil
	}
	if len(data) > w.limit {
		data = data[len(data)-w.limit:]
	}
	return append([]byte(nil), data...)
}

// Save 原子写入整份快照（超过 limit 时保留尾部）。
func (w *terminalScrollback) Save(data []byte) error {
	if w == nil {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return nil
	}
	if len(data) > w.limit {
		data = data[len(data)-w.limit:]
	}
	tmp := w.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("write terminal buffer %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, w.path); err != nil {
		return fmt.Errorf("replace terminal buffer %s: %w", w.path, err)
	}
	return nil
}

// Remove 删除缓冲文件（会话结束）。
func (w *terminalScrollback) Remove() {
	if w == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return
	}
	w.closed = true
	_ = os.Remove(w.path)
	_ = os.Remove(w.path + ".tmp")
}
