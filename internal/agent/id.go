package agent

import (
	"strings"

	"github.com/google/uuid"
)

// newID 生成带前缀的唯一标识（如 task_xxx / perm_xxx）。
func newID(prefix string) string {
	return prefix + "_" + strings.ReplaceAll(uuid.NewString(), "-", "")
}
