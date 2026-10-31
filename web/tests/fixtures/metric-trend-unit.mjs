import '/src/styles/vben.css'
import { createApp, ref } from 'vue'
import { createPinia, defineStore } from 'pinia'
import { createI18n } from 'vue-i18n'
import { messages } from '/src/locales'
import MetricTrendUnitPreview from './MetricTrendUnitPreview.vue'

// 仅为真实组件预览提供内存 theme，不使用生产 preferences 的持久化流程。
const usePreviewPreferences = defineStore('preferences', () => ({ theme: ref('light') }))
const pinia = createPinia()
usePreviewPreferences(pinia)
const i18n = createI18n({ legacy: false, locale: 'zh-CN', fallbackLocale: 'en-US', messages })
createApp(MetricTrendUnitPreview).use(pinia).use(i18n).mount('#root')
