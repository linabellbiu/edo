package terminal

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"

	"edo/internal/terminalwire"
)

func helperFixtureBinary(architecture string) []byte {
	b := make([]byte, 128)
	copy(b, []byte{0x7f, 'E', 'L', 'F', 2, 1, 1})
	binary.LittleEndian.PutUint16(b[16:], 2)
	machine := uint16(62)
	if architecture == "arm64" {
		machine = 183
	}
	binary.LittleEndian.PutUint16(b[18:], machine)
	binary.LittleEndian.PutUint32(b[20:], 1)
	binary.LittleEndian.PutUint16(b[52:], 64)
	return b
}

type helperInstallFixture struct {
	service                            *Service
	api                                *client.Client
	ctx                                context.Context
	directory                          string
	mu                                 sync.Mutex
	files                              map[string][]byte
	copies                             int
	allowed                            bool
	readonly, symlink, corrupt, revoke bool
	events                             []DockerPreparation
}

func newHelperInstallFixture(t *testing.T, architecture string) *helperInstallFixture {
	t.Helper()
	f := &helperInstallFixture{directory: t.TempDir(), files: map[string][]byte{}, allowed: true}
	for _, arch := range []string{"amd64", "arm64"} {
		directory := filepath.Join(f.directory, "linux-"+arch)
		if err := os.MkdirAll(directory, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(directory, "edo-terminal-session"), helperFixtureBinary(arch), 0600); err != nil {
			t.Fatal(err)
		}
	}
	f.service = NewService(nil, nil, time.Hour, WithDockerHelperDirectory(f.directory))
	f.ctx = WithExecutionAuthorization(t.Context(), func() bool { f.mu.Lock(); defer f.mu.Unlock(); return f.allowed })
	f.ctx = WithDockerPreparationObserver(f.ctx, func(event DockerPreparation) { f.events = append(f.events, event) })
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if strings.HasSuffix(r.URL.Path, "/_ping") {
			w.Header().Set("API-Version", "1.55")
			return
		}
		if strings.Contains(r.URL.Path, "/images/") {
			_ = json.NewEncoder(w).Encode(map[string]any{"Os": "linux", "Architecture": architecture})
			return
		}
		file := r.URL.Query().Get("path")
		switch r.Method {
		case http.MethodHead, http.MethodGet:
			stat := container.PathStat{Name: path.Base(file), Mode: os.ModeDir | 0755}
			content, exists := f.files[file]
			if strings.HasPrefix(file, terminalwire.HelperPath) {
				if !exists {
					http.Error(w, "missing", 404)
					return
				}
				stat.Mode = 0555
				stat.Size = int64(len(content))
			} else if f.symlink && file == "/usr/local/libexec" {
				stat.Mode = os.ModeSymlink | 0777
				stat.LinkTarget = "/business"
			}
			encoded, _ := json.Marshal(stat)
			w.Header().Set("X-Docker-Container-Path-Stat", base64.StdEncoding.EncodeToString(encoded))
			if r.Method == http.MethodGet {
				writer := tar.NewWriter(w)
				_ = writer.WriteHeader(&tar.Header{Name: path.Base(file), Typeflag: tar.TypeReg, Mode: 0555, Size: int64(len(content))})
				_, _ = writer.Write(content)
				_ = writer.Close()
			}
		case http.MethodPut:
			f.copies++
			if f.readonly {
				http.Error(w, "read-only filesystem", 403)
				return
			}
			reader := tar.NewReader(r.Body)
			for {
				header, err := reader.Next()
				if errors.Is(err, io.EOF) {
					break
				}
				if err != nil {
					t.Error(err)
					return
				}
				if header.Typeflag == tar.TypeDir {
					continue
				}
				if header.Mode != 0555 || !strings.HasPrefix("/"+header.Name, terminalwire.HelperPath+"-") {
					t.Error("工具写入了约定路径以外的位置或模式错误")
				}
				body, _ := io.ReadAll(reader)
				if f.corrupt {
					body[0] = 0
				}
				f.files["/"+header.Name] = body
			}
			if f.revoke {
				f.allowed = false
			}
		default:
			t.Error("准备阶段出现非预期操作", r.Method, r.URL.Path)
			http.Error(w, "unexpected", 500)
		}
	}))
	t.Cleanup(server.Close)
	var err error
	f.api, err = client.NewClientWithOpts(client.WithHost(server.URL), client.WithAPIVersionNegotiation())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.api.Close() })
	return f
}

