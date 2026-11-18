import { createApp } from 'vue'
import { createPinia } from 'pinia'
import i18n from '/src/locales'
import TerminalEscPreview from './TerminalEscPreview.vue'
import '/src/styles/vben.css'
import 'ant-design-vue/dist/reset.css'

createApp(TerminalEscPreview).use(createPinia()).use(i18n).mount('#app')
