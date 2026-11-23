package k3s

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/distribution/reference"
	"github.com/regclient/regclient"
	"github.com/regclient/regclient/scheme"
	"github.com/regclient/regclient/scheme/reg"
	"github.com/regclient/regclient/types/manifest"
	registryref "github.com/regclient/regclient/types/ref"
	"golang.org/x/sync/singleflight"
)

const k3dDomesticRepository = "swr.cn-north-4.myhuaweicloud.com/ddn-k8s/docker.io/rancher/k3s"
const k3dDockerHubRepository = "docker.io/rancher/k3s"
const k3dDockerHubTagsURL = "https://hub.docker.com/v2/namespaces/rancher/repositories/k3s/tags?page_size=100&ordering=last_updated"

var ErrK3dImageCatalog = errors.New("镜像版本读取失败，请重试、切换来源或手动填写 K3s 镜像地址")
var k3dStableTag = regexp.MustCompile(`^(v[0-9]+\.[0-9]+\.[0-9]+)-k3s([1-9][0-9]*)(?:-linux(?:amd64|arm64|arm))?$`)

func k3dTagVersion(tag string) string {
	parts := k3dStableTag.FindStringSubmatch(tag)
	if len(parts) != 3 {
		return ""
	}
	version := parts[1] + "+k3s" + parts[2]
	if _, valid := parseK3sVersion(version); !valid {
		return ""
	}
	return version
}

func normalizeK3dImage(options *K3dOptions) error {
	if options.ImageSource == "" {
		options.ImageSource = "domestic"
	}
	if options.ImageSource != "domestic" && options.ImageSource != "dockerhub" && options.ImageSource != "custom" {
		return &ValidationError{Message: "请选择国内源、Docker Hub 或手动填写镜像地址"}
	}
	value := strings.TrimSpace(options.Image)
	if value == "" || len(value) > 512 || strings.ContainsAny(value, " \t\r\n\x00?#\\") || strings.Contains(value, "://") {
		return &ValidationError{Message: "请填写带明确标签或摘要的 K3s 镜像地址，不包含协议或登录凭据"}
	}
	named, err := reference.ParseNormalizedNamed(value)
	if err != nil || reference.IsNameOnly(named) {
		return &ValidationError{Message: "K3s 镜像地址无效，请填写仓库地址及明确标签或摘要"}
	}
	if options.ImageSource != "custom" {
		repository := k3dDomesticRepository
		if options.ImageSource == "dockerhub" {
			repository = k3dDockerHubRepository
		}
		tagged, ok := named.(reference.NamedTagged)
		if !ok || named.Name() != repository || k3dTagVersion(tagged.Tag()) == "" {
			return &ValidationError{Message: "所选镜像与来源不一致，请重新选择可安装版本"}
		}
	}
	options.Image = named.String()
	if options.ImageSource == "custom" {
		options.Image = value
	}
	return nil
}

func k3dImage(options *K3dOptions) string {
	if options != nil && options.Image != "" {
		return options.Image
	}
	// 历史快照没有镜像字段，按当时确认的官方固定镜像执行。
	return "rancher/k3s:" + strings.ReplaceAll(legacyK3dK3sVersion, "+", "-")
}

func k3dImageVersion(options *K3dOptions) string {
	named, err := reference.ParseNormalizedNamed(k3dImage(options))
	if err == nil {
		if tagged, ok := named.(reference.NamedTagged); ok {
			return k3dTagVersion(tagged.Tag())
		}
	}
	return ""
}

type k3dImageCatalogCache struct {
	once     sync.Once
	mu       sync.Mutex
	entries  map[string]*K3dImageCatalog
	requests singleflight.Group
	registry *regclient.RegClient
	http     *http.Client
}

func (cache *k3dImageCatalogCache) init() {
	cache.once.Do(func() {
		cache.entries = make(map[string]*K3dImageCatalog)
		if cache.http == nil {
			cache.http = &http.Client{Timeout: 15 * time.Second}
		}
		if cache.registry == nil {
			// 仅访问公开仓库，不读取本机 Docker 凭据或 Context。
			cache.registry = regclient.New(regclient.WithUserAgent("EDO/k3d-images"), regclient.WithRegOpts(reg.WithRetryLimit(1), reg.WithHTTPClient(cache.http)))
		}
	})
}

