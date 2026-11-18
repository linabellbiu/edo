# 分段页签与筛选视觉规范

页面页签（系统设置、访问控制）与筛选/范围控件（任务状态、时间范围、编辑方式、权限动作）共用 Ant Design Segmented，视觉统一在 `web/src/styles/vben.css` 的 `.ant-segmented` 规则中维护，契约由 `web/tests/segmented-tabs-design.test.mjs` 校验。

## 规则

- 一个控件只有一层容器底色：`--edo-tabs-bg`（浅中性色），不再叠加描边、内阴影或渐变。
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
