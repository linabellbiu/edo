import 'ant-design-vue/dist/reset.css'
import { createApp, h, onBeforeUnmount, ref } from 'vue'
import { createRouter, createWebHistory, onBeforeRouteLeave, RouterView } from 'vue-router'
import { installChunkRecovery } from '../../src/router/chunkRecovery'

const Editor = {
  setup() {
    const draft = ref('')
    onBeforeRouteLeave(() => !draft.value || window.confirm('放弃未保存的修改？'))
    const beforeUnload = event => {
      if (draft.value) { event.preventDefault(); event.returnValue = '' }
    }
    window.addEventListener('beforeunload', beforeUnload)
    onBeforeUnmount(() => window.removeEventListener('beforeunload', beforeUnload))
    return () => h('label', ['编辑草稿', h('input', { value: draft.value, onInput: event => { draft.value = event.target.value } })])
  },
}
const router = createRouter({ history: createWebHistory(), routes: [
  { path: '/editor', component: Editor },
  { path: '/target', component: () => import('./ChunkRecoveryTarget.vue') },
  { path: '/healthy', component: { render: () => h('p', '其他菜单正常') } },
] })
installChunkRecovery(router)
createApp({
  render: () => h('main', { 'data-version': __NAV_REC_VERSION__ }, [
    h('p', `页面版本 ${__NAV_REC_VERSION__}`),
    ...[['编辑页', '/editor'], ['系统监控', '/target?range=3d&cluster=a&cluster=b#memory'], ['其他菜单', '/healthy']].map(([label, path]) =>
      h('button', { onClick: () => { void router.push(path) } }, label)),
    h(RouterView),
  ]),
}).use(router).mount('#app')
