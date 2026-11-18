import { createApp } from 'vue'
import i18n from '/src/locales'
import SidebarNavigationPreview from './SidebarNavigationPreview.vue'
import '/src/styles/vben.css'
import 'ant-design-vue/dist/reset.css'

createApp(SidebarNavigationPreview).use(i18n).mount('#app')
