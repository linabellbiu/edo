# 弹层交互一致性（ESC / 遮罩 / 关闭按钮）

EDO 的弹窗与抽屉统一走 Ant Design Vue 的 `a-modal` / `a-drawer`，交互规则如下。新增弹层必须满足，`web/tests/interaction-esc.test.mjs` 会静态检查。

## 1. 关闭路径

- **ESC、右上角 ×、点击遮罩、取消按钮走同一条关闭逻辑**，都触发组件自己的 `@close` / `@cancel`；存在草稿确认、未保存提示的弹层，ESC 同样会先走确认，不是绕过入口。
- 阻断态（保存中、连接测试中、安装执行中、批量写操作进行中）时，`closable`、`mask-closable`、`keyboard` 使用**同一个条件**一起禁用，不允许出现「× 灰了但 ESC 还能关」或相反。
- 嵌套弹层只关闭最上层；底层弹层不因上层 ESC 一起消失。
- 关闭后焦点回到触发按钮；命令面板（全局搜索）用 ESC 关闭。

## 2. 例外

- **终端输入面**（`TerminalDrawer`）保留 `keyboard=false`：ESC 属于 shell（vim、htop 等），关闭终端应使用右上角 ×、程序栏或快捷键，避免误杀前台程序。
- 终端内的文件编辑器弹窗仍可用 ESC 关闭，但保存中禁用。

## 3. 已统一的位置

本轮补齐了只控制 ×/遮罩、漏掉 ESC 的弹层：主机、Docker 连接、集群关联、K3s 安装、节点加入、集群 RBAC、K8s 节点移除、扩缩容、发布计划编辑器、发布计划添加应用、组织与部门、项目表单、回滚确认、终端文件编辑器；命令面板显式声明 ESC 关闭。

## 4. 测试

```sh
node --test web/tests/interaction-esc.test.mjs
```

检查项：`closable` / `mask-closable` 受条件控制时必须声明 `:keyboard`；除终端输入面外不允许永久 `:keyboard="false"`。运行时行为（焦点恢复、草稿确认、嵌套层级）仍需在浏览器中按页面验收。
