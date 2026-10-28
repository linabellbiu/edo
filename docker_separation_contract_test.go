package main

import (
	"os"
	"slices"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

type separationComposeService struct {
	Image       string                                       `yaml:"image"`
	Build       struct{ Context, Dockerfile, Target string } `yaml:"build"`
	Environment map[string]string                            `yaml:"environment"`
	Command     []string                                     `yaml:"command"`
	Ports       []string                                     `yaml:"ports"`
	Volumes     []string                                     `yaml:"volumes"`
	Networks    []string                                     `yaml:"networks"`
	DependsOn   map[string]struct{ Condition string }        `yaml:"depends_on"`
	ReadOnly    bool                                         `yaml:"read_only"`
	Init        bool                                         `yaml:"init"`
	User        string                                       `yaml:"user"`
	SecurityOpt []string                                     `yaml:"security_opt"`
	CapDrop     []string                                     `yaml:"cap_drop"`
	Tmpfs       []string                                     `yaml:"tmpfs"`
	Healthcheck struct{ Test []string }                      `yaml:"healthcheck"`
}

func separationReadFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取分离部署配置失败: path=%s err=%v", path, err)
	}
	return string(data)
}

func separationComposeServices(t *testing.T, path string) map[string]separationComposeService {
	t.Helper()
	var config struct {
		Services map[string]separationComposeService
	}
	if err := yaml.Unmarshal([]byte(separationReadFile(t, path)), &config); err != nil {
		t.Fatalf("解析分离部署配置失败: path=%s err=%v", path, err)
	}
	return config.Services
}

func TestProductionComposeSeparatesWebAndAPI(t *testing.T) {
	services := separationComposeServices(t, "docker-compose.yml")
	for _, name := range []string{"api", "migrate", "web", "redis", "nats", "docker-builder"} {
		if _, ok := services[name]; !ok {
			t.Fatalf("默认部署缺少必要服务: %s", name)
		}
	}
	api, migrate, web, builder := services["api"], services["migrate"], services["web"], services["docker-builder"]
	if api.Image == "" || api.Image != migrate.Image || !slices.Equal(api.Command, []string{"server"}) || !slices.Equal(migrate.Command, []string{"migrate"}) {
		t.Fatal("迁移与单进程 API 必须使用同一后端镜像，不能另建 Worker")
	}
	if migrate.Build.Context != "." || migrate.Build.Dockerfile != "Dockerfile" || web.Build.Context != "." || web.Build.Dockerfile != "web/Dockerfile" || web.Image == api.Image {
		t.Fatal("正式 Web 与 API 必须分别构建、使用独立镜像")
	}
	for name, service := range services {
		if name != "web" && len(service.Ports) != 0 {
			t.Errorf("默认部署只允许 Web 映射宿主机入口: service=%s ports=%v", name, service.Ports)
		}
	}
	if !slices.Equal(web.Ports, []string{"127.0.0.1:${EDO_HTTP_PORT:-8080}:8080"}) {
		t.Fatalf("Web 必须保留现有 HTTP 端口并默认只监听回环: %v", web.Ports)
	}
	if _, ok := api.Environment["EDO_WEB_ROOT"]; ok || api.Environment["EDO_SERVER_ADDRESS"] != ":8080" {
		t.Fatal("正式 API 只保留内部 8080 入口，不再配置前端静态目录")
	}
	for name, value := range map[string]string{
		"EDO_DATABASE_DRIVER":        "${EDO_COMPOSE_DATABASE_DRIVER:-sqlite}",
		"EDO_DATABASE_DSN":           "${EDO_COMPOSE_DATABASE_DSN:-/app/data/edo.db}",
		"EDO_REDIS_URL":              "${EDO_COMPOSE_REDIS_URL:-redis://redis:6379/0}",
		"EDO_NATS_URL":               "${EDO_COMPOSE_NATS_URL:-nats://nats:4222}",
		"EDO_SERVER_TRUSTED_PROXIES": "${EDO_COMPOSE_SERVER_TRUSTED_PROXIES:-}",
	} {
		if api.Environment[name] != value || migrate.Environment[name] != value {
			t.Errorf("前后端分离不得改变根 .env 的连接映射: %s", name)
		}
	}
	if !strings.HasPrefix(api.Environment["EDO_SECRETS_KEY"], "${EDO_SECRETS_KEY:?") {
		t.Fatal("后端缺少根 .env 密钥时必须失败，不允许临时密钥")
	}
	if web.DependsOn["api"].Condition != "service_healthy" || api.DependsOn["migrate"].Condition != "service_completed_successfully" {
		t.Fatal("Web 启动必须等待 API 健康，API 必须等待迁移成功")
	}
	if !web.ReadOnly || !web.Init || web.User != "101:101" || !slices.Contains(web.SecurityOpt, "no-new-privileges:true") || !slices.Contains(web.CapDrop, "ALL") || !slices.Contains(web.Tmpfs, "/tmp:size=64m,mode=1777") {
		t.Fatal("Web 必须非 root、只读且只在 tmpfs 写入临时文件")
	}
	if len(web.Volumes) != 0 || len(web.Environment) != 0 || !slices.Equal(web.Networks, []string{"edo"}) || !strings.Contains(strings.Join(web.Healthcheck.Test, " "), "http://127.0.0.1:8080/healthz") {
		t.Fatal("Web 不得持有后端密钥、数据卷或构建网络，并且必须提供健康检查")
	}
	if api.Environment["EDO_DOCKER_BUILDER_HOST"] != "tcp://docker-builder:2376" || api.Environment["EDO_DOCKER_BUILDER_TLS_CERT_PATH"] != "/certs/client" || !slices.Contains(api.Volumes, "edo-builder-client-certs:/certs/client:ro") || builder.Environment["DOCKER_TLS_CERTDIR"] != "/certs" {
		t.Fatal("API 到隔离构建器的 mTLS 与只读客户端证书不得降级")
	}
	if !slices.Contains(api.Volumes, "./data:/app/data") || !slices.Contains(migrate.Volumes, "./data:/app/data") || !slices.Equal(builder.Networks, []string{"edo-builder"}) {
		t.Fatal("数据库持久化与构建器网络隔离必须保留")
	}
}

