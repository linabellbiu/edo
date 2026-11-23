# 参与贡献

感谢你愿意改进 EDO。下面是提交改动前需要知道的约定。

## 提交信息

提交信息使用 Conventional Commits 的前缀，与现有历史保持一致：

```text
fix(web): 统一资源列表按钮并收紧 Secret 引用列
feat(kubernetes): 独立管理命名空间及关联资源
```

常用前缀：`feat`、`fix`、`refactor`、`style`、`docs`、`test`、`ci`。括号内写影响范围，如 `web`、`kubernetes`、`pipeline`。

## 改动前后要做的事

1. 改动行为时，同时更新或新增测试。后端测试与代码放在同一个包内，文件名以 `_test.go` 结尾。
2. 改动影响部署、升级或数据库结构时，更新 `docs/` 下对应的说明文档。
3. 数据库结构变化只能通过 `internal/database/migrate.go` 追加新版本，**不要修改已经发布的迁移版本**。

## 本地检查

CI 实际执行的检查命令（见 `.github/workflows/ci.yml`）：

```bash
go vet ./...
go test -race -count=1 ./...
npm ci --prefix web
node --test web/tests/*.test.mjs
npm run build --prefix web
```

前端测试依赖 `@vue/compiler-sfc` 等未在 `web/package.json` 显式声明的包，请以 CI 结果为准。

## 文档

- 文档索引见 [docs/README.md](docs/README.md)。
- `docs/acceptance/` 与 `docs/*-acceptance*.md` 是历史验收记录，不代表当前行为。新增验收记录请放入该目录，并在文件中写明基线提交。

## 提交 Pull Request

请在 PR 描述中说明：改动动机、验证方式（运行了哪些测试、是否在真实环境验证）、以及未验证的部分。
