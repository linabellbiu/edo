# 部署镜像输入框被默认插槽覆盖的修复（2026-11-17）

## 现象

部署配置（Kubernetes 部署任务/部署方案）里「镜像」只剩 label，输入框整块消失；右侧「镜像 Tag」正常显示 `${EDO_IMAGE_TAG}`。

## 原因

`7a81ce02` 给「镜像」的 `a-auto-complete` 加了 `<template #default="{value}">{{ value }}</template>`。ant-design-vue 的 AutoComplete 会把默认插槽的第一个子节点当作自定义输入元素：`getInputElement()` 执行 `slots.default.call(slots)`，**不传插槽 props**；SFC 编译出的插槽按 `({ value })` 解构 `undefined`，抛 `TypeError: Cannot destructure property 'value' of 'undefined'`，该控件渲染失败，只剩 form-item 的 label。

复现（真实 ant-design-vue + `@vue/server-renderer`，无浏览器 DOM）：

- 无插槽：`inputs: 1`，无 `ascustomize-input`
- `({ value } = {})`：`inputs: 1`（空文本被丢弃，走默认输入框）
- `({ value }) => [text]`（SFC 编译结果）：`THROWS: Cannot destructure property 'value' of 'undefined'`

## 修复

- `DeploymentConfigurationFields.vue`：移除该默认插槽，改为自闭合 `a-auto-complete`。上游构建产物选项（`kubeImageOptions`，含“非产物镜像”提示与已保存值回显）不变。
- 新增 `web/tests/autocomplete-input-slot.test.mjs`：
  - 全量扫描 `web/src/**/*.vue`，任何 `a-auto-complete` 带默认插槽即失败，用测试注释说明 antd 行为；
  - 固定「部署镜像」字段为自闭合、保留 `:options="kubeImageOptions"` 与 `placeholder="选择上游构建产物"`。

## 验证

- `node --test tests/autocomplete-input-slot.test.mjs tests/deployment-configuration.test.mjs`：17 项通过。
- 相关子集（pipeline*、deployment*、workflow-build-images）：267 项中 255 通过，12 项失败均为 `pipeline-log-focus` / `pipeline-log-layout` 既存老用例（日志抽屉改 SessionPanel 后未同步），与本次无关。
- `vue-tsc --noEmit --skipLibCheck` 通过。
- 部署后需在页面确认「镜像」输入框恢复（本次浏览器扩展离线，未做点击验收）。