func TestDockerAPIRuntimeDoesNotDependOnWebBuild(t *testing.T) {
	dockerfile := separationReadFile(t, "Dockerfile")
	parts := strings.Split(dockerfile, "FROM debian:bookworm-slim AS api-runtime\n")
	if len(parts) != 2 {
		t.Fatal("默认最终阶段必须是独立 API 运行时")
	}
	for _, required := range []string{
		"FROM golang:1.26.10-bookworm AS go-source",
		"FROM go-source AS go-builder",
		"COPY --from=go-builder --chown=edo:edo /out/edo /app/edo",
	} {
		if !strings.Contains(dockerfile, required) {
			t.Errorf("缺少 API 构建契约: %s", required)
		}
	}
	for _, forbidden := range []string{"FROM node:", "RUN npm", "--from=web-builder", "-tags=edo_web", "go-release-builder", "EDO_WEB_ROOT"} {
		if strings.Contains(dockerfile, forbidden) {
			t.Errorf("默认 API 构建不得依赖前端产物或内嵌标签: %s", forbidden)
		}
	}
	if strings.Contains(parts[1], "npm") || strings.Contains(parts[1], "node:") {
		t.Fatal("API 运行层不得携带前端工具链")
	}
	_, goSource, ok := strings.Cut(parts[0], "FROM golang:1.26.10-bookworm AS go-source\n")
	if !ok {
		t.Fatal("缺少 Go 源码构建阶段")
	}
	goSource, _, ok = strings.Cut(goSource, "FROM go-source AS go-builder")
	if !ok {
		t.Fatal("缺少 Go 二进制构建阶段")
	}
	if !strings.Contains(goSource, "COPY cmd/ ./cmd/") || !strings.Contains(goSource, "COPY internal/ ./internal/") {
		t.Fatal("Go 构建层必须显式复制后端编译源码")
	}
	for _, forbidden := range []string{"COPY . .", "COPY web", "COPY deploy", "COPY README"} {
		if strings.Contains(goSource, forbidden) {
			t.Errorf("Go 编译层不得因无关前端或文档变化失效: %s", forbidden)
		}
	}
	dev := separationComposeServices(t, "deploy/compose.dev.yml")
	if dev["api"].Build.Dockerfile != "Dockerfile" || dev["api"].Build.Target != "go-builder" || dev["web"].Build.Dockerfile != "web/Dockerfile" || dev["web"].Build.Target != "web-builder" || !slices.Equal(dev["web"].Ports, []string{"127.0.0.1:${EDO_WEB_PORT:-5173}:5173"}) || !slices.Equal(dev["api"].Ports, []string{"127.0.0.1:${EDO_HTTP_PORT:-8080}:8080"}) {
		t.Fatal("正式分离部署不得改变 Vite 开发模式的构建目标或端口")
	}
}

