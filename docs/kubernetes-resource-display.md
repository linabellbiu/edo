# EDO Kubernetes 资源数量展示

## 显示约定

- 内存、存储、临时存储、大页内存和缓存镜像大小统一采用二进制单位：`1 GiB = 1024 MiB`，`1 MiB = 1048576 B`。达到 GiB 时展示 GiB，其余展示 MiB，通常最多两位小数；微小正值显示 `<0.01 MiB`，真实零值显示 `0 MiB`。
- CPU 展示核心数（CPU）或毫核（m）；Pod、GPU 和其他计数资源展示整数，不能把计数当成字节。
- 未返回、格式错误、负值和非有限值显示 `—`。节点条保留用量与容量各自的单位，悬浮提示保留更高精度及字节数；容量和请求/限制可悬停查看原始数量。
- 总览、列表、详情、监控和图表提示复用同一套格式；容器资源不再只展示 CPU、内存、临时存储，扩展资源也展示。
- 换算只用于只读显示，不更改 API 原始对象、YAML、编辑表单及提交值。PV/PVC 的请求量和已分配容量保持独立，节点容量仍不表示当前剩余空间。

## 解析方案评估（2026-10-23）

研究了 [Kubernetes 官方 Quantity 格式](https://kubernetes.io/docs/reference/kubernetes-api/definitions/quantity-resource/)、[官方 JavaScript 客户端](https://github.com/kubernetes-client/javascript/blob/main/src/util.ts) 及 [k8s-resource-parser](https://github.com/etiennedi/kubernetes-resource-parser)。

- 后端继续复用已有官方 `k8s.io/apimachinery/pkg/api/resource`，负责配置与资源运算；本次不增加后端接口、持久化字段或依赖。
- 官方 JavaScript SDK 为 Apache-2.0，持续维护且许可兼容，但面向 Node.js，依赖网络、认证、归档和 WebSocket 等组件。为只读格式化引入整个 SDK 会增加浏览器兼容及供应链维护成本；其 `quantityToScalar` 对带大单位的小数调用 BigInt，也不能直接覆盖 `1.5Gi`。
- MIT 的 `k8s-resource-parser` 体积小且无依赖，但自身声明不提供错误处理，不符合页面对异常和缺失值的要求，因此不采用。
- 前端在现有 Kubernetes API 展示辅助函数中保留一个范围受限的转换器，识别十进制、二进制和科学计数后缀，先解析原始单位再格式化，避免混淆 `M` 与 `Mi`。不将其用于 Kubernetes 配置合法性判断、调度或写入计算，也不复制官方 SDK 实现。

维护风险：前端 Number 仅用于显示近似值，不能替代官方任意精度 Quantity；后续 Quantity 语法扩展需要同步测试和转换器。测试覆盖 SI/二进制/指数/毫量、异常值、阈值、小容量、扩展资源及跨页面一致性；原始配置继续交由官方解析与服务端 dry-run 校验。本次只改变展示，无数据迁移。
