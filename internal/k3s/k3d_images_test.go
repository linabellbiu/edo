package k3s

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/regclient/regclient"
	registryconfig "github.com/regclient/regclient/config"
	"github.com/regclient/regclient/scheme/reg"

	"edo/internal/model"
)

type k3dRoundTrip func(*http.Request) (*http.Response, error)

func (fn k3dRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return fn(r) }

func TestK3dImageSourcesAndVersionsAreFrozenWithoutChangingOldSnapshots(t *testing.T) {
	for _, example := range []struct{ source, image, version string }{
		{"domestic", k3dDomesticRepository + ":v1.35.5-k3s1", "v1.35.5+k3s1"},
		{"dockerhub", "rancher/k3s:v1.36.6-k3s2", "v1.36.6+k3s2"},
		{"custom", "registry.example.com:5000/team/k3s:v1.34.1-k3s1", "v1.34.1+k3s1"},
		{"custom", "rancher/k3s:v1.36.6-k3s1", "v1.36.6+k3s1"},
		{"custom", "registry.example.com/team/k3s@sha256:" + strings.Repeat("a", 64), ""},
	} {
		t.Run(example.source+example.version, func(t *testing.T) {
			f, _, _, input := k3dFixture(t)
			input.K3d.ImageSource, input.K3d.Image = example.source, example.image
			plan, err := f.service.Preview(f.ctx, f.actor.ID, input)
			if err != nil {
				t.Fatal(err)
			}
			var record model.K3sInstallation
			if err := f.db.First(&record, "uuid = ?", plan.ID).Error; err != nil {
				t.Fatal(err)
			}
			snapshot, err := f.service.decode(record)
			if err != nil {
				t.Fatal(err)
			}
			if plan.Version != example.version || snapshot.K3dK3sVersion != example.version || plan.K3d.ImageSource != example.source || snapshot.Input.K3d.Image != plan.K3d.Image {
				t.Fatalf("镜像或版本未冻结: %+v", plan)
			}
			if err := f.service.recheckRuntime(f.ctx, record, snapshot); err != nil {
				t.Fatal("动态版本被固定旧版本阻断", err)
			}
			script, err := k3dInstallScript(snapshot.Input, plan.ID)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(script, "--image "+shellQuote(plan.K3d.Image)) {
				t.Fatal("实际命令未使用确认的完整镜像地址")
			}
		})
	}
	legacy := &K3dOptions{}
	if k3dImage(legacy) != "rancher/k3s:v1.37.1-k3s1" || k3dImageVersion(legacy) != legacyK3dK3sVersion {
		t.Fatal("旧快照镜像被国内默认改写")
	}
}

func TestK3dImageSelectionRejectsInvalidOrMismatchedReferences(t *testing.T) {
	for _, example := range []K3dOptions{
		{}, {Image: "rancher/k3s:v1.37.1-k3s1"},
		{ImageSource: "unknown", Image: "rancher/k3s:v1.37.1-k3s1"},
		{ImageSource: "dockerhub", Image: "registry.example.com/team/k3s:v1.37.1-k3s1"},
		{ImageSource: "domestic", Image: k3dDomesticRepository + ":latest"},
		{ImageSource: "custom", Image: "https://user:password@registry.example.com/k3s:v1"},
		{ImageSource: "custom", Image: "rancher/k3s"},
		{ImageSource: "custom", Image: "rancher/k3s:v1\nfalse"},
		{ImageSource: "dockerhub", Image: "rancher/k3s:v1.37.2-rc1-k3s1"},
	} {
		if err := normalizeK3dImage(&example); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("接受无效镜像: %+v %v", example, err)
		}
	}
	value := K3dOptions{Image: k3dDomesticRepository + ":v1.29.6-k3s2-linuxarm64"}
	if err := normalizeK3dImage(&value); err != nil || value.ImageSource != "domestic" || k3dImageVersion(&value) != "v1.29.6+k3s2" {
		t.Fatal("国内默认或独立 ARM64 标签不可用", err)
	}
}