func TestDockerHelperReusesMatchingPreinstalledToolOnReadOnlyTarget(t *testing.T) {
	f := newHelperInstallFixture(t, "arm64")
	f.readonly = true
	f.files[terminalwire.HelperPath] = helperFixtureBinary("arm64")
	file, err := f.prepare()
	if err != nil || file != terminalwire.HelperPath || f.copies != 0 || len(f.events) != 0 {
		t.Fatal("匹配的预装工具未复用", err)
	}
}

func (f *helperInstallFixture) prepare() (string, error) {
	return f.service.prepareDockerHelper(f.ctx, f.api, strings.Repeat("a", 64), "sha256:test", func(ctx context.Context) error { return executionAuthorized(ctx, true) })
}

func TestDockerHelperAutomaticallyInstallsMatchingArchitectureAndReusesDigest(t *testing.T) {
	for _, arch := range []string{"amd64", "arm64"} {
		t.Run(arch, func(t *testing.T) {
			f := newHelperInstallFixture(t, arch)
			file, err := f.prepare()
			if err != nil || !strings.HasPrefix(file, terminalwire.HelperPath+"-") || f.copies != 1 || len(f.events) != 1 || !f.events[0].Succeeded || f.events[0].Architecture != arch {
				t.Fatal("自动安装或审计结果错误", err)
			}
			if !bytes.Equal(f.files[file], helperFixtureBinary(arch)) {
				t.Fatal("安装了错误架构的工具")
			}
			again, err := f.prepare()
			if err != nil || again != file || f.copies != 1 || len(f.events) != 1 {
				t.Fatal("已安装工具没有复用", err)
			}
		})
	}
}

func TestDockerHelperConcurrentPreparationOnlyCopiesOnce(t *testing.T) {
	f := newHelperInstallFixture(t, "arm64")
	var group sync.WaitGroup
	for range 8 {
		group.Go(func() {
			if _, err := f.prepare(); err != nil {
				t.Error("并发准备失败", err)
			}
		})
	}
	group.Wait()
	if f.copies != 1 || len(f.events) != 1 || !f.events[0].Succeeded {
		t.Fatal("并发准备重复安装或审计")
	}
}

func TestDockerHelperRejectsMissingAuthorityRevocationAndUnsafeTargets(t *testing.T) {
	for _, scenario := range []string{"无 Exec 授权", "无当前授权检查", "目录是符号链接", "只读文件系统", "校验失败", "准备时撤权", "架构不支持"} {
		t.Run(scenario, func(t *testing.T) {
			arch := "arm64"
			if scenario == "架构不支持" {
				arch = "riscv64"
			}
			f := newHelperInstallFixture(t, arch)
			switch scenario {
			case "无 Exec 授权":
				f.allowed = false
			case "无当前授权检查":
				f.ctx = t.Context()
			case "目录是符号链接":
				f.symlink = true
			case "只读文件系统":
				f.readonly = true
			case "校验失败":
				f.corrupt = true
			case "准备时撤权":
				f.revoke = true
			}
			_, err := f.prepare()
			if err == nil {
				t.Fatal("不满足准备条件仍然成功")
			}
			if scenario == "无 Exec 授权" || scenario == "无当前授权检查" || scenario == "目录是符号链接" || scenario == "架构不支持" {
				if f.copies != 0 {
					t.Fatal("校验失败仍然写入工具")
				}
			}
			for _, event := range f.events {
				if event.Succeeded {
					t.Fatal("准备失败仍登记安装成功")
				}
			}
		})
	}
}

func TestDockerHelperBundledSourceRejectsLinksAndWrongArchitecture(t *testing.T) {
	f := newHelperInstallFixture(t, "arm64")
	file := filepath.Join(f.directory, "linux-arm64", "edo-terminal-session")
	if err := os.WriteFile(file, helperFixtureBinary("amd64"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readDockerHelper(f.directory, "arm64"); err == nil {
		t.Fatal("接受错误架构工具")
	}
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(f.directory, "linux-amd64", "edo-terminal-session"), file); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readDockerHelper(f.directory, "arm64"); err == nil {
		t.Fatal("接受工具目录中的符号链接")
	}
}

func TestDockerHelperUnbundledPathStillRequiresAuthorization(t *testing.T) {
	f := newHelperInstallFixture(t, "arm64")
	f.service.helperDirectory = filepath.Join(t.TempDir(), "missing")
	f.ctx = t.Context()
	if _, err := f.prepare(); !errors.Is(err, ErrExecAuthorization) || f.copies != 0 {
		t.Fatal("源码运行路径绕过了 Exec 授权", err)
	}
}
