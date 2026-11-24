# 分段控件与按钮视觉规范

页面页签和功能性单选控件共用 `web/src/styles/vben.css` 中的分段样式。系统设置使用 Ant Design Segmented。访问控制使用 `.edo-segmented-tabs` 和 `.edo-tab-item`，选中底色随当前页签直接切换。原生链接和页签也复用这两个样式类及各自的导航和选中属性。视觉契约由 `web/tests/segmented-tabs-design.test.mjs` 校验。

## 规则

- 一个控件只有一层容器底色：`--edo-tabs-bg`（浅色主题为白底），配一条 `--edo-tabs-border` 发丝边框界定范围；不使用内阴影或渐变。
- 相邻条目通过 `--edo-tabs-item-gap` 保留 6px 留白。选中项、悬停项和键盘焦点同样遵守此间距。
- 单行控件的普通条目之间在留白中显示 1px×14px（小尺寸 12px）的发丝线 `--edo-tabs-divider`。高亮项两侧只保留留白，可换行的按钮式单选组也用留白分隔。
- 选中为浅靛蓝实底 `--edo-tabs-selected-bg` + 靛蓝文字 `--edo-tabs-selected-color`（字重 600），不使用高饱和品牌蓝大色块、外发光或渐变。
- 悬停为更浅一档 `--edo-tabs-hover-bg`，未选中文字使用次要色 `--edo-muted`。
- 条目高度固定分档：默认 28px、`size="small"` 24px、页面页签 32px。选中块与相邻条目同高，不得溢出容器或让容器高度随选中项跳变。
- 条目圆角 `--edo-tabs-item-radius`（7px），容器圆角 `--edo-tabs-radius`（10px），容器内边距 3px。
- 单行控件在窄屏于容器内横向滚动，页面级页签在移动端使用可用宽度。按钮式单选组换行时沿用 6px 的横纵间距。
- 键盘焦点落在条目上：`:has(.ant-segmented-item-input:focus-visible)` 显示 2px 内环 `--edo-tabs-focus-ring`。
- 禁用时分段控件整体降低对比度，选中项不再保持高亮实底。

| 状态 | 底色 | 文字 |
| --- | --- | --- |
| 默认 | `--edo-tabs-bg` | `--edo-muted` |
| 悬停 | `--edo-tabs-hover-bg` | `--edo-text` |
| 选中 | `--edo-tabs-selected-bg` | `--edo-tabs-selected-color`，600 |
| 禁用 | `--edo-tabs-bg` | 次要色 55% 混合 |

明暗主题都必须满足：选中文字对比度 ≥ 4.5、未选中文字 ≥ 4.5、容器底与页面底可分。

## 验证

```bash
node --test web/tests/segmented-tabs-design.test.mjs
cd web && npm run dev   # 打开 /tests/fixtures/segmented-tabs.html 目视校验（支持 ?theme=dark）
```

夹具直接挂载真实 Segmented 与共享样式，覆盖系统设置、访问控制、时间范围、状态筛选、小尺寸、禁用与长选项。验收时将鼠标停在已选项的相邻条目，检查两块底色间的留白。用键盘切换选中项，并用窄屏确认分段滚动及单选换行。

## 功能性单选按钮组（Radio.Button）

安装方式、镜像来源、授权范围和状态筛选等 `a-radio-group` 与 `a-radio-button` 共用容器、间距、选中态和焦点样式。只在按钮式单选组使用这一样式。

## 已核对的同类控件

| 控件 | 处理 |
| --- | --- |
| 分段控件 `a-segmented`（页面页签、时间范围、状态、编辑方式、权限动作） | 容器边框、6px 留白、浅靛蓝选中 |
| 功能性单选按钮组 `a-radio-group`（安装方式、镜像来源、授权范围、状态筛选等） | 共用分段令牌，可换行 |
| Kubernetes 资源类型、主机与集群导航 | 统一分段外观，保留数量与原生链接或页签语义 |
| 应用状态筛选、流水线任务分类与配置分区 | 统一分段外观，保留筛选和当前分区状态 |
| 详情抽屉与编辑表单内容页签（`a-tabs`） | 沿用下划线选中和容器底边线 |
| 顶栏页面标签（`.vben-tabs`）、侧栏菜单 | 独立族，保持现有选中底色与焦点环 |

新增分段控件复用 `--edo-tabs-*` 令牌与现有分段样式，由 `web/tests/segmented-tabs-design.test.mjs` 校验。

## 操作按钮

操作按钮复用 `web/src/styles/buttons.css` 的语义配色。颜色随操作用途选择，文字、图标、尺寸、禁用和确认流程继续沿用共用组件。

图标和文字作为一组水平、垂直居中，图文间距为 6px。资源列表的普通行按钮高 28px，左右内边距各 10px，按钮之间间距为 6px。「更多」使用 30 × 28px 的纯图标按钮。`ResourceTable` 与其他使用 `.resource-row-actions` 的列表共同使用 `web/src/styles/resource-list.css` 定义的行按钮尺寸。

| 用途 | 配色 | 用法 |
| --- | --- | --- |
| 保存、提交等主操作 | 中蓝实底、白字 | `type="primary"` |
| 添加端口、挂载、变量等辅助操作 | 浅蓝底、深蓝字 | `class="edo-button-soft"` |
| 已就绪能力入口、正向操作 | 浅绿底、深绿字 | `class="edo-button-soft" data-tone="success"` |
| 重启等有运行影响的操作 | 浅琥珀底、深琥珀字 | `class="edo-button-soft" data-tone="warning"` |
| 列表或表单的删除入口 | 莓红字，继承按钮类型的底色 | `danger` 配合默认或文字按钮 |
| 破坏性确认 | 莓红实底、白字 | `type="primary" danger` 或确认弹窗的 `okType: 'danger'` |
| 取消、普通刷新 | 主题中性 | 使用默认或文字按钮 |

浅色和深色主题统一检查默认、悬停、按下、键盘焦点和禁用。可操作状态下文字对比度至少 4.5:1。按钮配色预览使用 `/tests/fixtures/button-layout.html`，支持 `?theme=dark`。

行按钮验收用服务账户的「编辑」「YAML」「更多」核对两侧留白、图文间距和图标按钮尺寸，并在窄屏检查固定操作列。 [Kubernetes 表格规范](kubernetes-workbench-design.md#3-表格语言)
