import 'ant-design-vue/dist/reset.css'
import '/src/styles/vben.css'
import '/src/styles/resource-list.css'
import '/src/styles/kubernetes.css'
import { createApp, h } from 'vue'
import { ConfigProvider, Tooltip, message, theme } from 'ant-design-vue'
import KubeResourceCell from '/src/components/KubeResourceCell.vue'
import i18n from '/src/locales'

const options = new URLSearchParams(location.search)
const dark = options.has('dark')
const browserClipboard = navigator.clipboard
document.documentElement.dataset.theme = dark ? 'dark' : 'light'
if (options.has('legacy')) Object.defineProperty(navigator, 'clipboard', { configurable: true, value: undefined })
if (options.has('failure')) {
  Object.defineProperty(navigator, 'clipboard', { configurable: true, value: undefined })
  document.execCommand = () => false
}
const image = 'uhub.service.ucloud.cn/zrt-application/edo-demo-web:release-v0.0.8-complete-image-name'
createApp({ render: () => h(ConfigProvider, { theme: { algorithm: dark ? theme.darkAlgorithm : theme.defaultAlgorithm } }, () => h('main', { style: 'padding:32px;color:var(--edo-text);max-width:700px' }, [
  h('h3', '完整文本复制 · 隔离验收'),
  h('p', '双击镜像名称后，在下方粘贴核对；不会连接服务器。'),
  h('section', { style: 'padding:16px;border:1px solid var(--edo-border);margin:24px 0;width:330px;background:var(--edo-surface)' }, h(KubeResourceCell, { cell: { text: image, secondary: '1 个容器' } })),
  h('label', { for: 'paste-result' }, '粘贴核对'),
  h('textarea', { id: 'paste-result', style: 'display:block;width:100%;height:100px;margin-top:8px;color:var(--edo-text);background:var(--edo-surface);border:1px solid var(--edo-border)' }),
  h('button', { type: 'button', onClick: async () => {
    try { document.querySelector('#paste-result').value = await browserClipboard.readText() }
    catch { message.warning('浏览器未允许读取，请手动粘贴核对') }
  } }, '从剪贴板粘贴核对'),
  h('p', { style: 'overflow-wrap:anywhere' }, `预期完整内容：${image}`),
  h('section', { style: 'margin-top:24px' }, h(KubeResourceCell, { canFollow: true, cell: { text: '关联资源（单击原操作）', reference: { resource: 'pods', name: 'preview', namespace: 'preview' } }, onFollow: () => message.info('已保留原单击操作') })),
])) }).use(i18n).component('a-tooltip', Tooltip).mount('#root')
