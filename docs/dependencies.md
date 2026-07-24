# ZRT 第三方依赖决策

本文件记录通用能力的依赖选型。业务规则、权限边界和审计语义仍由 ZRT 定义；第三方包只负责经过验证的通用机制。

## 已采用

| 能力 | 依赖 | 采用原因 | ZRT 保留的边界 |
| --- | --- | --- | --- |
| 环境变量解析 | `github.com/caarlos0/env/v11` | 零运行时依赖，支持结构体标签、数值、布尔值和 `time.Duration`，替代手写类型转换 | 默认值、跨字段校验、显式空值检查和安全错误文案 |
| Git 远端引用与认证 | `github.com/go-git/go-git/v5` | 纯 Go、持续维护、支持 HTTPS Basic Auth、SSH 私钥和 `known_hosts` | 仓库地址白名单、不安全 HTTP 显式确认、凭据所有权和引用数量上限 |
| 密码摘要 | `github.com/matthewhartstonge/argon2` | Apache-2.0、持续维护、兼容标准 Argon2id PHC 格式 | 固定 Argon2id v1.3，并在计算前限制内存、迭代、并发、盐和摘要长度 |
| RBAC 判定 | `github.com/casbin/casbin/v3` | 成熟的 RBAC 模型、角色继承和 deny-override 判定 | 权限目录、超级管理员边界、个人密钥所有权和管理接口 |
| RBAC 多实例同步 | `github.com/casbin/redis-watcher/v2` | 使用现有 go-redis/v9 客户端广播策略失效事件 | GORM 表仍是唯一数据源；本实例先重载策略，再通知其他实例 |
| OCI 镜像仓库认证 | `github.com/regclient/regclient` | 成熟实现 OCI Distribution API、Basic/Bearer 认证挑战和 Registry 兼容处理 | 地址与 HTTP 安全校验、请求超时、权限审计、凭据加密和安全错误文案 |

`go-git` 替换外部 `git ls-remote` 后，运行镜像不再安装 `git` 和 `openssh-client`，SSH 私钥也不再写入临时文件。已有 Argon2id PHC 摘要不需要迁移。

## 评估后未采用

| 候选 | 结论 |
| --- | --- |
| `casbin/gorm-adapter` | 当前版本不能与 ZRT 的角色元数据共享同一个外部 GORM 事务。另建 `casbin_rule` 会形成双数据源并产生部分提交风险，因此使用只读适配器把现有事务表投影到 Casbin。 |
| `go-playground/webhooks` | 只覆盖 GitHub、GitLab、Gitea，不能完整覆盖 ZRT 必须支持的 Gitee 和通用 Git；接入后仍需维护两套签名和事件模型，不能降低复杂度。 |
| 通用 Redis 限流器 | ZRT 的登录限流只累计失败并在成功后清零，和普通请求频率限制语义不同；继续使用 Redis 原子计数与 TTL。 |
| 第三方 AES 封装 | 当前实现只是标准库 AES-256-GCM 的版本化信封和 AAD 绑定。额外依赖不会改善算法或密钥边界，反而扩大供应链。 |
| Gin 通用中间件合集 | 请求 ID、结构化访问日志和安全响应头都很短，并与 ZRT 的中文错误边界及 `slog` 字段约定绑定；整包替换收益低。 |

## 升级原则

- 依赖升级必须通过 `go test -race ./...`、`go vet ./...` 和前端生产构建。
- Git、认证、授权和加密依赖升级时必须额外验证现有数据兼容性及拒绝路径。
- 不采用预发布主版本；例如 `go-git/v6` 稳定前继续使用 v5 稳定线。
