# 分段页签与筛选视觉规范

页面页签（系统设置、访问控制）与筛选/范围控件（任务状态、时间范围、编辑方式、权限动作）共用 Ant Design Segmented，视觉统一在 `web/src/styles/vben.css` 的 `.ant-segmented` 规则中维护，契约由 `web/tests/segmented-tabs-design.test.mjs` 校验。

## 规则

- 一个控件只有一层容器底色：`--edo-tabs-bg`（浅色主题为白底），配一条 `--edo-tabs-border` 发丝边框界定范围；不使用内阴影或渐变。
- 相邻功能性分段之间有 1px×14px（小尺寸 12px）的发丝分隔线 `--edo-tabs-divider`，选中项和悬停项两侧不画线，避免线条压进色块。
- 选中为浅靛蓝实底 `--edo-tabs-selected-bg` + 靛蓝文字 `--edo-tabs-selected-color`（字重 600），不使用高饱和品牌蓝大色块、外发光或渐变。
- 悬停为更浅一档 `--edo-tabs-hover-bg`，未选中文字使用次要色 `--edo-muted`。
- 条目高度固定分档：默认 28px、`size="small"` 24px、页面页签 32px。选中块与相邻条目同高，不得溢出容器或让容器高度随选中项跳变。
- 条目圆角 `--edo-tabs-item-radius`（7px），容器圆角 `--edo-tabs-radius`（10px），容器内边距 3px。
- 页面页签保留 `width: max-content`、`max-width: 100%`、横向滚动和隐藏滚动条；移动端宽度 100%。
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

夹具直接挂载真实 Segmented 与共享样式，覆盖系统设置、访问控制、时间范围、状态筛选、小尺寸、禁用与长选项。
## 功能性单选按钮组（Radio.Button）

安装方式、镜像来源、授权范围、状态筛选等 `a-radio-group` + `a-radio-button` 与分段控件共用同一套外观：白底容器 + 1px 发丝边框、分段之间 1px 发丝分隔线（选中与悬停两侧不画线）、浅靛蓝选中 + 靛蓝文字、禁用项降对比，不再使用 antd 默认的整块品牌蓝实底。普通圆点单选组不受影响。

## 已核对的同类控件

| 控件 | 处理 |
| --- | --- |
| 分段控件 `a-segmented`（页面页签、时间范围、状态、编辑方式、权限动作） | 统一：容器边框 + 分隔线 + 浅靛蓝选中 |
| 功能性单选按钮组 `a-radio-group`（安装方式、镜像来源、授权范围、状态筛选等） | 统一：与分段控件同一套令牌 |
| 下划线页签（`a-tabs`、K8s 资源类型页签） | 保留页签族样式：下划线选中 + 容器底边线，不叠加色块 |
| 顶栏页面标签（`.vben-tabs`）、侧栏菜单 | 独立族，保持现有选中底色与焦点环 |

新增功能性按钮组必须复用 `--edo-tabs-*` 令牌（容器底色、边框、分隔线、选中、焦点），由 `web/tests/segmented-tabs-design.test.mjs` 校验，不再逐页写样式。
