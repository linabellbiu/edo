import { createApp } from 'vue'
import Antd from 'ant-design-vue'
import DeploymentInstanceInlinePreview from './DeploymentInstanceInlinePreview.vue'
import '/src/styles/vben.css'
import '/src/styles/application-workspace.css'
import 'ant-design-vue/dist/reset.css'

createApp(DeploymentInstanceInlinePreview).use(Antd).mount('#app')
