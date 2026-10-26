package repository

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/transport"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"

	"edo/internal/config"
	"edo/internal/model"
)

func TestListRefsRecoversTransientHTTPFailures(t *testing.T) {
	for _, failure := range []string{"eof", "truncated", "502"} {
		t.Run(failure, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/repo.git/info/refs" || r.URL.Query().Get("service") != "git-upload-pack" {
					t.Errorf("不应执行引用查询之外的请求: %s", r.URL.Path)
				}
				if calls.Add(1) == 1 {
					switch failure {
					case "eof":
						connection, _, err := w.(http.Hijacker).Hijack()
						if err != nil {
							t.Error(err)
							return
						}
						_ = connection.Close()
					case "truncated":
						w.Header().Set("Content-Type", "application/x-git-upload-pack-advertisement")
						w.Header().Set("Content-Length", "1000")
						_, _ = io.WriteString(w, "001e# service=git-upload-pack\n00000064partial")
					case "502":
						w.WriteHeader(http.StatusBadGateway)
					}
					return
				}
				writeTestGitAdvertisement(w)
			}))
			defer server.Close()
			client := NewGitClient(config.Git{Timeout: 3 * time.Second})
			refs, err := client.ListRefs(context.Background(), model.GitRepository{CloneURL: server.URL + "/repo.git", AuthType: model.GitAuthNone}, "")
			if err != nil || calls.Load() != 2 || len(refs.Branches) != 1 || refs.Branches[0].Name != "main" {
				t.Fatalf("瞬时中断后应重新读取引用成功: calls=%d refs=%+v err=%v", calls.Load(), refs, err)
			}
		})
	}
}

func writeTestGitAdvertisement(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/x-git-upload-pack-advertisement")
	line := strings.Repeat("a", 40) + " refs/heads/main\x00multi_ack\n"
	_, _ = fmt.Fprintf(w, "001e# service=git-upload-pack\n0000%04x%s0000", len(line)+4, line)
}

func TestListRefsLimitsAttemptsAndRejectsPermanentHTTPFailures(t *testing.T) {
	for _, status := range []int{401, 403, 404, 400, 502} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				w.WriteHeader(status)
			}))
			defer server.Close()
			client := NewGitClient(config.Git{Timeout: 3 * time.Second})
			_, err := client.ListRefs(context.Background(), model.GitRepository{CloneURL: server.URL + "/repo.git", AuthType: model.GitAuthNone}, "")
			want := int32(1)
			if status == 502 {
				want = 3
			}
			if err == nil || calls.Load() != want || IsTransientGitError(err) != (status == 502) {
				t.Fatalf("请求次数或错误分类不正确: calls=%d want=%d err=%v", calls.Load(), want, err)
			}
		})
	}
}

func TestListRefsRetryHonorsTotalTimeoutAndCancellation(t *testing.T) {
	for _, cancelRequest := range []bool{false, true} {
		t.Run(fmt.Sprint(cancelRequest), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				w.Header().Set("Retry-After", "10")
				w.WriteHeader(http.StatusTooManyRequests)
				if cancelRequest {
					cancel()
				}
			}))
			defer server.Close()
			client := NewGitClient(config.Git{Timeout: 100 * time.Millisecond})
			started := time.Now()
			_, err := client.ListRefs(ctx, model.GitRepository{CloneURL: server.URL + "/repo.git", AuthType: model.GitAuthNone}, "")
			if err == nil || calls.Load() != 1 || time.Since(started) > time.Second {
				t.Fatalf("取消或总超时后仍在继续重试: calls=%d elapsed=%s err=%v", calls.Load(), time.Since(started), err)
			}
			if !cancelRequest && !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("总超时应保留 deadline 错误: %v", err)
			}
		})
	}
}

func TestTransientGitErrorClassification(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"EOF", io.EOF, true},
		{"unexpected EOF", io.ErrUnexpectedEOF, true},
		{"wrapped EOF", fmt.Errorf("读取引用: %w", plumbing.NewUnexpectedError(&url.Error{Op: "Get", URL: "https://example.invalid/repo", Err: io.EOF})), true},
		{"reset", syscall.ECONNRESET, true},
		{"timeout", context.DeadlineExceeded, true},
		{"canceled", plumbing.NewUnexpectedError(context.Canceled), false},
		{"authentication", transport.ErrAuthenticationRequired, false},
		{"authorization", transport.ErrAuthorizationFailed, false},
		{"certificate", &url.Error{Op: "Get", Err: x509.UnknownAuthorityError{}}, false},
		{"permanent", plumbing.NewPermanentError(io.EOF), false},
		{"protocol", errors.New("invalid pkt-line: EOF"), false},
		{"http 503", plumbing.NewUnexpectedError(&githttp.Err{Response: &http.Response{StatusCode: 503}}), true},
		{"http 501", plumbing.NewUnexpectedError(&githttp.Err{Response: &http.Response{StatusCode: 501}}), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsTransientGitError(tc.err); got != tc.want {
				t.Fatalf("瞬时错误分类: got=%v want=%v", got, tc.want)
			}
		})
	}
}
