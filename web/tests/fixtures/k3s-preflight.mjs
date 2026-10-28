import 'ant-design-vue/dist/reset.css'
import '/src/styles/vben.css'
import '/src/styles/resource-list.css'
import {createApp,reactive} from 'vue'
import {createPinia} from 'pinia'
import {createMemoryHistory,createRouter} from 'vue-router'
import client from '/src/api/client'
import {useAuthStore} from '/src/stores/auth'
import i18n from '/src/locales'
import K3sPreflightPreview from './K3sPreflightPreview.vue'
import {createPreflightPreview} from './k3s-preflight-data.mjs'

// 仅该夹具页面使用内存适配器，未知请求失败关闭，不透传开发或生产 API。
const parameters=new URLSearchParams(location.search),calls=reactive([])
const preview=createPreflightPreview(parameters.get('scenario')??'passed',{calls})
client.defaults.adapter=preview.adapter
const pinia=createPinia(),auth=useAuthStore(pinia)
auth.user={id:'k3s-preflight-fixture-user',username:'preview',nickname:'前端夹具',project_id:localStorage.getItem('edo.project')??'',department_id:'fixture-department',is_superuser:false,permissions:preview.permissions};auth.loaded=true
const router=createRouter({history:createMemoryHistory(),routes:[{path:'/:pathMatch(.*)*',component:{template:'<p>前端检测夹具不访问业务路由。</p>'}}]})
await router.push('/fixture')
document.body.dataset.preflightFixture='true'
createApp(K3sPreflightPreview,{preview}).use(pinia).use(i18n).use(router).mount('#root')
