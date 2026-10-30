package coder

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeTempFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

func TestCheckFileReadNotReadYet(t *testing.T) {
	path := writeTempFile(t, t.TempDir(), "a.txt", "hello\n")

	status := CheckFileRead(path, nil)
	if status.Read {
		t.Fatalf("Read = true, want false for a file never read in this process")
	}
	if !status.LastRead.IsZero() {
		t.Fatalf("LastRead = %s, want zero", status.LastRead)
	}
}

func TestCheckFileReadUnchanged(t *testing.T) {
	path := writeTempFile(t, t.TempDir(), "a.txt", "hello\n")
	recordFileRead(path)

	status := CheckFileRead(path, nil)
	if !status.Read {
		t.Fatalf("Read = false, want true")
	}
	if status.Modified {
		t.Fatalf("Modified = true (%s), want false", status.Detail)
	}
}

// NFS 的 mtime 由服务端盖章，可能"在未来"；内容没变就不该判为读后被改。
func TestCheckFileReadIgnoresFutureMtime(t *testing.T) {
	path := writeTempFile(t, t.TempDir(), "a.txt", "hello\n")
	recordFileRead(path)

	future := time.Now().Add(modTimeSkewTolerance + time.Minute)
	if err := os.Chtimes(path, future, future); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	status := CheckFileRead(path, nil)
	if status.Modified {
		t.Fatalf("Modified = true (%s), want false when content is identical", status.Detail)
	}
}

func TestCheckFileReadDetectsExternalModification(t *testing.T) {
	path := writeTempFile(t, t.TempDir(), "a.txt", "hello\n")
	recordFileRead(path)

	// 同样长度、不同内容：只有内容快照能发现。
	if err := os.WriteFile(path, []byte("world\n"), 0o644); err != nil {
		t.Fatalf("rewrite: %v", err)
	}

	status := CheckFileRead(path, nil)
	if !status.Modified {
		t.Fatalf("Modified = false (%s), want true", status.Detail)
	}
}

// 读软链、写真实路径时必须命中同一条记录。
func TestCheckFileReadResolvesSymlinkPath(t *testing.T) {
	dir := t.TempDir()
	realPath := writeTempFile(t, dir, "io_schema.json", "{\"outputs\":[]}\n")

	linkDir := filepath.Join(dir, "workspace")
	if err := os.MkdirAll(linkDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	linkPath := filepath.Join(linkDir, "io_schema.json")
	if err := os.Symlink(realPath, linkPath); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	recordFileRead(linkPath)

	status := CheckFileRead(realPath, nil)
	if !status.Read {
		t.Fatalf("Read = false, want true: symlink read should be visible via the real path")
	}
	if status.Modified {
		t.Fatalf("Modified = true (%s), want false", status.Detail)
	}

	// 反向：读真实路径、写软链路径。
	recordFileRead(realPath)
	if status := CheckFileRead(linkPath, nil); !status.Read || status.Modified {
		t.Fatalf("status = %+v, want read and unmodified via symlink path", status)
	}
}

func TestGuardFileModifyMessages(t *testing.T) {
	dir := t.TempDir()

	unread := writeTempFile(t, dir, "unread.txt", "x\n")
	msg, ok := guardFileModify(unread, nil)
	if ok || msg == "" {
		t.Fatalf("guardFileModify(unread) = (%q, %v), want a not-read message", msg, ok)
	}

	modified := writeTempFile(t, dir, "modified.txt", "x\n")
	recordFileRead(modified)
	if err := os.WriteFile(modified, []byte("y\n"), 0o644); err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	msg, ok = guardFileModify(modified, nil)
	if ok || msg == "" {
		t.Fatalf("guardFileModify(modified) = (%q, %v), want a modified message", msg, ok)
	}

	clean := writeTempFile(t, dir, "clean.txt", "x\n")
	recordFileRead(clean)
	if msg, ok = guardFileModify(clean, nil); !ok {
		t.Fatalf("guardFileModify(clean) = (%q, false), want ok", msg)
	}
}
