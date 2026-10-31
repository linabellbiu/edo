import 'ant-design-vue/dist/reset.css'
import '/src/styles/vben.css'
import '/src/styles/kubernetes.css'
import {createApp} from 'vue'
import i18n from '/src/locales'
import KubernetesEditorFocusPreview from './KubernetesEditorFocusPreview.vue'

createApp(KubernetesEditorFocusPreview).use(i18n).mount('#root')
