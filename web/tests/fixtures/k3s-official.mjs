import 'ant-design-vue/dist/reset.css'
import '/src/styles/vben.css'
import '/src/styles/resource-list.css'
import {createApp,reactive} from 'vue'
import {createPinia} from 'pinia'
import {createMemoryHistory,createRouter} from 'vue-router'
import client from '/src/api/client'
import {useAuthStore} from '/src/stores/auth'
import i18n from '/src/locales'
import K3sOfficialPreview from './K3sOfficialPreview.vue'
import {createOfficialPreview} from './k3s-official-data.mjs'
const calls=reactive([]),preview=createOfficialPreview({calls})
client.defaults.adapter=preview.adapter
const pinia=createPinia(),auth=useAuthStore(pinia)
auth.user={id:'official-fixture-user',username:'preview',project_id:localStorage.getItem('edo.project')??'',department_id:'fixture-department',is_superuser:false,permissions:preview.permissions};auth.loaded=true;auth.authorizationState='ready'
const router=createRouter({history:createMemoryHistory(),routes:[{path:'/:pathMatch(.*)*',component:{template:'<p>仅用于隔离前端验收。</p>'}}]})
await router.push('/fixture')
createApp(K3sOfficialPreview,{preview}).use(pinia).use(i18n).use(router).mount('#root')
