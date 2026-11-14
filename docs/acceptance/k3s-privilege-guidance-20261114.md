# K3s 发行版及安装权限检测验收

检测只读使用已固定 SSH 指纹及当前项目的已存凭据；复用官方 `x/crypto/ssh` 与系统 `id`、`sudo`。发行版只读取 os-release 登记字段并映射安全名称，不执行其内容，不按发行版或组名推定 sudo 权限，不新增依赖。

| ID | 角色与前置 | 操作 | 预期 | 结果 |
| --- | --- | --- | --- | --- |
| PG-01 | 有安装权限，Ubuntu/Debian 或 CentOS/RHEL 系普通账号 | 检测免密 sudo、保存密码 sudo、无 sudo、验证失败 | 展示真实发行版与权限状态；未验证或不可用阻断，给出对应引导 | 通过 |
| PG-02 | 同权限，root 或 EDO 未启用提权 | 检测并尝试生成计划 | root 真实 UID 通过；账号可 sudo 但开关未启用仍阻断，不创建安装任务 | 通过 |
| PG-03 | 错项目、撤权、无权限、迟到响应 | 检测或编辑提示中的主机 | SSH 前鉴权；不能查看或打开错误目标，迟到响应不恢复旧引导 | 通过 |
| PG-04 | 浅色、深色、窄屏前端夹具 | 点击检测、打开主机配置后返回 | 准确主机、发行版、原因及步骤可读；保留安装草稿，不自动开启 sudo、保存或安装 | 通过 |

真实目标只读检测结果另行记录；受控 SSH 对端和前端夹具不冒充实际主机安装成功。

验证证据：

- 完整 `go test -race -timeout 5m -json ./internal/k3s` 通过，180 个顶层测试，用时 195.929 秒；最后的文案、状态一致性和取消保护另经 `TestPrivilege` / `TestSSHTransportSudo` 定向竞争检测通过。
- `TestPrivilegeProbeUsesRealPinnedSSHAndBoundedCredentialInput` 使用本机真实 SSH 对端和受控 sudo，验证 root、免密、独立密码、未授权使用密码、没有可用密码、sudo 不存在与授权拒绝；发行版读取用真实 Shell 对 os-release 夹具执行，验证 Ubuntu、CentOS、Debian、Rocky 及不执行文件内容。不是只检查页面开关。
- 独立数据库的 HTTP 预检/计划及原有鉴权与跨项目定向竞争检测通过，明确断言鉴权失败前不进行权限探针。新检测失败返回报告；计划仍被阻断，无任务、锁或凭据写入。
- 前端 90 个定向测试及生产构建通过。页面实际点击检测、打开准确主机配置并取消，安装草稿保留；sudo 不自动勾选，无计划或安装请求。深色 390px 下长引导无横向溢出，浏览器无脚本错误。
- 截图：`/tmp/edo-privilege-ubuntu.png`、`/tmp/edo-privilege-centos-dark-mobile.png`、`/tmp/edo-privilege-host-config.png`。日志：`/tmp/edo-privilege-full-test.jsonl`、`/tmp/edo-privilege-http-final.log`、`/tmp/edo-privilege-web-final.log`。

范围限制：未修改用户主机的 sudo / sudoers、安装软件或执行 K3s 安装；未把隔离对端或夹具结果当作用户 vps 的实际权限状态。
