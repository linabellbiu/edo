import 'ant-design-vue/dist/reset.css'
import '@xterm/xterm/css/xterm.css'
import '/src/styles/vben.css'
import '/src/styles/resource-list.css'
import { createApp } from 'vue'
import { createPinia } from 'pinia'
import { createMemoryHistory, createRouter } from 'vue-router'
import client from '/src/api/client'
import { useAuthStore } from '/src/stores/auth'
import HostView from '/src/views/HostView.vue'
import i18n from '/src/locales'
import TerminalWorkspacePreview from './TerminalWorkspacePreview.vue'
import { createTerminalPreviewAdapter, installTerminalPreviewSocket } from './terminal-preview-data.mjs'

const preview=createTerminalPreviewAdapter(new URLSearchParams(location.search).get('scenario'))
client.defaults.adapter=preview.adapter
const originalFetch=window.fetch.bind(window)
window.fetch=(input,options)=>{const url=new URL(typeof input==='string'?input:input.url,location.href);if(url.origin!==location.origin||url.pathname.startsWith('/api/'))return Promise.reject(new Error('本地终端预览不连接真实接口'));return originalFetch(input,options)}
installTerminalPreviewSocket()
const pinia=createPinia(),auth=useAuthStore(pinia)
auth.user={id:'terminal-preview-user',username:'preview',nickname:'本地终端预览',project_id:localStorage.getItem('edo.project')??'',department_id:'preview-department',is_superuser:false,permissions:preview.hostData.permissions};auth.loaded=true
const router=createRouter({history:createMemoryHistory(),routes:[{path:'/hosts',component:HostView},{path:'/:pathMatch(.*)*',redirect:'/hosts'}]})
await router.push('/hosts')
createApp(TerminalWorkspacePreview).use(pinia).use(i18n).use(router).mount('#root')
