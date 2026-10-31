# EDO 前端依赖补丁

## Ant Design Vue 4.2.6 的 Select 活动项引用

真实用户表单验收发现两类无效引用：关闭时菜单尚未挂载，却仍有 `aria-activedescendant`；空列表或没有可选活动项时，引用仍指向索引 `0` 或 `-1`。展开部门单选时，原库的已选文本颜色也低于正常文本对比度要求。

选型与边界：

- 继续使用既有、MIT 许可的 Ant Design Vue，不另造选择器或替换整套组件库。已核对锁定版本的 Select、BaseSelect、Selector 和 Input；`defaultActiveFirstOption=false` 仍会生成索引引用，顶层 ARIA 属性不会完整传到内部输入，不能只凭 Input 最后一层属性合并推断修复有效。
- npm 项目使用成熟的 `patch-package` 8.0.1（MIT）持久化四处最小补丁；该版本修复了旧版临时目录依赖的安全问题。它仅是构建期开发依赖，不打入浏览器包、Go API 或 NGINX 运行镜像。不迁移包管理器、不引入运行服务，也不修改服务器 CI/Runner。
- 关闭时不声明不存在的活动项；展开时仅当活动索引落在实际过滤后选项范围内才生成引用。正常活动项、组合框角色、展开状态、键盘跟踪及真实菜单继续由 Ant 负责。不得使用假 listbox、DOM 修改或 axe 规则忽略来代替修复。
- 仅用户新增表单的展开单选文本复用现有 `--edo-muted`，不调整登录、全站主题或通用控件样式。

补丁同时覆盖库的 `es`（Vite）与 `lib`（Node/SSR）入口。`npm ci` 的 `postinstall` 自动应用补丁，失败必须阻断；Docker 在安装依赖前复制补丁目录。跳过 npm 生命周期的环境须显式执行 `npm run postinstall`，不得带未应用补丁继续声称验证通过。

维护风险是上游升级后补丁可能无法应用，或组件内部索引规则变化。升级 Ant Design Vue 时必须重新核对源码和补丁，跑完整前端测试，并回归真实菜单、关闭/无匹配/禁用选项、方向键、Escape 和主题对比。上游修复后应移除对应补丁，而不是长期叠加兼容逻辑。

参考：

- [W3C 组合框交互及活动项规范](https://www.w3.org/WAI/ARIA/apg/patterns/combobox/)
- [锁定版本的 Select Input 源码](https://github.com/vueComponent/ant-design-vue/blob/4.2.6/components/vc-select/Selector/Input.tsx)
- [patch-package 维护方式](https://github.com/ds300/patch-package)
- [patch-package 安全更新记录](https://github.com/ds300/patch-package/blob/master/CHANGELOG.md)

## Vue I18n 安全补丁

依赖审计发现原 `vue-i18n` 11.4.8 受两个上游安全公告影响，npm 将依赖链汇总为五个 high 告警；上游对两项公告分别评为 low 和 high。这里是依赖风险，不表示已经证实 EDO 存在可利用的 XSS 路径。

锁定升级到同系列 11.4.16，并回归现有翻译和页面，不改消息内容或全站视觉。只更新该包及其关联的 Intlify 包，不批量执行 `npm audit fix` 或升级其他依赖。升级后的审计结果与真实测试分开记录，零依赖告警不等于业务安全验收完成。

- [混合命名/列表参数转义公告](https://github.com/intlify/vue-i18n/security/advisories/GHSA-8rc2-428q-7ww3)
- [翻译 HTML 的 srcdoc 安全公告](https://github.com/intlify/vue-i18n/security/advisories/GHSA-83rm-6c8r-g728)
- [11.4.16 发布记录](https://github.com/intlify/vue-i18n/releases/tag/v11.4.16)
