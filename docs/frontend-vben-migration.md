# Vben 前端迁移说明

## 调整原因

旧前端使用 React、React Router 和 Zustand，页面外壳、表单、表格、主题与动效由项目分散维护，无法直接复用 Vben Admin 的 Vue 生态和统一交互。前端现已整体替换为 Vue 3 单页应用，并以 Vben Admin 5 `web-antd` 为界面与工程基线。

## 新架构

- Vue 3 + TypeScript + Vite。
- Vue Router 负责路由、面包屑和页面历史标签。
- Pinia 负责登录用户、系统状态、主题、语言和标签状态。
- Ant Design Vue 负责表单、表格、弹层、提示和全局主题。
- Vue I18n 同步应用外壳语言与 Ant Design Vue Locale，默认简体中文。
- VueUse Motion 与 CSS Transition 负责路由切换和局部动效，统一遵循 `prefers-reduced-motion`。
- Lucide Vue Next 提供统一线性图标。

应用继续保留 `web/` 单包、npm、现有 Dockerfile 和 Go 静态资源嵌入方式，没有引入 Vben 的 pnpm/Turbo Monorepo。这样可以对齐 Vben 的运行时和交互，同时避免改变后端部署拓扑。

## 行为变化

- 登录后固定为全高侧栏、紧凑 Header、路径面包屑、历史标签和浅灰内容区。
- 页面操作统一进入紧凑工具栏，表单、表格、抽屉和弹层统一使用 Ant Design Vue。
- 主题和语言入口固定在右上角；主题、语言及侧栏折叠状态保存在浏览器本地。
- 流水线方案改用类似 Gitee 的固定编排：一个代码源、横向分栏阶段、阶段内横向任务链。阶段和任务可拖动排序，并可通过阶段边界或任务之间的“＋”就地插入；属性和任务库使用右侧抽屉。执行语义直接来自 `source + stages[].tasks[]` 的数组顺序，不再使用端口连线、节点坐标、缩放或无限画布。
- 流水线模型已改为构建产出不可变制品、部署只消费制品；旧画布节点和旧运行数据不提供兼容执行路径。

## 兼容性

旧 React 运行时、TSX 页面、React Router、Zustand 和旧样式入口已删除。浏览器中已有登录会话仍然有效；旧页面 URL 通过 Vue Router 重定向到新的页面结构。历史主题键 `edo.theme` 继续复用，用户不需要重新选择主题。旧流水线图、节点定义和运行快照不迁移到新编辑器。

## 验证

```bash
cd web
npm install
npm run build
```

生产构建会先运行 `vue-tsc` 严格类型检查，再由 Vite 生成供 Go 服务嵌入的 `web/dist`。
