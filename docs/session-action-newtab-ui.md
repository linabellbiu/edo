# 日志与终端入口：角标与 Ctrl/⌘+点击

日志、终端（Exec）、流水线日志等流式入口统一使用 `web/src/components/SessionAction.vue`：把原先并排的方形箭头按钮收进动作按钮内部右上角，形成一个角标，不再单独占一个按钮位。

## 规则

- 一个动作只有一个按钮框：按钮本体负责打开抽屉/当前页，右上角角标表示"可新标签打开"。
- 角标是真实链接（`target="_blank"`、`rel="noopener noreferrer"`），可点击、可 Tab 聚焦，带独立可访问名称；按钮本体不再重复绑定点击。
- 悬停提示统一为「Ctrl/⌘+点击在新标签页打开日志（或终端）：目标名称」；macOS 上 Ctrl+点击会被识别为右键，因此同时拦截带 Ctrl 的 `contextmenu`，普通右键保持系统菜单。
- Ctrl 或 ⌘ + 点击按钮直接在新标签打开同一目标；普通点击仍走原抽屉与当前页操作，权限与禁用语义不变；带 Alt/Shift 的组合不改变默认动作。
- 禁用或目标无效（未登记路由、缺少必要身份）时不显示角标和提示，按钮原样保持禁用。
- 主按钮（primary/dangerous）上的角标改用浅色，保证在深色底上的可读性。
- 抽屉标题、菜单项、折叠摘要等没有按钮框的位置仍使用 `SessionPopoutLink`，但改为无描边、无底色的次要色小图标，悬停时显示靛蓝。

## 验证

```bash
node --test web/tests/session-action-newtab.test.mjs
cd web && npm run dev   # 打开 /tests/fixtures/session-action.html（支持 ?theme=dark）
```

夹具挂载真实 `SessionAction`，覆盖表格行、禁用、不同按钮尺寸与类型，并记录新标签打开结果。
