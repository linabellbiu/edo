import 'ant-design-vue/dist/reset.css'
import '/src/styles/vben.css'
import { createApp, h, ref } from 'vue'
import { ConfigProvider, theme } from 'ant-design-vue'
import { createPinia } from 'pinia'
import client from '/src/api/client'
import i18n from '/src/locales'
import { useAuthStore } from '/src/stores/auth'
import OrganizationView from '/src/views/OrganizationView.vue'

const params = new URLSearchParams(location.search)
const dark = params.get('theme') === 'dark', readonly = params.has('readonly')
const defaultDepartmentID = '00000000-0000-0000-0000-000000000001'
const company = { name: '示例公司', description: '产品与技术团队' }
const departments = [
  { id: 'engineering', parent_id: '', name: '研发部', description: '应用研发与平台维护', member_count: 6, is_default: false },
  { id: 'backend', parent_id: 'engineering', name: '后端组', description: '服务开发', member_count: 3, is_default: false },
  { id: 'payments', parent_id: 'backend', name: '支付服务组', description: '', member_count: 1, is_default: false },
  { id: 'frontend', parent_id: 'engineering', name: '前端组', description: '', member_count: 2, is_default: false },
  { id: defaultDepartmentID, parent_id: '', name: '默认部门', description: '公司初始部门', member_count: 1, is_default: true },
]
const memberNames = { engineering: ['陈阳', '李敏', '周宁', '吴晨', '郑远', '许安'], backend: ['林青', '张晓', '何川'], payments: ['赵明'], frontend: ['方可', '孙晴'], [defaultDepartmentID]: ['管理员'] }
const members = Object.entries(memberNames).flatMap(([department_id, names]) => names.map((nickname, index) => ({ id: `${department_id}-member-${index}`, department_id, nickname, username: department_id === defaultDepartmentID ? 'admin' : `${department_id}.${index}`, is_active: department_id !== 'payments' })))
let nextID = 0
const saved = ref('尚未保存')
client.defaults.adapter = async config => {
  const path = config.url, method = config.method
  const body = typeof config.data === 'string' ? JSON.parse(config.data) : config.data
  let data
  if (method === 'get' && path === '/organization') data = { organization: { ...company } }
  else if (method === 'get' && path === '/departments') data = { departments: departments.map(row => ({ ...row })) }
  else if (method === 'get' && path === '/users') {
    if (params.has('members-error')) throw new Error('模拟成员加载失败')
    data = { users: members.slice(config.params.offset, config.params.offset + config.params.limit) }
  }
  else if (!readonly && method === 'post' && path === '/departments') {
    const row = { ...body, id: `created-${++nextID}`, member_count: 0, is_default: false }
    departments.push(row); saved.value = `已添加 ${row.name}，上级 ${row.parent_id || '公司'}`; data = { department: row }
  } else if (!readonly && method === 'put' && path.startsWith('/departments/')) {
    const row = departments.find(row => row.id === path.split('/').at(-1))
    if (!row) throw new Error('部门不存在')
    Object.assign(row, body); saved.value = `已修改 ${row.name}`; data = { department: { ...row } }
  } else if (!readonly && method === 'put' && path === '/organization') {
    Object.assign(company, body); saved.value = `已修改 ${company.name}`; data = { organization: { ...company } }
  } else throw new Error('隔离验收未登记请求')
  return { data, status: 200, statusText: 'OK', headers: {}, config }
}
// 所有写入仅修改夹具内存，不连接外部 API。
window.fetch = () => Promise.reject(new Error('隔离验收不连接服务器'))
document.documentElement.dataset.theme = dark ? 'dark' : 'light'
const pinia = createPinia(), auth = useAuthStore(pinia)
auth.user = { id: 'preview', permissions: ['organization.read', 'department.read', ...(params.has('no-member-access') ? [] : ['user.read']), ...(readonly ? [] : ['organization.update', 'department.create', 'department.update', 'department.delete'])], is_superuser: false }
auth.loaded = true; auth.authorizationState = 'ready'
createApp({ render: () => h(ConfigProvider, { theme: { algorithm: dark ? theme.darkAlgorithm : theme.defaultAlgorithm } }, {
  default: () => [h('header', { style: 'padding:16px;color:var(--edo-muted)' }, ['隔离合成数据，仅在内存保存。', h('output', { 'aria-label': '隔离保存记录', style: 'margin-left:12px' }, saved.value)]), h('main', { style: 'padding:16px' }, h(OrganizationView))],
}) }).use(pinia).use(i18n).mount('#root')
