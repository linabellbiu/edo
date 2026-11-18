# 侧栏导航设计规范

侧栏承载全站一级入口，样式与信息组织统一在 `web/src/components/SidebarNavigation.vue`（结构）与 `web/src/styles/vben.css`（视觉变量）维护，`AppLayout.vue` 只负责权限过滤、选中与展开状态。`web/tests/sidebar-navigation-design.test.mjs` 校验本文的规则。

## 信息架构

- 侧栏只渲染一级分组（分支）与其下条目，不再显示“持续交付”“平台管理”这类更大范围的标题。
- 分组内的条目按职责从上到下排列：系统配置 → 运行观测 → 后台任务 → 账号与权限 → 审计。
- 同属一个权限族（例如 `monitor.read` 的系统监控与日志）的入口必须相邻。
- 同一页面的多个视图在侧栏按页面内页签顺序列出（访问控制：用户管理 → 角色与权限 → 审计日志），侧栏文字必须与页签文字一致，不得出现两套名称。
- 分组内条目的图标互不重复，也不复用分组图标；折叠态只显示分组图标，重复图标会让图标栏无法辨认。
- 条目路由与权限码属于安全边界，视觉调整不得修改。

## 视觉与状态

| 状态 | 底色 | 文字 | 图标 |
| --- | --- | --- | --- |
| 默认 | 侧栏底色 | `--edo-text` | `--edo-nav-icon-color`（次要色） |
| 悬停 | `--edo-nav-hover-bg`（`--edo-primary-soft`） | `--edo-text` | `--edo-primary` |
| 选中 | `--edo-nav-selected-bg`（`--edo-primary-soft-strong`） | `--edo-nav-selected-color`，字重 600 | 同选中文字 |
| 键盘焦点 | 条目内嵌 2px `--edo-nav-focus-ring` | 不变 | 不变 |

- 选中实底强度高于悬停：浅靛蓝两档，不使用中性灰，也不使用左侧竖条或描边强调当前项。
- 条目圆角 8px，左右外边距对称（`margin: 2px 4px`）；子菜单不再整块铺 `--edo-surface-soft` 底色，避免与侧栏底色割裂。
- 条目高 40px，图标 16px、`stroke-width: 1.8`；分组图标 18px。
- 折叠态弹出的子菜单沿用同一套圆角、图标颜色与线宽。
- 浅色与深色主题都需满足：选中文字对比度 ≥ 4.5、悬停文字 ≥ 4.5、图标与底色 ≥ 3。

## 验证方式

```bash
node --test web/tests/sidebar-navigation-design.test.mjs   # 信息架构、图标唯一性、配色与对比度契约
cd web && npm run dev                                      # 打开 /tests/fixtures/sidebar-navigation.html 目视校验
```

预览夹具支持 `?theme=dark`、`?collapsed=1`、`?selected=/settings`、`?open=security`，直接挂载真实侧栏组件。
