package dockerengine

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/containerd/errdefs"
	"github.com/moby/moby/client"
)

func TestImageTransferRealSDKProgressAndFailure(t *testing.T) {
	for _, failure := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "failure"}[failure], func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if pushStreamTestPing(w, r) {
					return
				}
				if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/images/create") {
					t.Errorf("额外Docker操作: %s %s", r.Method, r.URL.Path)
					http.NotFound(w, r)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, "{\"id\":\"layer-one\",\"status\":\"Downloading\",\"progressDetail\":{\"current\":1024,\"total\":4096}}\n")
				w.(http.Flusher).Flush()
				if failure {
					_, _ = io.WriteString(w, "{\"errorDetail\":{\"code\":401,\"message\":\"registry rejected fixture credentials\"}}\n")
					return
				}
				_, _ = io.WriteString(w, "{\"id\":\"layer-one\",\"status\":\"Pull complete\"}\n")
			}))
			defer server.Close()
			service, _ := pushStreamTestService(t, server.URL)
			api, err := service.builderExecutionClient()
			if err != nil {
				t.Fatal(err)
			}
			defer api.Close()
			stream, err := api.ImagePull(t.Context(), "alpine:3.22", client.ImagePullOptions{})
			if err != nil {
				t.Fatal(err)
			}
			var output bytes.Buffer
			err = waitImageTransfer(t.Context(), stream, &output)
			if !strings.Contains(output.String(), "layer-one: Downloading") || !strings.Contains(output.String(), "1.024kB/4.096kB") {
				t.Fatalf("真实分层进度丢失: %q", output.String())
			}
			if failure {
				if !errdefs.IsUnauthorized(err) || !strings.Contains(err.Error(), "registry rejected fixture credentials") {
					t.Fatalf("日志转换改变Docker错误分类: %v", err)
				}
			} else if err != nil || !strings.Contains(output.String(), "layer-one: Pull complete") {
				t.Fatalf("拉取完成日志丢失: %q %v", output.String(), err)
			}
		})
	}
}

func TestImageTransferRealSDKCancellationStopsStream(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if pushStreamTestPing(w, r) {
			return
		}
		_, _ = io.WriteString(w, "{\"status\":\"Downloading\"}\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer server.Close()
	service, _ := pushStreamTestService(t, server.URL)
	api, err := service.builderExecutionClient()
	if err != nil {
		t.Fatal(err)
	}
	defer api.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	stream, err := api.ImagePull(ctx, "alpine:3.22", client.ImagePullOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := waitImageTransfer(ctx, stream, &output); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("日志流未遵守实际取消: %v", err)
	}
	if !strings.Contains(output.String(), "Downloading") {
		t.Fatal("超时前已收到的进度丢失")
	}
}
