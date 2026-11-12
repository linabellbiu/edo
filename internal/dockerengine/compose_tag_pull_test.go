package dockerengine

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestConfiguredRegistryImagePullsOriginalTagEvenWhenCached(t *testing.T) {
	s, db := newMonitorTestService(t)
	var pulls, inspects atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/_ping"):
			w.Header().Set("API-Version", "1.54")
		case strings.HasSuffix(r.URL.Path, "/images/create"):
			pulls.Add(1)
			if r.URL.Query().Get("fromImage") != "registry.example.com/team/app" || r.URL.Query().Get("tag") != "v4" {
				t.Error("拉取地址没有保留用户 Tag", r.URL.RawQuery)
			}
			decoded, err := base64.URLEncoding.DecodeString(r.Header.Get("X-Registry-Auth"))
			if err != nil {
				t.Error(err)
			}
			var auth map[string]any
			if err = json.Unmarshal(decoded, &auth); err != nil || auth["password"] != "private-password" {
				t.Error("没有复用已选择的仓库认证", err)
			}
			_, _ = w.Write([]byte("{\"status\":\"Downloaded\"}\n"))
		case strings.HasSuffix(r.URL.Path, "/json"):
			inspects.Add(1)
			_, _ = w.Write([]byte("{\"Id\":\"sha256:" + strings.Repeat("b", 64) + "\"}"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	endpoint := createMonitorEndpoint(t, db, "tag-pull", strings.Replace(server.URL, "http://", "tcp://", 1), "tag-host")
	auth := RegistryAuth{ServerAddress: "https://registry.example.com", Username: "user", Credential: "private-password"}
	for range 2 {
		if err := s.PrepareConfiguredRegistryImage(t.Context(), endpoint.ID, "tag-host", "registry.example.com/team/app:v4", auth); err != nil {
			t.Fatal(err)
		}
	}
	if pulls.Load() != 2 || inspects.Load() != 2 {
		t.Fatal("缓存 Tag 导致跳过实际拉取", pulls.Load(), inspects.Load())
	}
	if err := s.PrepareConfiguredRegistryImage(t.Context(), endpoint.ID, "foreign-host", "registry.example.com/team/app:v4", auth); err == nil || pulls.Load() != 2 {
		t.Fatal("未校验执行主机身份")
	}
	if err := s.PrepareRegistryImage(t.Context(), endpoint.ID, "tag-host", "registry.example.com/team/app:v4", auth); err == nil {
		t.Fatal("改变了历史不可变镜像入口的校验")
	}
}
