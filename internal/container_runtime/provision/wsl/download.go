package wsl

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

// downloadFile 把 url 下载到 dst，支持断点续传。
//
// 若存在 dst.part，会用 Range 请求续传；服务端不支持 Range 时重新下载。
// 下载完成后若提供了 checksum 则校验，失败则删除产物。
func downloadFile(ctx context.Context, client *http.Client, url, dst, checksum string, progress func(done, total int64)) error {
	if client == nil {
		client = &http.Client{Timeout: 0}
	}

	part := dst + ".part"
	if err := os.MkdirAll(dirOf(dst), 0o755); err != nil {
		return fmt.Errorf("create cache dir: %w", err)
	}

	// 已下载完成且校验通过：直接复用。
	if st, err := os.Stat(dst); err == nil && st.Size() > 0 {
		if checksum == "" || verifySHA256(dst, checksum) == nil {
			return nil
		}
		_ = os.Remove(dst)
	}

	var offset int64
	if st, err := os.Stat(part); err == nil {
		offset = st.Size()
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	if offset > 0 {
		req.Header.Set("Range", "bytes="+strconv.FormatInt(offset, 10)+"-")
	}

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("download %s: %w", url, err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
		// 服务端不支持续传，从头写入。
		offset = 0
	case http.StatusPartialContent:
		// 正常续传。
	default:
		return fmt.Errorf("download %s: unexpected status %s", url, resp.Status)
	}

	total := int64(-1)
	if resp.ContentLength > 0 {
		total = offset + resp.ContentLength
	}

	flag := os.O_CREATE | os.O_WRONLY
	if offset == 0 {
		flag |= os.O_TRUNC
	} else {
		flag |= os.O_APPEND
	}
	f, err := os.OpenFile(part, flag, 0o644)
	if err != nil {
		return fmt.Errorf("open %s: %w", part, err)
	}

	written := offset
	buf := make([]byte, 1<<20)
	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := f.Write(buf[:n]); werr != nil {
				f.Close()
				return fmt.Errorf("write %s: %w", part, werr)
			}
			written += int64(n)
			if progress != nil {
				progress(written, total)
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				break
			}
			f.Close()
			return fmt.Errorf("read body: %w", readErr)
		}
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close %s: %w", part, err)
	}

	if checksum != "" {
		if err := verifySHA256(part, checksum); err != nil {
			_ = os.Remove(part)
			return err
		}
	}

	if err := os.Rename(part, dst); err != nil {
		return fmt.Errorf("finalize %s: %w", dst, err)
	}
	return nil
}

// verifySHA256 校验文件摘要；checksum 形如 "abc123" 或 "abc123  filename"。
func verifySHA256(path, checksum string) error {
	want := strings.ToLower(strings.TrimSpace(checksum))
	// 兼容 "hash  filename" 形式。
	if i := strings.IndexAny(want, " \t"); i > 0 {
		want = want[:i]
	}
	if want == "" {
		return nil
	}

	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return fmt.Errorf("hash %s: %w", path, err)
	}
	got := hex.EncodeToString(h.Sum(nil))
	if got != want {
		return fmt.Errorf("checksum mismatch for %s: got %s, want %s", path, got, want)
	}
	return nil
}

// fetchChecksum 尝试从 "<url>.sha256" 获取期望的摘要。
//
// 拿不到时返回空串（不视为错误）：部分镜像源不提供该文件，此时跳过校验，
// 但 Install 阶段仍会因 tar 结构非法而失败，不会静默装入损坏数据。
func fetchChecksum(ctx context.Context, client *http.Client, url string) string {
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url+".sha256", nil)
	if err != nil {
		return ""
	}
	resp, err := client.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ""
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func dirOf(p string) string {
	if i := strings.LastIndexAny(p, `\/`); i > 0 {
		return p[:i]
	}
	return "."
}
