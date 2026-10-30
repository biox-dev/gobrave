package coder

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const (
	// maxSnapshotSize 是读取时做内容快照（sha256）的最大文件大小；超过后退化为
	// size + mtime 判断，避免对大文件反复做全量哈希。
	maxSnapshotSize = 8 << 20
	// modTimeSkewTolerance 是退化到 mtime 比较时容忍的时钟偏差。
	//
	// /data2 等 NFS 挂载的 mtime 由服务端盖章，实测比本机时钟快 ~145s，
	// 直接用 mtime.After(readTime) 会把"刚读过的文件"误判为"读后被改"。
	// 内容快照可用时不会走到这个分支。
	modTimeSkewTolerance = 5 * time.Minute
)

// fileRecord 记录文件最后一次被读取 / 写入的状态。
//
// readSize / readHash / readMod 是"读取那一刻"的文件快照，用于判断读后是否被
// 外部进程（用户、其他服务、脚本）改动：内容级比较不受 NFS mtime 时钟偏差影响。
type fileRecord struct {
	path      string
	readTime  time.Time
	writeTime time.Time

	readSize int64
	readHash string // 为空表示内容快照不可用（文件过大或读取失败）
	readMod  time.Time
}

var (
	fileRecords     = make(map[string]fileRecord)
	fileRecordMutex sync.RWMutex
)

// normalizeFilePath 把路径归一化为记录用的 key：绝对路径 + Clean + 解析软链。
//
// 同一文件可能以多种写法出现（相对路径、节点工作目录里的软链
// io_schema.json -> 脚本目录真实文件），不归一化就会出现"读过却查不到记录"。
func normalizeFilePath(path string) string {
	if path == "" {
		return ""
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	abs = filepath.Clean(abs)

	// 文件存在（含软链）时解析到真实路径。
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return resolved
	}
	// 新建文件时自身还不存在，至少把父目录的软链解析掉。
	if dir, err := filepath.EvalSymlinks(filepath.Dir(abs)); err == nil {
		return filepath.Join(dir, filepath.Base(abs))
	}
	return abs
}

// snapshotFile 返回文件当前的内容快照。hash 为空表示快照不可用。
func snapshotFile(path string) (size int64, hash string, mod time.Time) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, "", time.Time{}
	}
	mod = info.ModTime()
	size = info.Size()
	if size > maxSnapshotSize {
		return size, "", mod
	}
	file, err := os.Open(path)
	if err != nil {
		return size, "", mod
	}
	defer file.Close()

	hasher := sha256.New()
	if _, err = io.Copy(hasher, file); err != nil {
		return size, "", mod
	}
	return size, hex.EncodeToString(hasher.Sum(nil)), mod
}

func recordFileRead(path string) {
	key := normalizeFilePath(path)
	size, hash, mod := snapshotFile(key)

	fileRecordMutex.Lock()
	defer fileRecordMutex.Unlock()

	record, exists := fileRecords[key]
	if !exists {
		record = fileRecord{path: key}
	}
	record.readTime = time.Now()
	record.readSize = size
	record.readHash = hash
	record.readMod = mod
	fileRecords[key] = record
}

func getLastReadTime(path string) time.Time {
	fileRecordMutex.RLock()
	defer fileRecordMutex.RUnlock()

	record, exists := fileRecords[normalizeFilePath(path)]
	if !exists {
		return time.Time{}
	}
	return record.readTime
}

func recordFileWrite(path string) {
	key := normalizeFilePath(path)

	fileRecordMutex.Lock()
	defer fileRecordMutex.Unlock()

	record, exists := fileRecords[key]
	if !exists {
		record = fileRecord{path: key}
	}
	record.writeTime = time.Now()
	fileRecords[key] = record
}

// FileReadStatus 描述文件相对"最后一次读取"的状态。
type FileReadStatus struct {
	// Read 表示本进程内是否读取过该文件（进程重启后为 false）。
	Read bool
	// Modified 表示读取之后文件内容被外部改动过。
	Modified bool
	// LastRead 是最后一次读取时间（未读过时为零值）。
	LastRead time.Time
	// ModTime 是文件当前的修改时间（mtime 可能因 NFS 服务端时钟而超前）。
	ModTime time.Time
	// Detail 是判定细节，便于日志 / 排障。
	Detail string
}

// CheckFileRead 判断 path 是否已读过且读后未被改动。
//
// info 为调用方已有的 os.FileInfo，可为 nil（内部自行 Stat）。
// 判定优先使用内容快照（size + sha256），因此不受 NFS mtime 时钟偏差影响；
// 快照不可用（文件过大 / 读取失败）时退化为 size + 带容忍窗口的 mtime 比较。
func CheckFileRead(path string, info os.FileInfo) FileReadStatus {
	status := FileReadStatus{}
	if info != nil {
		status.ModTime = info.ModTime()
	}

	key := normalizeFilePath(path)
	fileRecordMutex.RLock()
	record, exists := fileRecords[key]
	fileRecordMutex.RUnlock()

	if !exists {
		status.Detail = "no read record in this process"
		return status
	}
	status.Read = true
	status.LastRead = record.readTime

	if info == nil {
		var err error
		if info, err = os.Stat(key); err != nil {
			// 文件已消失：交给调用方按"文件不存在"处理。
			status.Detail = fmt.Sprintf("stat failed: %v", err)
			return status
		}
		status.ModTime = info.ModTime()
	}

	// 1) 内容快照可用时按内容判断：mtime 变化不算改动。
	if record.readHash != "" {
		size, hash, _ := snapshotFile(key)
		if hash != "" {
			if size == record.readSize && hash == record.readHash {
				status.Detail = "content unchanged (sha256 match)"
				return status
			}
			status.Modified = true
			status.Detail = fmt.Sprintf("content changed: size %d->%d", record.readSize, size)
			return status
		}
		// 当前快照不可用，退化到下面的 size / mtime 判断。
	}

	// 2) 退化判断：size 不同直接判改动。
	if info.Size() != record.readSize {
		status.Modified = true
		status.Detail = fmt.Sprintf("size changed: %d->%d", record.readSize, info.Size())
		return status
	}
	// 3) 同 size 时用 mtime，但要扣掉已知时钟偏差。
	if info.ModTime().After(record.readTime.Add(modTimeSkewTolerance)) {
		status.Modified = true
		status.Detail = fmt.Sprintf("mtime %s is beyond read time + tolerance %s",
			info.ModTime().Format(time.RFC3339), modTimeSkewTolerance)
		return status
	}
	status.Detail = "size unchanged, mtime within tolerance"
	return status
}

// guardFileModify 检查 path 是否可以被修改。
//
// 返回 (空字符串, true) 表示可以继续；否则返回给模型看的提示。
// 区分"从未读过"与"读后被改"，避免零值时间的 0001-01-01 误导。
func guardFileModify(path string, info os.FileInfo) (string, bool) {
	status := CheckFileRead(path, info)
	if !status.Read {
		return fmt.Sprintf("you must read the file %s before modifying it. Use the View tool first", path), false
	}
	if status.Modified {
		return fmt.Sprintf("file %s has been modified since it was last read (mod time: %s, last read: %s)",
			path, status.ModTime.Format(time.RFC3339), status.LastRead.Format(time.RFC3339)), false
	}
	return "", true
}
