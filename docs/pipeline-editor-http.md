# EDO 流水线 HTTP 访问回归

普通 HTTP 地址下浏览器不提供 `crypto.randomUUID()`，原先新增阶段、任务及打开发布计划直接调用该方法，点击后抛出异常。ID 生成改用 `uuid` 的 v4 实现，保留阶段和任务前缀；UUID 的权限、业务归属和服务端校验不变。

本轮只检查受影响的新增和保存流程，不执行真实构建或部署任务。

| ID | 角色与前置 | 操作 | 预期 |
| --- | --- | --- | --- |
| PIPE-HTTP-01 | 有流水线修改权限；通过普通内网 HTTP 访问 | 在代码源后、已有阶段后分别添加阶段，给新阶段添加任务 | 新阶段和任务正常生成，不报 randomUUID 错误 |
| PIPE-HTTP-02 | 同一角色；阶段内已有一个任务 | 连续从任务库添加三个并行任务，关闭面板再打开，等待草稿保存并刷新 | 同一阶段保留四个不同任务，始终可继续新增，保存后顺序与 ID 不变 |
| PLAN-HTTP-01 | 有发布计划执行权限；可执行计划 | 打开执行窗口、取消后重新打开 | 正常打开；每次确认使用独立 UUID，打开窗口本身不执行发布 |

自动回归覆盖应用流水线与项目共用流水线。浏览器夹具为 `/tests/fixtures/pipeline-editor-http.html`，挂载真实编辑器，只将合成草稿存入浏览器 Session Storage，拒绝其他接口和执行操作。通过非回环 HTTP 地址检查 `isSecureContext=false`、`crypto.randomUUID` 缺失的实际浏览器路径；不能用 localhost 的安全上下文代替。

## 2026-11-05 本地结果

- PIPE-HTTP-01、PIPE-HTTP-02：通过。真实浏览器的 `isSecureContext=false`、`randomUUID=undefined`、`getRandomValues=function`。实际新增阶段，连续添加 Shell、单元测试、质量扫描、人工放行四个任务，自动保存后刷新，任务数量、顺序和 ID 均保留，仍可继续添加。
- PLAN-HTTP-01：组件回归通过。模拟缺少 randomUUID 的环境，打开、取消、重新打开均正常，确认请求 ID 为不同的合法 UUID；没有提交真实发布。
- 基于已提交源码加本次补丁的独立副本，前端 1643 项回归及类型检查、生产构建通过。测试命令为 `node --no-experimental-webstorage --test web/tests/*.test.mjs`；关闭 Node.js 24 的实验性全局 Web Storage，浏览器与产品配置不变。未关闭时既有测试的 localStorage 桩与 Node 全局对象冲突，失败日志另存，未将其判作产品缺陷。

本轮没有执行真实构建任务，也没有修改用户已有流水线。
