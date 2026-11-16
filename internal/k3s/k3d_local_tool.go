package k3s

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// 本地平台支持情况从官方固定版本的发布清单读取，摘要信任官方 GitHub API 元数据。
// 不把镜像下载站与其自行提供的校验值作为信任来源。
type localK3dTool struct {
	mu                          sync.Mutex
	resolveMu                   sync.Mutex
	path, url, hash             string
	cache, platform, releaseURL string
	client                      *http.Client
}

func newLocalK3dTool() *localK3dTool {
	cache, _ := os.UserCacheDir()
	return &localK3dTool{cache: cache, platform: runtime.GOOS + "/" + runtime.GOARCH, releaseURL: "https://api.github.com/repos/k3d-io/k3d/releases/tags/" + k3dVersion,
		client: &http.Client{Timeout: 3 * time.Minute, CheckRedirect: func(request *http.Request, via []*http.Request) error {
			if request.URL.Scheme != "https" || len(via) > 5 {
				return errors.New("官方工具下载重定向无效")
			}
			return nil
		}}}
}
func (t *localK3dTool) resolve(ctx context.Context) error {
	t.resolveMu.Lock()
	defer t.resolveMu.Unlock()
	if t.path != "" && t.url != "" && t.hash != "" {
		return nil
	}
	if t.cache == "" {
		return &ValidationError{Message: "当前运行环境没有可写的应用缓存目录"}
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, t.releaseURL, nil)
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	response, err := t.client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("官方 k3d 发布清单读取失败，HTTP %d", response.StatusCode)
	}
	var release struct {
		Assets []struct {
			Name   string `json:"name"`
			URL    string `json:"browser_download_url"`
			Digest string `json:"digest"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 2*1024*1024)).Decode(&release); err != nil {
		return err
	}
	name := "k3d-" + strings.ReplaceAll(t.platform, "/", "-")
	if strings.HasPrefix(t.platform, "windows/") {
		name += ".exe"
	}
	for _, asset := range release.Assets {
		if asset.Name != name {
			continue
		}
		expected := "https://github.com/k3d-io/k3d/releases/download/" + k3dVersion + "/" + name
		hash, ok := strings.CutPrefix(asset.Digest, "sha256:")
		decoded, err := hex.DecodeString(hash)
		if asset.URL != expected || !ok || err != nil || len(decoded) != sha256.Size {
			return &ValidationError{Message: "官方 k3d 发布资产缺少有效摘要，未下载工具"}
		}
		t.path, t.url, t.hash = filepath.Join(t.cache, "edo", "k3d", k3dVersion, name), asset.URL, strings.ToLower(hash)
		return nil
	}
	return &ValidationError{Message: "官方 k3d 当前版本没有适用于此运行平台的安装工具"}
}
func (t *localK3dTool) valid() (bool, error) {
	if t == nil || t.path == "" {
		return false, &ValidationError{Message: "当前系统平台没有受支持的官方 k3d 工具"}
	}
	info, err := os.Lstat(t.path)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !info.Mode().IsRegular() || info.Size() > 128*1024*1024 || runtime.GOOS != "windows" && info.Mode()&0111 == 0 {
		return false, ErrRemote
	}
	file, err := os.Open(t.path)
	if err != nil {
		return false, err
	}
	defer file.Close()
	digest := sha256.New()
	if _, err = io.Copy(digest, file); err != nil {
		return false, err
	}
	if hex.EncodeToString(digest.Sum(nil)) != t.hash {
		return false, ErrRemote
	}
	return true, nil
}
func (t *localK3dTool) check(ctx context.Context) (string, error) {
	if err := t.resolve(ctx); err != nil {
		slog.ErrorContext(ctx, "检测官方 k3d 平台支持失败", "operation", "k3d_local_tool_assets", "err", err)
		safe := newInstallationLogRedactor(nil).line(err.Error())
		return safe + "\n", err
	}
	if valid, err := t.valid(); valid || err != nil {
		if err != nil {
			slog.ErrorContext(ctx, "核验本地官方 k3d 工具失败", "operation", "k3d_local_tool_verify", "err", err)
			return "官方 k3d 工具文件核验失败，未覆盖已有文件。\n", err
		}
		return "官方 k3d 工具 SHA-256 校验通过。\n", nil
	}
	ctx, cancel := context.WithTimeout(ctx, 35*time.Second)
	defer cancel()
	var logs strings.Builder
	redactor := newInstallationLogRedactor(nil)
	for attempt := 0; attempt < 2; attempt++ {
		probeCtx, stop := context.WithTimeout(ctx, 15*time.Second)
		request, err := http.NewRequestWithContext(probeCtx, http.MethodHead, t.url, nil)
		if err != nil {
			stop()
			return "", err
		}
		fmt.Fprintf(&logs, "HEAD %s\n", t.url)
		response, err := t.client.Do(request)
		if err == nil {
			response.Body.Close()
			stop()
			fmt.Fprintf(&logs, "HTTP %d\n", response.StatusCode)
			if response.StatusCode >= 200 && response.StatusCode < 300 {
				return logs.String(), nil
			}
			return logs.String(), ErrRemote
		}
		stop()
		slog.WarnContext(ctx, "检测本地 k3d 官方下载源失败", "operation", "k3d_local_tool_head", "attempt", attempt+1, "err", err)
		logs.WriteString(redactor.line(err.Error()) + "\n")
		if ctx.Err() != nil {
			return logs.String(), ctx.Err()
		}
	}
	return logs.String(), ErrRemote
}
func (t *localK3dTool) prepare(ctx context.Context, out io.Writer) error {
	if err := t.resolve(ctx); err != nil {
		slog.ErrorContext(ctx, "读取官方 k3d 支持平台失败", "operation", "k3d_local_tool_resolve", "err", err)
		return ErrRemote
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if valid, err := t.valid(); valid || err != nil {
		if err != nil {
			slog.ErrorContext(ctx, "核验本地 k3d 缓存失败", "operation", "k3d_local_tool_prepare", "err", err)
		}
		return err
	}
	directory := filepath.Dir(t.path)
	root := filepath.Dir(filepath.Dir(directory))
	if err := os.MkdirAll(filepath.Dir(root), 0700); err != nil {
		return err
	}
	for _, current := range []string{root, filepath.Join(root, "k3d"), directory} {
		if err := os.Mkdir(current, 0700); err != nil && !os.IsExist(err) {
			return err
		}
		info, err := os.Lstat(current)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return ErrRemote
		}
	}
	file, err := os.CreateTemp(directory, ".download-*")
	if err != nil {
		return err
	}
	defer func() { file.Close(); os.Remove(file.Name()) }()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, t.url, nil)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "GET %s\n", t.url)
	response, err := t.client.Do(request)
	if err != nil {
		slog.ErrorContext(ctx, "下载本地官方 k3d 工具失败", "operation", "k3d_local_tool_download", "err", err)
		return ErrRemote
	}
	defer response.Body.Close()
	fmt.Fprintf(out, "HTTP %d\n", response.StatusCode)
	if response.StatusCode != http.StatusOK {
		return ErrRemote
	}
	digest := sha256.New()
	size, err := io.Copy(io.MultiWriter(file, digest), io.LimitReader(response.Body, 128*1024*1024+1))
	if err != nil || size > 128*1024*1024 || hex.EncodeToString(digest.Sum(nil)) != t.hash {
		slog.ErrorContext(ctx, "本地 k3d 工具下载或摘要校验失败", "operation", "k3d_local_tool_checksum", "bytes", size, "err", err)
		return ErrRemote
	}
	if err := file.Chmod(0755); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	// 仅发布已校验文件；不覆盖同名文件，也不在失败后删除已有内容。
	if err := os.Link(file.Name(), t.path); err != nil {
		if valid, verifyErr := t.valid(); valid && verifyErr == nil {
			return nil
		}
		return err
	}
	fmt.Fprintf(out, "Downloaded %d bytes; SHA-256 verified\n", size)
	return nil
}
