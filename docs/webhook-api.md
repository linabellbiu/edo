# ZRT 外部 Git Webhook API

ZRT 可以接收普通 Git、GitHub、GitLab、Gitea 和 Gitee 的代码事件。此入口默认关闭，管理员需要先在“系统设置”中开启“Git Webhook API”，再在具体代码仓库中启用 Webhook。

## 接入条件

1. 在“系统设置”中开启全局 Git Webhook API。
2. 在“代码仓库”中创建或编辑仓库并启用 Webhook。
3. 使用仓库卡片的“查看 Webhook”取得该仓库的路径和签名密钥。
4. 在 Git 平台配置公网可访问的 HTTPS 地址。完整地址由 ZRT 的外部访问域名和以下路径组成：

```text
/api/v1/webhooks/git/{repository_id}
```

Webhook 请求不使用登录会话，但每次请求都必须通过对应平台的签名或 Token 校验。请求体必须是 JSON，最大为 2 MiB。

## 支持的事件

| Git 事件 | ZRT 标准事件 | 说明 |
| --- | --- | --- |
| 分支 Push | `branch_push` | `ref` 必须以 `refs/heads/` 开头 |
| Tag Push | `tag_push` | `ref` 必须以 `refs/tags/` 开头 |
| Pull Request / Merge Request | `pull_request` | 使用目标分支和来源提交版本触发流水线 |

Git 平台的 Ping、测试请求或其他未支持事件返回 `204 No Content`，不会创建任务。

## 平台请求头

| 平台 | 事件头 | 投递 ID | 签名或 Token |
| --- | --- | --- | --- |
| GitHub | `X-GitHub-Event` | `X-GitHub-Delivery` | `X-Hub-Signature-256: sha256=<HMAC-SHA256>` |
| GitLab | `X-Gitlab-Event` | `X-Gitlab-Event-UUID` | `X-Gitlab-Token: <secret>` |
| Gitea | `X-Gitea-Event` | `X-Gitea-Delivery` | `X-Gitea-Signature: <HMAC-SHA256>` |
| Gitee | `X-Gitee-Event` | `X-Gitee-Delivery` | `X-Gitee-Token: <secret>` |
| 普通 Git | `X-ZRT-Event` | `X-ZRT-Delivery` | `X-ZRT-Signature-256: sha256=<HMAC-SHA256>` |

HMAC-SHA256 必须针对收到的原始请求体计算，不能先解析再重新序列化。投递 ID 用于去重；普通 Git 未提供投递 ID 时，ZRT 使用请求体摘要作为稳定 ID。

## 普通 Git 请求格式

分支或 Tag Push 使用 `push` 事件：

```http
POST /api/v1/webhooks/git/{repository_id}
Content-Type: application/json
X-ZRT-Event: push
X-ZRT-Delivery: 8da1c56f-0a54-4b88-8bd5-b4a978302c89
X-ZRT-Signature-256: sha256=<hex digest>

{
  "ref": "refs/heads/main",
  "after": "0123456789012345678901234567890123456789",
  "head_commit": {
    "message": "发布 1.4.0"
  }
}
```

Tag Push 只需把 `ref` 改为 `refs/tags/v1.4.0`。删除分支或 Tag 时平台通常把提交版本设为全零，ZRT 会保留事件但不把全零值当作可发布版本。

PR 使用 `pull_request` 事件：

```http
POST /api/v1/webhooks/git/{repository_id}
Content-Type: application/json
X-ZRT-Event: pull_request
X-ZRT-Delivery: 4cd49e26-8e60-41e4-8cc0-19ae0c43fe43
X-ZRT-Signature-256: sha256=<hex digest>

{
  "pull_request": {
    "head": { "sha": "abcdefabcdefabcdefabcdefabcdefabcdefabcd" },
    "base": { "ref": "main" }
  }
}
```

## 响应

成功接收后返回 `202 Accepted`：

```json
{
  "delivery_id": "ZRT 内部投递记录 ID",
  "job_id": "任务 ID",
  "duplicate": false
}
```

相同仓库和投递 ID 的重复请求不会重复创建任务，响应中的 `duplicate` 为 `true`。常见状态码如下：

- `204`：事件不受支持，已安全忽略。
- `401`：签名或 Token 校验失败。
- `404`：全局入口关闭、仓库不存在或仓库 Webhook 未启用。响应不会区分具体原因。
- `413`：请求体超过 2 MiB。
- `503`：ZRT 暂时无法读取全局开关，入口按失败关闭处理。

Webhook 通过 Transactional Outbox 和 NATS JetStream 进入任务队列，签名通过不等于发布一定执行。应用和流水线仍会根据仓库、目标分支、事件类型、启用状态与审批规则决定后续操作。
