import 'ant-design-vue/dist/reset.css'
import '@xterm/xterm/css/xterm.css'
import './styles/vben.css'

import { createPinia } from 'pinia'
import { createApp } from 'vue'
import { MotionPlugin } from '@vueuse/motion'

import App from './App.vue'
import i18n from './locales'
import router from './router'

createApp(App)
  .use(createPinia())
  .use(router)
  .use(i18n)
  .use(MotionPlugin)
  .mount('#root')