func (s *Service) K3dImages(ctx context.Context, actorID, source string) (*K3dImageCatalog, error) {
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	if _, err := s.authorize(ctx, actorID, "create"); err != nil {
		slog.WarnContext(ctx, "读取 k3d 镜像版本权限不足", "operation", "k3d_images_authorize", "actor_id", actorID, "err", err)
		return nil, err
	}
	if source == "" {
		source = "domestic"
	}
	if source != "domestic" && source != "dockerhub" {
		err := &ValidationError{Message: "请选择国内源或 Docker Hub 读取版本"}
		slog.WarnContext(ctx, "k3d 镜像来源无效", "operation", "k3d_images_source", "err", err)
		return nil, err
	}
	cache := s.k3dImages
	cache.init()
	request := cache.requests.DoChan(source, func() (any, error) {
		cache.mu.Lock()
		value := cache.entries[source]
		cache.mu.Unlock()
		if value != nil && time.Since(value.CheckedAt) < 5*time.Minute {
			return value, nil
		}
		readCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 40*time.Second)
		defer cancel()
		var result *K3dImageCatalog
		var err error
		if source == "domestic" {
			result, err = cache.domestic(readCtx)
		} else {
			result, err = cache.dockerHub(readCtx)
		}
		if err != nil {
			slog.ErrorContext(readCtx, "读取 k3d 镜像版本失败", "operation", "k3d_images_read", "source", source, "err", err)
			return nil, ErrK3dImageCatalog
		}
		cache.mu.Lock()
		cache.entries[source] = result
		cache.mu.Unlock()
		return result, nil
	})
	select {
	case <-ctx.Done():
		slog.WarnContext(ctx, "k3d 镜像版本请求已取消", "operation", "k3d_images_cancel", "err", ctx.Err())
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, ErrK3dImageCatalog
		}
		return nil, ctx.Err()
	case result := <-request:
		if result.Err != nil {
			return nil, result.Err
		}
		if _, err := s.authorize(ctx, actorID, "create"); err != nil {
			slog.WarnContext(ctx, "读取 k3d 镜像版本后权限失效", "operation", "k3d_images_reauthorize", "actor_id", actorID, "err", err)
			return nil, err
		}
		value := result.Val.(*K3dImageCatalog)
		copy := *value
		copy.Versions = append([]K3dImageVersion{}, value.Versions...)
		for index := range copy.Versions {
			copy.Versions[index].Platforms = slices.Clone(copy.Versions[index].Platforms)
		}
		return &copy, nil
	}
}

func sortK3dImageVersions(versions []K3dImageVersion) {
	slices.SortFunc(versions, func(a, b K3dImageVersion) int {
		av, _ := parseK3sVersion(a.Version)
		bv, _ := parseK3sVersion(b.Version)
		for _, pair := range [][2]int{{av.major, bv.major}, {av.minor, bv.minor}, {av.patch, bv.patch}, {av.revision, bv.revision}} {
			if pair[0] > pair[1] {
				return -1
			}
			if pair[0] < pair[1] {
				return 1
			}
		}
		return strings.Compare(a.Tag, b.Tag)
	})
}

func (cache *k3dImageCatalogCache) domestic(ctx context.Context) (*K3dImageCatalog, error) {
	r, _ := registryref.New(k3dDomesticRepository)
	list, err := cache.registry.TagList(ctx, r, scheme.WithTagLimit(500))
	if err != nil {
		return nil, err
	}
	tags, err := list.GetTags()
	if err != nil {
		return nil, err
	}
	result := &K3dImageCatalog{Source: "domestic", Repository: k3dDomesticRepository, Versions: []K3dImageVersion{}}
	for _, tag := range tags {
		if version := k3dTagVersion(tag); version != "" {
			result.Versions = append(result.Versions, K3dImageVersion{Version: version, Tag: tag, Image: k3dDomesticRepository + ":" + tag, Platforms: []string{}})
		}
	}
	sortK3dImageVersions(result.Versions)
	if len(result.Versions) > 100 {
		result.Versions = result.Versions[:100]
	}
	// 固定并发读取清单和小型配置，不下载镜像层，不为每个版本启动无界请求。
	var group sync.WaitGroup
	jobs := make(chan int)
	for range 4 {
		group.Go(func() {
			for index := range jobs {
				item := &result.Versions[index]
				platforms, err := cache.platforms(ctx, item.Image)
				if err != nil {
					slog.WarnContext(ctx, "读取国内 K3s 镜像架构失败", "operation", "k3d_image_platforms", "tag", item.Tag, "err", err)
					continue
				}
				item.Platforms = platforms
			}
		})
	}
	for index := range result.Versions {
		jobs <- index
	}
	close(jobs)
	group.Wait()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	result.Versions = slices.DeleteFunc(result.Versions, func(item K3dImageVersion) bool { return len(item.Platforms) == 0 })
	if len(result.Versions) == 0 && len(tags) > 0 {
		return nil, errors.New("镜像清单未返回可确认的稳定版本与架构")
	}
	result.CheckedAt = time.Now().UTC()
	return result, nil
}

