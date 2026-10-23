package repository

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/protocol/packp"
	"github.com/go-git/go-git/v5/plumbing/protocol/packp/capability"

	"edo/internal/config"
	"edo/internal/model"
)

func TestGitCommitMetadataFallbackUsesOriginalRefAndPinnedCommit(t *testing.T) {
	path, first := newMetadataGitFixture(t, 64)
	remote, err := git.PlainOpen(path)
	if err != nil {
		t.Fatal(err)
	}
	second := commitVersionFile(t, remote, path, "advanced branch\n", "分支最新标题")
	for _, ref := range []string{"refs/pull/17/head", "refs/merge-requests/18/head", "refs/tags/v1.0-lightweight"} {
		if err := remote.Storer.SetReference(plumbing.NewHashReference(plumbing.ReferenceName(ref), first)); err != nil {
			t.Fatal(err)
		}
	}
	tag, err := remote.CreateTag("v1.0-annotated", first, &git.CreateTagOptions{
		Tagger:  &object.Signature{Name: "EDO Test", Email: "edo@example.com", When: time.Now().UTC()},
		Message: "原始版本说明",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		ref     string
		pinned  plumbing.Hash
		message string
	}{
		{"refs/heads/master", second, "分支最新标题"},
		{"refs/pull/17/head", first, "固定版本标题"},
		{"refs/merge-requests/18/head", first, "固定版本标题"},
		{"refs/tags/v1.0-lightweight", first, "固定版本标题"},
		{"refs/tags/v1.0-annotated", tag.Hash(), "固定版本标题"},
	} {
		t.Run(tc.ref, func(t *testing.T) {
			server, trace := metadataRefOnlyHTTPServer(t, path)
			client := NewGitClient(config.Git{Timeout: 5 * time.Second})
			message, err := client.CommitMessageCachedAt(context.Background(), model.GitRepository{
				CloneURL: server.URL + "/repo.git", AuthType: model.GitAuthNone,
			}, "", tc.ref, tc.pinned.String(), filepath.Join(t.TempDir(), "objects"), false)
			if err != nil || message != tc.message {
				t.Fatalf("不支持按 SHA 获取时没有沿原 Ref 返回固定提交: message=%q err=%v", message, err)
			}
			trace.assertFetch(t, 1, tc.pinned)
		})
	}
}

func TestGitCommitMetadataFallbackRespectsHistoricalDepth(t *testing.T) {
	path, pinned := newMetadataGitFixture(t, 64)
	remote, err := git.PlainOpen(path)
	if err != nil {
		t.Fatal(err)
	}
	commitVersionFile(t, remote, path, "next\n", "中间版本标题")
	head := commitVersionFile(t, remote, path, "latest\n", "新版本标题不得替代旧版")
	for _, historical := range []bool{false, true} {
		name := "当前版本"
		if historical {
			name = "历史回填"
		}
		t.Run(name, func(t *testing.T) {
			server, trace := metadataRefOnlyHTTPServer(t, path)
			client := NewGitClient(config.Git{Timeout: 5 * time.Second})
			message, err := client.CommitMessageCachedAt(context.Background(), model.GitRepository{
				CloneURL: server.URL + "/repo.git", AuthType: model.GitAuthNone,
			}, "", "refs/heads/master", pinned.String(), filepath.Join(t.TempDir(), "objects"), historical)
			if historical {
				if err != nil || message != "固定版本标题" {
					t.Fatalf("有限历史深度没有找到固定提交: message=%q err=%v", message, err)
				}
				trace.assertFetch(t, 100, head)
			} else {
				if message != "" || !errors.Is(err, plumbing.ErrObjectNotFound) {
					t.Fatalf("旧提交超出浅历史时不得返回新标题: message=%q err=%v", message, err)
				}
				trace.assertFetch(t, 1, head)
			}
		})
	}
}

type metadataFetchTrace struct {
	mu             sync.Mutex
	advertisements int
	depths         []int
	wants          [][]plumbing.Hash
}

func (trace *metadataFetchTrace) assertFetch(t *testing.T, depth int, want plumbing.Hash) {
	t.Helper()
	trace.mu.Lock()
	defer trace.mu.Unlock()
	if trace.advertisements != 2 || len(trace.depths) != 1 || trace.depths[0] != depth ||
		len(trace.wants) != 1 || len(trace.wants[0]) != 1 || trace.wants[0][0] != want {
		t.Fatalf("SHA 能力失败后只能沿原 Ref 有限获取一次: advertisements=%d depths=%v wants=%v want_depth=%d want_hash=%s",
			trace.advertisements, trace.depths, trace.wants, depth, want)
	}
}

// 使用真实 upload-pack 生成浅层 pack，只改 advertisement 的 SHA 获取能力；
// 每个测试拥有独立 HTTP 服务，不修改 go-git 的全局协议注册表。
func metadataRefOnlyHTTPServer(t *testing.T, path string) (*httptest.Server, *metadataFetchTrace) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Fatal("Git 元数据传输集成测试需要本机 git")
	}
	trace := &metadataFetchTrace{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		args := []string{"-c", "uploadpack.allowTipSHA1InWant=false", "-c", "uploadpack.allowReachableSHA1InWant=false", "upload-pack", "--stateless-rpc"}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/repo.git/info/refs" && r.URL.Query().Get("service") == "git-upload-pack":
			args = append(args, "--advertise-refs", path)
			body, err := exec.CommandContext(r.Context(), "git", args...).Output()
			if err != nil {
				t.Errorf("生成测试引用声明失败: %v", err)
				http.Error(w, "测试 Git 引用不可用", http.StatusInternalServerError)
				return
			}
			advertisement := packp.NewAdvRefs()
			if err := advertisement.Decode(bytes.NewReader(body)); err != nil {
				t.Errorf("解析测试引用声明失败: %v", err)
				http.Error(w, "测试 Git 引用不可用", http.StatusInternalServerError)
				return
			}
			advertisement.Capabilities.Delete(capability.AllowTipSHA1InWant)
			advertisement.Capabilities.Delete(capability.AllowReachableSHA1InWant)
			advertisement.Prefix = [][]byte{[]byte("# service=git-upload-pack"), {}}
			trace.mu.Lock()
			trace.advertisements++
			trace.mu.Unlock()
			w.Header().Set("Content-Type", "application/x-git-upload-pack-advertisement")
			if err := advertisement.Encode(w); err != nil {
				t.Errorf("发送测试引用声明失败: %v", err)
			}
		case r.Method == http.MethodPost && r.URL.Path == "/repo.git/git-upload-pack":
			body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
			if err != nil {
				t.Error(err)
				http.Error(w, "读取测试获取请求失败", http.StatusBadRequest)
				return
			}
			request := packp.NewUploadRequest()
			if err := request.Decode(bytes.NewReader(body)); err != nil {
				t.Errorf("解析测试获取请求失败: %v", err)
				http.Error(w, "测试获取请求无效", http.StatusBadRequest)
				return
			}
			depth, ok := request.Depth.(packp.DepthCommits)
			if !ok {
				t.Errorf("获取请求缺少提交深度上限: %T", request.Depth)
				http.Error(w, "测试获取请求无深度限制", http.StatusBadRequest)
				return
			}
			trace.mu.Lock()
			trace.depths = append(trace.depths, int(depth))
			trace.wants = append(trace.wants, append([]plumbing.Hash(nil), request.Wants...))
			trace.mu.Unlock()
			command := exec.CommandContext(r.Context(), "git", append(args, path)...)
			command.Stdin = bytes.NewReader(body)
			output, err := command.Output()
			if err != nil {
				t.Errorf("生成测试浅层 pack 失败: %v", err)
				http.Error(w, "测试 Git 对象不可用", http.StatusInternalServerError)
				return
			}
			w.Header().Set("Content-Type", "application/x-git-upload-pack-result")
			_, _ = w.Write(output)
		default:
			t.Errorf("收到不应发生的 Git 请求: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	return server, trace
}
