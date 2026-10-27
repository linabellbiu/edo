# EDO 保护发布

仅操作现有 `/home/ubuntu/edo`、`/home/ubuntu/edo-runtime`、`edo.service` 与本地 `edo-debug-postgres-1`，不是通用部署或远程命令入口。脚本从 GitHub 拉取后的源码运行，不接收凭据，不上传源码或修改 Compose、Runner、Docker、防火墙。

发布目录保留 `revision`、`database-name`、`edo`、`edo.previous`、三份 `*.sha256` 和 `counts.sql` 制品约定。本地测试及构建通过后，在目录中记录命令与结果，并将对应提交的完整 SHA 写入 `validation.ok`，不要求 GitHub Actions。兼容已有的 `ci.ok`，仅在同 SHA 完整 CI 确实成功时填写，不能用它代替本地验证记录。`counts.sql` 只允许由操作者生成并核验的只读计数语句，文件不得来自业务请求。

先重新采集当前运行状态，不复用回滚前的旧 PID：

```sh
bash tools/release-activate/activate.sh capture '<release绝对路径>'
```

这一步只读服务、数据库计数和进程/容器状态，并在 release 内新建 `700` 权限的 `capture.*` 私有证据目录；不会停止或激活服务。当前运行程序、磁盘程序必须都与 `edo.previous` 相同。Runner 身份使用启动 ID、PID、comm 和内核 starttick，不依赖可能受时钟换算影响的 `ps lstart`。

确认同一候选和证据后显式激活：

```sh
bash tools/release-activate/activate.sh activate '<release绝对路径>' '<capture绝对路径>'
```

需要终端 sudo 认证时，最后加 `--interactive`；不会接收聊天密码。两种模式均保留 `flock`、干净 `main`、源码 HEAD/版本/验证凭据一致、候选 VCS 未修改、配置和程序摘要校验及在途任务检查。GitHub 拉取新 Commit 后必须重新构建并验证对应候选，不能沿用旧 SHA 的程序或验证凭据。

激活只停止 EDO，停止后创建新的 PostgreSQL 备份并通过 `pg_restore -l` 验证，再原子替换程序。检查 readiness、实际进程版本及摘要、配置未变、其他容器与 Runner 未变化，私有文件记录数据库计数和服务状态。失败自动尝试恢复上一程序并检查健康，不自动恢复数据库，不删除备份；回滚失败必须人工处理。进程及容器基线不是数据库恢复演练或完整业务验收，发布后仍须另行核对 API/UI。

验证：`bash -n tools/release-activate/activate.sh`、`go test -race ./tools/release-activate`。本机测试使用合成 procfs 和 Docker 命令桩，覆盖稳定进程身份、PID 复用、缺失证据、摘要失败、Docker 环境隔离与既有保护契约，不会停止真实服务；它们不替代服务器上的备份恢复演练和实际发布验收。macOS 仅运行这些隔离测试；发布脚本依赖 Linux procfs、既有 Bash、systemd、Docker、PostgreSQL 客户端工具、Git、Go 和 GNU coreutils，不引入新运行组件。
