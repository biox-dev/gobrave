package utils

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
)

// storePathNameBytes 是随机 store 目录标识的随机字节数：16 字节 → 32 位十六进制字符。
const storePathNameBytes = 16

// GenerateStorePathName 生成 store 目录的随机相对标识（types.Store.PathName）。
//
// store 目录名不再复用业务 ID（workflow_id / script_id / git 地址里的 "<owner>/<repo>"）：
// 业务 ID 在重新发布、复制安装时会变化甚至被多个组件复用，git 地址则会随仓库改名漂移，
// 还会把上游仓库信息暴露到本地路径里；而目录名一旦落盘（并作为裸仓库位置）就不该再变。
// 因此发布与下载统一在「首次创建 store」时调用本函数生成一次随机标识，之后的更新
// （重新发布 / 重新下载）只沿用既有值，绝不重新生成。
func GenerateStorePathName() (string, error) {
	buf := make([]byte, storePathNameBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate store path name: %w", err)
	}
	return hex.EncodeToString(buf), nil
}