func (cache *k3dImageCatalogCache) platforms(ctx context.Context, image string) ([]string, error) {
	r, err := registryref.New(image)
	if err != nil {
		return nil, err
	}
	m, err := cache.registry.ManifestGet(ctx, r)
	if err != nil {
		return nil, err
	}
	platforms := []string{}
	add := func(os, architecture, variant string) {
		if os != "linux" || !slices.Contains([]string{"amd64", "arm64", "arm"}, architecture) {
			return
		}
		value := os + "/" + architecture
		if architecture == "arm" && variant != "" {
			value += "/" + variant
		}
		if !slices.Contains(platforms, value) {
			platforms = append(platforms, value)
		}
	}
	if m.IsList() {
		index, ok := m.(manifest.Indexer)
		if !ok {
			return nil, errors.New("镜像索引格式不受支持")
		}
		entries, err := index.GetManifestList()
		if err != nil {
			return nil, err
		}
		for _, entry := range entries {
			if p := entry.Platform; p != nil {
				add(p.OS, p.Architecture, p.Variant)
			}
		}
	} else {
		image, ok := m.(manifest.Imager)
		if !ok {
			return nil, errors.New("镜像清单格式不受支持")
		}
		config, err := image.GetConfig()
		if err != nil {
			return nil, err
		}
		blob, err := cache.registry.BlobGetOCIConfig(ctx, r, config)
		if err != nil {
			return nil, err
		}
		p := blob.GetConfig()
		add(p.OS, p.Architecture, p.Variant)
	}
	slices.Sort(platforms)
	return platforms, nil
}

func (cache *k3dImageCatalogCache) dockerHub(ctx context.Context) (*K3dImageCatalog, error) {
	result := &K3dImageCatalog{Source: "dockerhub", Repository: k3dDockerHubRepository, Versions: []K3dImageVersion{}}
	// Hub 标签接口批量附带平台信息，避免逐版本查询仓库清单。
	next := k3dDockerHubTagsURL
	seen := map[string]bool{}
	for page := 0; next != "" && page < 3; page++ {
		if !strings.HasPrefix(next, "https://hub.docker.com/v2/namespaces/rancher/repositories/k3s/tags?") {
			return nil, errors.New("镜像标签分页地址无效")
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, next, nil)
		if err != nil {
			return nil, err
		}
		resp, err := cache.http.Do(req)
		if err != nil {
			return nil, err
		}
		var data struct {
			Next    string `json:"next"`
			Results []struct {
				Name   string `json:"name"`
				Images []struct {
					OS           string `json:"os"`
					Architecture string `json:"architecture"`
					Variant      string `json:"variant"`
				} `json:"images"`
			} `json:"results"`
		}
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			return nil, fmt.Errorf("标签接口响应状态 %d", resp.StatusCode)
		}
		err = json.NewDecoder(io.LimitReader(resp.Body, 4*1024*1024)).Decode(&data)
		resp.Body.Close()
		if err != nil {
			return nil, err
		}
		for _, tag := range data.Results {
			version := k3dTagVersion(tag.Name)
			if version == "" || seen[tag.Name] {
				continue
			}
			platforms := []string{}
			for _, image := range tag.Images {
				if image.OS != "linux" || !slices.Contains([]string{"amd64", "arm64", "arm"}, image.Architecture) {
					continue
				}
				p := "linux/" + image.Architecture
				if image.Architecture == "arm" && image.Variant != "" {
					p += "/" + image.Variant
				}
				if !slices.Contains(platforms, p) {
					platforms = append(platforms, p)
				}
			}
			if len(platforms) == 0 {
				continue
			}
			slices.Sort(platforms)
			seen[tag.Name] = true
			result.Versions = append(result.Versions, K3dImageVersion{Version: version, Tag: tag.Name, Image: k3dDockerHubRepository + ":" + tag.Name, Platforms: platforms})
		}
		next = data.Next
		if len(result.Versions) >= 60 {
			break
		}
	}
	sortK3dImageVersions(result.Versions)
	result.CheckedAt = time.Now().UTC()
	return result, nil
}
