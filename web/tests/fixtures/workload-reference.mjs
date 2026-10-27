import 'ant-design-vue/dist/reset.css'
import '/src/styles/vben.css'
import '/src/styles/resource-list.css'
import '/src/styles/kubernetes.css'
import { createApp } from 'vue'
import { createPinia } from 'pinia'
import i18n from '/src/locales'
import WorkloadReference from './WorkloadReference.vue'

// 只由 Vite 开发服务器显式打开此 HTML；不加入应用路由或生产构建入口。
createApp(WorkloadReference).use(createPinia()).use(i18n).mount('#root')
