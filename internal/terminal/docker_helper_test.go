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
	"github.com/moby/moby/api/types/volume"
	"github.com/moby/moby/client"

	"edo/internal/terminalassets"
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
	disconnects                        int
	disconnectAfterCopy                bool
	revokeOnDisconnect                 bool
	target                             container.InspectResponse
	volume                             volume.Volume
	preparer                           container.InspectResponse
	creates, removes                   int
	cleanupDenied, changePreparer      bool
	revokeOnCreate                     bool
	createDisconnect                   bool
	preparationName                    string
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
		if strings.Contains(r.URL.Path, "/volumes/") {
			_ = json.NewEncoder(w).Encode(f.volume)
			return
		}
		preparerID := strings.Repeat("b", 64)
		if strings.HasSuffix(r.URL.Path, "/containers/create") {
			f.creates++
			f.preparationName = r.URL.Query().Get("name")
			var input struct {
				container.Config
				HostConfig container.HostConfig
			}
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
				t.Error(err)
			}
			if input.Image != "sha256:test" || !input.HostConfig.ReadonlyRootfs || input.HostConfig.NetworkMode != "none" || len(input.HostConfig.Mounts) != 1 || input.HostConfig.Mounts[0].Source != f.volume.Name || input.HostConfig.Mounts[0].ReadOnly || !input.HostConfig.Mounts[0].VolumeOptions.NoCopy || len(input.Entrypoint) != 1 || input.Labels[helperPreparationLabel] == "" {
				t.Error("准备容器未保持固定镜像、隔离或工具卷边界")
			}
			f.preparer = container.InspectResponse{ID: preparerID, Image: input.Image, Config: &input.Config, State: &container.State{Status: container.StateCreated}}
			if f.changePreparer {
				f.preparer.Config.Labels[helperPreparationLabel] = "changed"
			}
			if f.revokeOnCreate {
				f.allowed = false
			}
			if f.createDisconnect {
				connection, _, err := w.(http.Hijacker).Hijack()
				if err != nil {
					t.Error(err)
					return
				}
				_ = connection.Close()
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"Id": preparerID})
			return
		}
		if strings.HasSuffix(r.URL.Path, "/containers/"+preparerID+"/json") || (f.preparationName != "" && strings.HasSuffix(r.URL.Path, "/containers/"+f.preparationName+"/json")) {
			_ = json.NewEncoder(w).Encode(f.preparer)
			return
		}
		if r.Method == http.MethodDelete && strings.HasSuffix(r.URL.Path, "/containers/"+preparerID) {
			f.removes++
			if r.URL.Query().Get("force") == "1" || r.URL.Query().Get("force") == "true" {
				t.Error("清理准备容器不允许强制删除")
			}
			if f.cleanupDenied {
				http.Error(w, "cleanup denied", 403)
			} else {
				w.WriteHeader(http.StatusNoContent)
			}
			return
		}
		file := r.URL.Query().Get("path")
		switch r.Method {
		case http.MethodHead, http.MethodGet:
			stat := container.PathStat{Name: path.Base(file), Mode: os.ModeDir | 0755}
			content, exists := f.files[file]
			if strings.HasPrefix(file, terminalwire.HelperPath) || strings.HasPrefix(file, terminalassets.Directory+"/edo-terminal-session") {
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
			if f.disconnects > 0 {
				f.disconnects--
				if f.revokeOnDisconnect {
					f.allowed = false
				}
				connection, _, err := w.(http.Hijacker).Hijack()
				if err != nil {
					t.Error(err)
					return
				}
				_ = connection.Close()
				return
			}
			if f.readonly && !strings.Contains(r.URL.Path, preparerID) {
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
				destination := "/" + header.Name
				if strings.Contains(r.URL.Path, preparerID) {
					if file != terminalassets.Directory || header.Name != path.Base(header.Name) {
						t.Error("工具卷归档超出了固定安装目录")
					}
					destination = path.Join(file, header.Name)
				} else if f.volume.Name != "" {
					t.Error("直接写入了只读业务容器")
				}
				if header.Mode != 0555 || !strings.HasPrefix(path.Base(header.Name), "edo-terminal-session-") {
					t.Error("工具写入了约定路径以外的位置或模式错误")
				}
				body, _ := io.ReadAll(reader)
				if f.corrupt {
					body[0] = 0
				}
				f.files[destination] = body
			}
			if f.revoke {
				f.allowed = false
			}
			if f.disconnectAfterCopy {
				connection, _, err := w.(http.Hijacker).Hijack()
				if err != nil {
					t.Error(err)
					return
				}
				_ = connection.Close()
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
	return f.service.prepareDockerHelper(f.ctx, f.api, strings.Repeat("a", 64), "sha256:test", func(ctx context.Context) error { return executionAuthorized(ctx, true) }, f.target)
}

func (f *helperInstallFixture) copyCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.copies
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

func TestDockerHelperRecoversSingleUploadDisconnectWithFreshReader(t *testing.T) {
	f := newHelperInstallFixture(t, "arm64")
	f.disconnects = 1
	file, err := f.prepare()
	if err != nil || f.copies != 2 || !bytes.Equal(f.files[file], helperFixtureBinary("arm64")) {
		t.Fatal("传输中断后未能完整重传同一工具", err)
	}
	if len(f.events) != 1 || !f.events[0].Succeeded || f.events[0].Attempts != 2 {
		t.Fatal("未记录真实传输次数和安装结果", f.events)
	}
}

func TestDockerHelperChecksCompletedUploadBeforeRepeatingWrite(t *testing.T) {
	f := newHelperInstallFixture(t, "arm64")
	f.disconnectAfterCopy = true
	file, err := f.prepare()
	if err != nil || f.copies != 1 || !bytes.Equal(f.files[file], helperFixtureBinary("arm64")) || len(f.events) != 1 || !f.events[0].Succeeded {
		t.Fatal("已完整写入的工具在断连后被重复覆盖", err)
	}
}

func TestDockerHelperUploadRetryIsBoundedAndCannotBypassRevocation(t *testing.T) {
	for _, revoked := range []bool{false, true} {
		f := newHelperInstallFixture(t, "arm64")
		f.disconnects = 3
		f.revokeOnDisconnect = revoked
		_, err := f.prepare()
		want := 2
		if revoked {
			want = 1
			if !errors.Is(err, ErrExecAuthorization) {
				t.Fatal("中断期间撤权未立即阻止重传", err)
			}
		} else if !dockerHelperTransportInterrupted(err) {
			t.Fatal("持续中断没有保留网络失败", err)
		}
		if err == nil || f.copyCount() != want || len(f.events) != 1 || f.events[0].Succeeded || f.events[0].Attempts != want {
			t.Fatal("重传无界或错误报告安装成功", f.copyCount(), f.events, err)
		}
	}
	f := newHelperInstallFixture(t, "arm64")
	f.readonly = true
	_, err := f.prepare()
	if f.copyCount() != 1 || !errors.Is(err, ErrDockerHelperWriteDenied) {
		t.Fatal("写入拒绝被盲目重传或未正确分类", f.copyCount(), err)
	}
}

func TestDockerHelperDoesNotRetryChangedContainerIdentity(t *testing.T) {
	f := newHelperInstallFixture(t, "arm64")
	f.disconnects = 1
	_, err := f.service.prepareDockerHelper(f.ctx, f.api, strings.Repeat("a", 64), "image", func(ctx context.Context) error {
		if err := executionAuthorized(ctx, true); err != nil {
			return err
		}
		if f.copyCount() > 0 {
			return ErrTargetNotReady
		}
		return nil
	}, f.target)
	if !errors.Is(err, ErrTargetNotReady) || f.copyCount() != 1 {
		t.Fatal("容器身份变化后仍然重传", err)
	}
}

func TestDockerHelperErrorKeepsTransportFailureDistinctFromWriteDenied(t *testing.T) {
	s := NewService(nil, nil, time.Hour)
	for _, sample := range []struct{ cause, want error }{
		{io.EOF, ErrDockerHelperConnection},
		{context.DeadlineExceeded, ErrDockerHelperTimeout},
		{ErrDockerHelperWriteDenied, ErrDockerHelperWriteDenied},
		{ErrDockerHelperReadOnly, ErrDockerHelperReadOnly},
		{errors.Join(ErrDockerHelperWriteDenied, io.EOF), ErrDockerHelperWriteDenied},
		{errors.New("随包工具校验失败"), ErrDockerHelperUnavailable},
	} {
		if got := s.dockerPreparationError(t.Context(), "fixture", sample.cause); got != sample.want {
			t.Fatal("准备失败分类错误", got)
		}
	}
}