func TestK3dDomesticCatalogUsesActualTagsAndManifestPlatforms(t *testing.T) {
	config := []byte(`{"architecture":"arm64","os":"linux","config":{},"rootfs":{"type":"layers","diff_ids":[]}}`)
	digest := fmt.Sprintf("sha256:%x", sha256.Sum256(config))
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Method != http.MethodGet {
			t.Error("版本读取产生非只读请求", r.Method)
		}
		switch {
		case strings.HasSuffix(r.URL.Path, "/tags/list"):
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"name":"ddn-k8s/docker.io/rancher/k3s","tags":["latest","v1.29.6-k3s2-linuxarm64","v1.37.2-rc1-k3s1","v1.37.1-k3s1"]}`)
		case strings.HasSuffix(r.URL.Path, "/manifests/v1.37.1-k3s1"):
			w.Header().Set("Content-Type", "application/vnd.oci.image.index.v1+json")
			fmt.Fprintf(w, `{"schemaVersion":2,"mediaType":"application/vnd.oci.image.index.v1+json","manifests":[{"mediaType":"application/vnd.oci.image.manifest.v1+json","digest":"sha256:%s","size":100,"platform":{"os":"linux","architecture":"amd64"}},{"mediaType":"application/vnd.oci.image.manifest.v1+json","digest":"sha256:%s","size":100,"platform":{"os":"unknown","architecture":"unknown"}}]}`, strings.Repeat("b", 64), strings.Repeat("c", 64))
		case strings.HasSuffix(r.URL.Path, "/manifests/v1.29.6-k3s2-linuxarm64"):
			w.Header().Set("Content-Type", "application/vnd.oci.image.manifest.v1+json")
			fmt.Fprintf(w, `{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","config":{"mediaType":"application/vnd.oci.image.config.v1+json","digest":%q,"size":%d},"layers":[]}`, digest, len(config))
		case strings.HasSuffix(r.URL.Path, "/blobs/"+digest):
			w.Header().Set("Content-Type", "application/octet-stream")
			w.Write(config)
		default:
			t.Error("未预期仓库请求", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	f, _, _, _ := k3dFixture(t)
	f.service.k3dImages.registry = regclient.New(regclient.WithConfigHost(registryconfig.Host{Name: "swr.cn-north-4.myhuaweicloud.com", Hostname: strings.TrimPrefix(server.URL, "http://"), TLS: registryconfig.TLSDisabled}), regclient.WithRegOpts(reg.WithRetryLimit(0)))
	result, err := f.service.K3dImages(f.ctx, f.actor.ID, "domestic")
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Versions) != 2 || result.Versions[0].Version != "v1.37.1+k3s1" || fmt.Sprint(result.Versions[0].Platforms) != "[linux/amd64]" || fmt.Sprint(result.Versions[1].Platforms) != "[linux/arm64]" {
		t.Fatalf("版本或平台来自猜测: %+v", result)
	}
	before := requests.Load()
	result.Versions[0].Platforms[0] = "modified"
	cached, err := f.service.K3dImages(f.ctx, f.actor.ID, "domestic")
	if err != nil || requests.Load() != before || cached.Versions[0].Platforms[0] != "linux/amd64" {
		t.Fatal("缓存未复用或可被响应调用者改写", err)
	}
	f.auth.denied = true
	if _, err := f.service.K3dImages(f.ctx, f.actor.ID, "domestic"); !errors.Is(err, ErrPermission) {
		t.Fatal("缓存绕过撤权", err)
	}
}

func TestK3dDockerHubCatalogBatchesPlatformsAndNeverReturnsRawErrors(t *testing.T) {
	f, _, _, _ := k3dFixture(t)
	var calls atomic.Int32
	f.service.k3dImages.http = &http.Client{Transport: k3dRoundTrip(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		if r.URL.Host != "hub.docker.com" || r.Method != "GET" {
			t.Fatal("读取了非官方标签接口")
		}
		body := `{"next":null,"results":[{"name":"v1.37.2-k3s1","images":[{"os":"linux","architecture":"arm64"},{"os":"linux","architecture":"amd64"}]},{"name":"latest","images":[]},{"name":"v1.37.3-rc1-k3s1","images":[]}]}`
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header), Request: r}, nil
	})}
	result, err := f.service.K3dImages(f.ctx, f.actor.ID, "dockerhub")
	if err != nil || calls.Load() != 1 || len(result.Versions) != 1 || len(result.Versions[0].Platforms) != 2 {
		t.Fatal("未批量读取实际稳定版本", result, err)
	}
	f.service.k3dImages.mu.Lock()
	f.service.k3dImages.entries["dockerhub"].CheckedAt = time.Time{}
	f.service.k3dImages.mu.Unlock()
	f.service.k3dImages.http.Transport = k3dRoundTrip(func(r *http.Request) (*http.Response, error) {
		return nil, errors.New("underlying token=do-not-display private.address")
	})
	_, err = f.service.K3dImages(f.ctx, f.actor.ID, "dockerhub")
	if err != ErrK3dImageCatalog || strings.Contains(err.Error(), "do-not-display") {
		t.Fatal("错误未安全返回", err)
	}
}

func TestK3dImageCatalogSharesConcurrentReadsAndHonorsCancellation(t *testing.T) {
	f, _, _, _ := k3dFixture(t)
	started, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	f.service.k3dImages.http = &http.Client{Transport: k3dRoundTrip(func(r *http.Request) (*http.Response, error) {
		if calls.Add(1) == 1 {
			close(started)
		}
		<-release
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"results":[]}`)), Header: make(http.Header), Request: r}, nil
	})}
	ctx, cancel := context.WithCancel(f.ctx)
	done := make(chan error, 1)
	go func() { _, err := f.service.K3dImages(ctx, f.actor.ID, "dockerhub"); done <- err }()
	<-started
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal("取消请求仍等待网络", err)
	}
	close(release)
	if _, err := f.service.K3dImages(f.ctx, f.actor.ID, "dockerhub"); err != nil || calls.Load() != 1 {
		t.Fatal("共享读取未完成或重复请求", err, calls.Load())
	}
}