func TestWebImageUsesIndependentNonRootRuntime(t *testing.T) {
	dockerfile := separationReadFile(t, "web/Dockerfile")
	parts := strings.Split(dockerfile, "FROM nginx:1.30.6-alpine AS web-runtime\n")
	if len(parts) != 2 {
		t.Fatal("Web 必须使用版本固定的 NGINX 官方稳定镜像")
	}
	for _, required := range []string{"FROM node:24.16.0-bookworm-slim AS web-builder", "RUN npm ci", "RUN npm run build", "COPY web/nginx.conf /etc/nginx/nginx.conf", "COPY --from=web-builder /src/web/dist/ /usr/share/nginx/html/", "USER 101:101", "ENTRYPOINT [\"nginx\"]", "CMD [\"-g\", \"daemon off;\"]"} {
		if !strings.Contains(dockerfile, required) {
			t.Errorf("缺少 Web 镜像隔离契约: %s", required)
		}
	}
	if strings.Contains(parts[1], "/app/edo") || strings.Contains(parts[1], "npm") || strings.Contains(parts[1], "node:") {
		t.Fatal("Web 运行镜像不得携带后端二进制或 Node 运行时")
	}
}

func TestWebProxyKeepsStreamingAndCacheBoundaries(t *testing.T) {
	config := separationReadFile(t, "web/nginx.conf")
	for _, required := range []string{
		"pid /tmp/nginx.pid;", "client_body_temp_path /tmp/client_temp;", "proxy_temp_path /tmp/proxy_temp;",
		"resolver 127.0.0.11 valid=10s ipv6=off;", "server api:8080 resolve;",
		"include /etc/nginx/mime.types;", "client_max_body_size 0;", "proxy_http_version 1.1;",
		"proxy_set_header Host $http_host;", "proxy_set_header Upgrade $http_upgrade;", "proxy_set_header Connection $connection_upgrade;",
		"proxy_set_header X-Real-IP $remote_addr;", "proxy_set_header X-Forwarded-For $remote_addr;", "proxy_set_header Forwarded \"\";",
		"proxy_request_buffering off;", "proxy_buffering off;", "proxy_cache off;", "proxy_pass_request_headers on;", "proxy_next_upstream off;",
		"proxy_read_timeout 25h;", "proxy_send_timeout 25h;", "location = /api {", "location ^~ /api/ {", "proxy_pass http://edo_api;",
		"location ^~ /assets/ {", "try_files $uri =404;", "add_header Cache-Control $asset_cache_control always;",
		"default 'no-store';", "200 'public, max-age=31536000, immutable';", "304 'public, max-age=31536000, immutable';",
		"try_files $uri /index.html;", "add_header Cache-Control 'no-store' always;", "add_header X-Content-Type-Options nosniff always;",
	} {
		if !strings.Contains(config, required) {
			t.Errorf("缺少代理、流式输出或缓存契约: %s", required)
		}
	}
	for _, forbidden := range []string{"proxy_pass http://edo_api/;", "proxy_set_header Cookie", "proxy_set_header Authorization", "proxy_set_header Origin", "proxy_set_header Sec-WebSocket-Protocol", "$proxy_add_x_forwarded_for", "$request ", "$request_uri", "$args", "$http_cookie", "$http_authorization"} {
		if strings.Contains(config, forbidden) {
			t.Errorf("代理不得重写认证信息、裁掉 API 前缀或记录敏感查询: %s", forbidden)
		}
	}
}
