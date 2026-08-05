<script setup lang="ts">
import { computed, onMounted, reactive, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { Modal, message } from 'ant-design-vue'
import { Plus, RefreshCw } from 'lucide-vue-next'

import client from '@/api/client'
import { apiErrorMessage, type ResourceRecord } from '@/api/resources'
import PageToolbar from '@/components/PageToolbar.vue'
import ResourceTable from '@/components/ResourceTable.vue'
import { useAuthStore } from '@/stores/auth'

interface Permission {
  code: string
  group: string
  resource: string
  resource_name: string
  action: string
  action_name: string
  name: string
  description: string
  dangerous: boolean
}

interface PermissionResource {
  key: string
  name: string
  items: Permission[]
}

interface PermissionGroup {
  name: string
  resources: PermissionResource[]
}

interface Department extends ResourceRecord {
  id: string
  name: string
  description: string
  is_active: boolean
  member_count: number
  is_default: boolean
  created_at: string
  updated_at: string
}

interface Role extends ResourceRecord {
  id: string
  name: string
  display_name: string
  description: string
  permissions: string[]
  updated_at: string
}

interface ManagedUser extends ResourceRecord {
  id: string
  username: string
  nickname: string
  department_id: string
  department_name: string
  is_superuser: boolean
  is_active: boolean
  role_ids: string[]
  permission_overrides: { allow: string[]; deny: string[] }
  effective_permissions: string[]
  last_login_at?: string
}

interface AuditLog extends ResourceRecord {
  id: string
  action: string
  resource_type: string
  resource_id?: string
  result: string
  client_ip: string
  created_at: string
}

const route = useRoute()
const router = useRouter()
const auth = useAuthStore()

const users = ref<ManagedUser[]>([])
const departments = ref<Department[]>([])
const roles = ref<Role[]>([])
const permissions = ref<Permission[]>([])
const audits = ref<AuditLog[]>([])
const loading = ref(false)
const saving = ref(false)

const userFormOpen = ref(false)
const departmentFormOpen = ref(false)
const userDepartmentOpen = ref(false)
const roleFormOpen = ref(false)
const accessOpen = ref(false)
const editingDepartmentID = ref('')
const editingRoleID = ref('')
const editingUser = ref<ManagedUser | null>(null)

const userForm = reactive({
  username: '',
  nickname: '',
  password: '',
  department_id: '',
  role_ids: [] as string[],
})
const departmentForm = reactive({ name: '', description: '' })
const userDepartmentForm = reactive({ department_id: '' })
const roleForm = reactive({
  name: '',
  display_name: '',
  description: '',
  permissions: [] as string[],
})
const accessForm = reactive({
  role_ids: [] as string[],
  effects: {} as Record<string, 'inherit' | 'allow' | 'deny'>,
})

const tabs = computed(() => [
  ...(auth.canAny(['user.read', 'user.create', 'user.update', 'user.delete'])
    ? [{ key: 'users', label: '用户管理' }]
    : []),
  ...(auth.canAny(['department.read', 'department.create', 'department.update', 'department.delete'])
    ? [{ key: 'departments', label: '部门管理' }]
    : []),
  ...(auth.canAny(['role.read', 'role.create', 'role.update', 'role.delete'])
    ? [{ key: 'roles', label: '角色与权限' }]
    : []),
  ...(auth.canAny(['audit.read']) ? [{ key: 'audit', label: '审计日志' }] : []),
])

const active = computed(() =>
  tabs.value.some((item) => item.key === route.query.view)
    ? String(route.query.view)
    : tabs.value[0]?.key || '',
)

const permissionGroups = computed<PermissionGroup[]>(() => {
  const groups = new Map<string, Map<string, PermissionResource>>()
  for (const permission of permissions.value) {
    const groupName = permission.group || '其他'
    const resourceKey = permission.resource || permission.code.split('.')[0] || 'other'
    const group = groups.get(groupName) ?? new Map<string, PermissionResource>()
    const resource = group.get(resourceKey) ?? {
      key: resourceKey,
      name: permission.resource_name || resourceKey,
      items: [],
    }
    resource.items.push(permission)
    group.set(resourceKey, resource)
    groups.set(groupName, group)
  }
  return Array.from(groups, ([name, resources]) => ({
    name,
    resources: Array.from(resources.values()),
  }))
})

const userColumns = [
  { key: 'username', label: '用户名' },
  { key: 'nickname', label: '昵称' },
  { key: 'department_name', label: '部门' },
  { key: 'role_ids', label: '角色' },
  { key: 'effective_permissions', label: '有效权限' },
  { key: 'is_active', label: '状态' },
  { key: 'last_login_at', label: '最后登录' },
]
const departmentColumns = [
  { key: 'name', label: '部门名称' },
  { key: 'description', label: '说明' },
  { key: 'member_count', label: '成员' },
  { key: 'is_active', label: '状态' },
  { key: 'updated_at', label: '更新时间' },
]
const roleColumns = [
  { key: 'display_name', label: '名称' },
  { key: 'name', label: '技术标识' },
  { key: 'description', label: '说明' },
  { key: 'permissions', label: '权限数量' },
  { key: 'updated_at', label: '更新时间' },
]
const auditColumns = [
  { key: 'action', label: '动作' },
  { key: 'resource_type', label: '资源' },
  { key: 'resource_id', label: '资源 ID' },
  { key: 'result', label: '结果' },
  { key: 'client_ip', label: '来源 IP' },
  { key: 'created_at', label: '时间' },
]

const toolbarDescription = computed(() => {
  if (active.value === 'audit') return '查看身份、资源、操作结果和请求来源，不展示任何密钥内容。'
  if (active.value === 'roles') return '按查看、创建、修改、删除和执行等动作组合可复用的角色权限。'
  if (active.value === 'departments') return '部门用于隔离业务数据；普通用户只能访问本部门成员创建的资源。'
  return '维护账户所属部门、状态、角色和用户级允许或拒绝例外。'
})

function canAny(required: string[]) {
  return auth.canAny(required)
}

async function refresh() {
  loading.value = true
  try {
    const canLoadUsers = canAny(['user.read', 'user.create', 'user.update', 'user.delete'])
    const canLoadDepartments = canAny([
      'department.read',
      'department.create',
      'department.update',
      'department.delete',
      'user.create',
      'user.update',
    ])
    const canLoadRoles = canAny([
      'role.read',
      'role.create',
      'role.update',
      'role.delete',
      'user.create',
      'user.update',
    ])
    const canLoadPermissions = canAny(['role.read', 'role.create', 'role.update', 'role.delete', 'user.update'])
    const [userResponse, departmentResponse, roleResponse, permissionResponse, auditResponse] =
      await Promise.all([
        canLoadUsers ? client.get<{ users: ManagedUser[] }>('/users?limit=200') : null,
        canLoadDepartments ? client.get<{ departments: Department[] }>('/departments') : null,
        canLoadRoles ? client.get<{ roles: Role[] }>('/roles') : null,
        canLoadPermissions ? client.get<{ permissions: Permission[] }>('/permissions') : null,
        canAny(['audit.read'])
          ? client.get<{ audit_logs: AuditLog[] }>('/audit-logs?limit=100')
          : null,
      ])
    users.value = userResponse?.data.users || []
    departments.value = departmentResponse?.data.departments || []
    roles.value = roleResponse?.data.roles || []
    permissions.value = permissionResponse?.data.permissions || []
    audits.value = auditResponse?.data.audit_logs || []
  } catch (error) {
    message.error(apiErrorMessage(error))
  } finally {
    loading.value = false
  }
}

function selectTab(key: string) {
  void router.replace({ query: { ...route.query, view: key } })
}

function resetUser() {
  Object.assign(userForm, {
    username: '',
    nickname: '',
    password: '',
    department_id: '',
    role_ids: [],
  })
}

async function createUser() {
  if (!userForm.department_id) {
    message.warning('请选择用户所属部门')
    return
  }
  saving.value = true
  try {
    await client.post('/users', userForm)
    message.success('用户已创建')
    userFormOpen.value = false
    resetUser()
    await refresh()
  } catch (error) {
    message.error(apiErrorMessage(error))
  } finally {
    saving.value = false
  }
}

function resetDepartment() {
  Object.assign(departmentForm, { name: '', description: '' })
  editingDepartmentID.value = ''
}

function editDepartment(item: Department) {
  Object.assign(departmentForm, { name: item.name, description: item.description || '' })
  editingDepartmentID.value = item.id
  departmentFormOpen.value = true
}

async function saveDepartment() {
  if (!departmentForm.name.trim()) {
    message.warning('请输入部门名称')
    return
  }
  saving.value = true
  try {
    if (editingDepartmentID.value) {
      await client.put(`/departments/${editingDepartmentID.value}`, departmentForm)
      message.success('部门已更新')
    } else {
      await client.post('/departments', departmentForm)
      message.success('部门已创建')
    }
    departmentFormOpen.value = false
    resetDepartment()
    await refresh()
  } catch (error) {
    message.error(apiErrorMessage(error))
  } finally {
    saving.value = false
  }
}

function removeDepartment(item: Department) {
  Modal.confirm({
    title: `删除部门“${item.name}”？`,
    content: item.member_count > 0
      ? `该部门仍有 ${item.member_count} 名成员，请先调整成员所属部门。`
      : '仅没有成员、业务资源和历史记录的部门可以删除；删除后无法恢复。',
    okText: '删除',
    okType: 'danger',
    cancelText: '取消',
    okButtonProps: { disabled: item.member_count > 0 },
    async onOk() {
      try {
        await client.delete(`/departments/${item.id}`)
        message.success('部门已删除')
        await refresh()
      } catch (error) {
        message.error(apiErrorMessage(error))
      }
    },
  })
}

function openUserDepartment(item: ManagedUser) {
  editingUser.value = item
  userDepartmentForm.department_id = item.department_id || ''
  userDepartmentOpen.value = true
}

async function saveUserDepartment() {
  if (!editingUser.value || !userDepartmentForm.department_id) {
    message.warning('请选择用户所属部门')
    return
  }
  saving.value = true
  try {
    await client.put(`/users/${editingUser.value.id}/department`, userDepartmentForm)
    message.success('用户所属部门已更新')
    userDepartmentOpen.value = false
    await refresh()
  } catch (error) {
    message.error(apiErrorMessage(error))
  } finally {
    saving.value = false
  }
}

function resetRole() {
  Object.assign(roleForm, {
    name: '',
    display_name: '',
    description: '',
    permissions: [],
  })
  editingRoleID.value = ''
}

function editRole(item: Role) {
  Object.assign(roleForm, {
    name: item.name,
    display_name: item.display_name,
    description: item.description || '',
    permissions: [...item.permissions],
  })
  editingRoleID.value = item.id
  roleFormOpen.value = true
}

async function saveRole() {
  saving.value = true
  try {
    if (editingRoleID.value) await client.put(`/roles/${editingRoleID.value}`, roleForm)
    else await client.post('/roles', roleForm)
    message.success(editingRoleID.value ? '角色已更新' : '角色已创建')
    roleFormOpen.value = false
    resetRole()
    await refresh()
  } catch (error) {
    message.error(apiErrorMessage(error))
  } finally {
    saving.value = false
  }
}

function removeRole(item: Role) {
  Modal.confirm({
    title: `删除角色“${item.display_name}”？`,
    content: '仅未分配给任何用户的角色可以删除；删除后无法恢复。',
    okText: '删除',
    okType: 'danger',
    cancelText: '取消',
    async onOk() {
      try {
        await client.delete(`/roles/${item.id}`)
        message.success('角色已删除')
        await refresh()
      } catch (error) {
        message.error(apiErrorMessage(error))
      }
    },
  })
}

function removeUser(item: ManagedUser) {
  Modal.confirm({
    title: `删除用户“${item.nickname || item.username}”？`,
    content: '账户、个人 Git 令牌和权限配置会被删除；部门业务资源和审计记录会保留。若个人令牌仍被仓库引用，需要先为仓库更换凭据。',
    okText: '删除',
    okType: 'danger',
    cancelText: '取消',
    async onOk() {
      try {
        await client.delete(`/users/${item.id}`)
        message.success('用户已删除')
        await refresh()
      } catch (error) {
        message.error(apiErrorMessage(error))
        throw error
      }
    },
  })
}

async function toggleUser(item: ManagedUser) {
  try {
    await client.patch(`/users/${item.id}/status`, { active: !item.is_active })
    message.success(item.is_active ? '用户已停用' : '用户已启用')
    await refresh()
  } catch (error) {
    message.error(apiErrorMessage(error))
  }
}

function editAccess(item: ManagedUser) {
  editingUser.value = item
  accessForm.role_ids = [...item.role_ids]
  accessForm.effects = {}
  permissions.value.forEach((permission) => {
    accessForm.effects[permission.code] = 'inherit'
  })
  item.permission_overrides.allow.forEach((permission) => {
    accessForm.effects[permission] = 'allow'
  })
  item.permission_overrides.deny.forEach((permission) => {
    accessForm.effects[permission] = 'deny'
  })
  accessOpen.value = true
}

async function saveAccess() {
  if (!editingUser.value) return
  saving.value = true
  try {
    const allow = Object.entries(accessForm.effects)
      .filter(([, effect]) => effect === 'allow')
      .map(([permission]) => permission)
    const deny = Object.entries(accessForm.effects)
      .filter(([, effect]) => effect === 'deny')
      .map(([permission]) => permission)
    await client.put(`/users/${editingUser.value.id}/roles`, { role_ids: accessForm.role_ids })
    await client.put(`/users/${editingUser.value.id}/permissions`, { allow, deny })
    message.success('用户权限已更新')
    accessOpen.value = false
    await refresh()
  } catch (error) {
    message.error(apiErrorMessage(error))
  } finally {
    saving.value = false
  }
}

function permissionTitle(permission: Permission) {
  return permission.name || `${permission.action_name || permission.action}${permission.resource_name || permission.resource}`
}

function effectivePermissionSummary(value: unknown) {
  if (!Array.isArray(value)) return '全部'
  if (value.includes('*')) return '全部'
  return `${value.length} 项`
}

watch(
  active,
  (value) => {
    if (value && route.query.view !== value) selectTab(value)
  },
  { immediate: true },
)
onMounted(refresh)
</script>

<template>
  <section>
    <PageToolbar :description="toolbarDescription">
      <a-button :loading="loading" @click="refresh">
        <RefreshCw :size="15" />刷新
      </a-button>
      <a-button
        v-if="active === 'users' && canAny(['user.create'])"
        type="primary"
        @click="resetUser(); userFormOpen = true"
      >
        <Plus :size="15" />创建用户
      </a-button>
      <a-button
        v-if="active === 'departments' && canAny(['department.create'])"
        type="primary"
        @click="resetDepartment(); departmentFormOpen = true"
      >
        <Plus :size="15" />创建部门
      </a-button>
      <a-button
        v-if="active === 'roles' && canAny(['role.create'])"
        type="primary"
        @click="resetRole(); roleFormOpen = true"
      >
        <Plus :size="15" />创建角色
      </a-button>
    </PageToolbar>

    <a-segmented
      :value="active"
      :options="tabs.map((item) => ({ value: item.key, label: item.label }))"
      class="edo-page-tabs"
      aria-label="访问控制页面"
      @change="(value: string) => selectTab(value)"
    />

    <div class="vben-card">
      <ResourceTable
        v-if="active === 'users'"
        :rows="users"
        :columns="userColumns"
        :loading="loading"
      >
        <template #cell-department_name="{ row, value }">
          <a-tag v-if="row.is_superuser" color="purple">全部部门</a-tag>
          <span v-else>{{ value || '未分配' }}</span>
        </template>
        <template #cell-role_ids="{ row }">
          <div class="tag-list">
            <a-tag v-if="row.is_superuser" color="purple">超级管理员</a-tag>
            <a-tag v-for="id in row.role_ids" :key="String(id)">
              {{ roles.find((role) => role.id === id)?.display_name || id }}
            </a-tag>
          </div>
        </template>
        <template #cell-effective_permissions="{ value }">
          {{ effectivePermissionSummary(value) }}
        </template>
        <template #actions="{ row }">
          <template v-if="!row.is_superuser">
            <a-button
              v-if="auth.user?.is_superuser && canAny(['user.update'])"
              type="link"
              @click="openUserDepartment(row as ManagedUser)"
            >调整部门</a-button>
            <a-button
              v-if="canAny(['user.update'])"
              type="link"
              @click="editAccess(row as ManagedUser)"
            >配置权限</a-button>
            <a-button
              v-if="canAny(['user.update'])"
              type="link"
              :danger="row.is_active"
              @click="toggleUser(row as ManagedUser)"
            >{{ row.is_active ? '停用' : '启用' }}</a-button>
            <a-button
              v-if="canAny(['user.delete'])"
              type="link"
              danger
              @click="removeUser(row as ManagedUser)"
            >删除</a-button>
          </template>
        </template>
      </ResourceTable>

      <ResourceTable
        v-else-if="active === 'departments'"
        :rows="departments"
        :columns="departmentColumns"
        :loading="loading"
      >
        <template #cell-member_count="{ value }">{{ value || 0 }} 人</template>
        <template #actions="{ row }">
          <a-button
            v-if="canAny(['department.update'])"
            type="link"
            @click="editDepartment(row as Department)"
          >编辑</a-button>
          <a-button
            v-if="canAny(['department.delete'])"
            type="link"
            danger
            :disabled="row.is_default"
            :title="row.is_default ? '默认部门不能删除' : '删除部门'"
            @click="removeDepartment(row as Department)"
          >删除</a-button>
        </template>
      </ResourceTable>

      <ResourceTable
        v-else-if="active === 'roles'"
        :rows="roles"
        :columns="roleColumns"
        :loading="loading"
      >
        <template #cell-permissions="{ value }">
          {{ Array.isArray(value) ? value.length : 0 }} 项
        </template>
        <template #actions="{ row }">
          <a-button
            v-if="canAny(['role.update'])"
            type="link"
            @click="editRole(row as Role)"
          >编辑</a-button>
          <a-button
            v-if="canAny(['role.delete'])"
            type="link"
            danger
            @click="removeRole(row as Role)"
          >删除</a-button>
        </template>
      </ResourceTable>

      <ResourceTable v-else :rows="audits" :columns="auditColumns" :loading="loading">
        <template #cell-result="{ value }">
          <a-tag :color="value === 'success' ? 'success' : 'error'">{{ value }}</a-tag>
        </template>
      </ResourceTable>
    </div>

    <a-modal
      v-model:open="userFormOpen"
      title="创建用户"
      :confirm-loading="saving"
      ok-text="创建"
      cancel-text="取消"
      @ok="createUser"
    >
      <a-form layout="vertical">
        <a-form-item label="用户名" required>
          <a-input v-model:value="userForm.username" maxlength="32" />
        </a-form-item>
        <a-form-item label="昵称">
          <a-input v-model:value="userForm.nickname" maxlength="64" />
        </a-form-item>
        <a-form-item label="所属部门" required>
          <a-select
            v-model:value="userForm.department_id"
            placeholder="请选择部门"
            :options="departments.map((department) => ({ value: department.id, label: department.name }))"
          />
        </a-form-item>
        <a-form-item label="初始密码" required>
          <a-input-password v-model:value="userForm.password" minlength="12" maxlength="128" />
        </a-form-item>
        <a-form-item label="角色">
          <a-select
            v-model:value="userForm.role_ids"
            mode="multiple"
            :options="roles.map((role) => ({ value: role.id, label: role.display_name }))"
          />
        </a-form-item>
      </a-form>
    </a-modal>

    <a-modal
      v-model:open="departmentFormOpen"
      :title="editingDepartmentID ? '编辑部门' : '创建部门'"
      :confirm-loading="saving"
      :ok-text="editingDepartmentID ? '保存' : '创建'"
      cancel-text="取消"
      @ok="saveDepartment"
    >
      <a-form layout="vertical">
        <a-form-item label="部门名称" required>
          <a-input v-model:value="departmentForm.name" maxlength="64" />
        </a-form-item>
        <a-form-item label="说明">
          <a-textarea v-model:value="departmentForm.description" :rows="3" maxlength="255" />
        </a-form-item>
      </a-form>
    </a-modal>

    <a-modal
      v-model:open="userDepartmentOpen"
      :title="`调整 ${editingUser?.nickname || editingUser?.username || ''} 的部门`"
      :confirm-loading="saving"
      ok-text="保存"
      cancel-text="取消"
      @ok="saveUserDepartment"
    >
      <a-alert
        type="warning"
        show-icon
        message="调整部门只影响用户之后创建的资源；历史资源仍归原部门，避免数据意外转移。用户的现有会话会立即失效。"
        class="form-alert"
      />
      <a-form layout="vertical">
        <a-form-item label="所属部门" required>
          <a-select
            v-model:value="userDepartmentForm.department_id"
            placeholder="请选择部门"
            :options="departments.map((department) => ({ value: department.id, label: department.name }))"
          />
        </a-form-item>
      </a-form>
    </a-modal>

    <a-drawer
      v-model:open="roleFormOpen"
      :title="editingRoleID ? '编辑角色' : '创建角色'"
      width="680"
    >
      <a-form layout="vertical">
        <a-form-item label="角色标识（技术字段）" required>
          <a-input v-model:value="roleForm.name" :disabled="Boolean(editingRoleID)" />
        </a-form-item>
        <a-form-item label="角色名称" required>
          <a-input v-model:value="roleForm.display_name" />
        </a-form-item>
        <a-form-item label="说明">
          <a-textarea v-model:value="roleForm.description" :rows="3" />
        </a-form-item>

        <a-checkbox-group v-model:value="roleForm.permissions" class="permission-catalog">
          <section v-for="group in permissionGroups" :key="group.name" class="permission-group">
            <h3>{{ group.name }}</h3>
            <div v-for="resource in group.resources" :key="resource.key" class="permission-resource">
              <h4>{{ resource.name }}</h4>
              <div class="permission-options">
                <a-checkbox
                  v-for="item in resource.items"
                  :key="item.code"
                  :value="item.code"
                  :class="{ dangerous: item.dangerous }"
                >
                  <span class="permission-name">
                    {{ permissionTitle(item) }}
                    <a-tag v-if="item.dangerous" color="error">高风险</a-tag>
                  </span>
                  <small>{{ item.description }}</small>
                  <code>{{ item.code }}</code>
                </a-checkbox>
              </div>
            </div>
          </section>
        </a-checkbox-group>

        <a-button type="primary" block :loading="saving" @click="saveRole">保存</a-button>
      </a-form>
    </a-drawer>

    <a-drawer
      v-model:open="accessOpen"
      :title="`配置 ${editingUser?.nickname || editingUser?.username || ''}`"
      width="800"
    >
      <a-alert
        type="info"
        show-icon
        message="用户级拒绝优先于角色允许；“继承角色”不会创建用户规则。"
      />
      <h3>角色</h3>
      <a-checkbox-group
        v-model:value="accessForm.role_ids"
        :options="roles.map((role) => ({ value: role.id, label: role.display_name }))"
      />
      <h3>用户权限例外</h3>
      <section v-for="group in permissionGroups" :key="group.name" class="override-group">
        <h3>{{ group.name }}</h3>
        <div v-for="resource in group.resources" :key="resource.key" class="override-resource">
          <h4>{{ resource.name }}</h4>
          <div v-for="item in resource.items" :key="item.code" class="override-row">
            <span>
              <b>
                {{ permissionTitle(item) }}
                <a-tag v-if="item.dangerous" color="error">高风险</a-tag>
              </b>
              <small>{{ item.description }}</small>
              <code>{{ item.code }}</code>
            </span>
            <a-segmented
              v-model:value="accessForm.effects[item.code]"
              :options="[
                { label: '继承', value: 'inherit' },
                { label: '允许', value: 'allow' },
                { label: '拒绝', value: 'deny' },
              ]"
            />
          </div>
        </div>
      </section>
      <a-button type="primary" block :loading="saving" @click="saveAccess">保存权限</a-button>
    </a-drawer>
  </section>
</template>

<style scoped>
.tag-list {
  display: flex;
  flex-wrap: wrap;
  gap: 3px;
}

.form-alert {
  margin-bottom: 18px;
}

.permission-catalog {
  display: block;
}

.permission-group,
.override-group {
  margin: 16px 0;
  padding: 15px;
  border: 1px solid var(--edo-border);
  border-radius: 9px;
}

.permission-group > h3,
.override-group > h3 {
  margin: 0 0 13px;
  font-size: 15px;
}

.permission-resource,
.override-resource {
  padding: 12px 0;
  border-top: 1px solid var(--edo-border);
}

.permission-resource h4,
.override-resource h4 {
  margin: 0 0 10px;
  color: var(--edo-muted);
  font-size: 12px;
  font-weight: 600;
}

.permission-options {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 10px;
}

.permission-options :deep(.ant-checkbox-wrapper) {
  min-width: 0;
  margin-inline-start: 0;
  padding: 10px;
  border: 1px solid var(--edo-border);
  border-radius: 7px;
  align-items: flex-start;
}

.permission-options :deep(.ant-checkbox-wrapper.dangerous) {
  border-color: color-mix(in srgb, #ff4d4f 28%, var(--edo-border));
}

.permission-name,
.permission-options small,
.permission-options code,
.override-row span b,
.override-row span small,
.override-row span code {
  display: block;
}

.permission-name,
.override-row b {
  color: var(--edo-text);
}

.permission-options small,
.override-row small {
  margin-top: 3px;
  color: var(--edo-muted);
  font-size: 12px;
}

.permission-options code,
.override-row code {
  margin-top: 4px;
  color: var(--edo-muted);
  font-size: 10px;
  opacity: 0.72;
}

.permission-name :deep(.ant-tag),
.override-row b :deep(.ant-tag) {
  margin-inline-start: 6px;
}

.override-row {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 14px;
  padding: 10px 0;
  border-top: 1px dashed var(--edo-border);
}

h3 {
  margin: 24px 0 12px;
}

@media (max-width: 650px) {
  .permission-options {
    grid-template-columns: 1fr;
  }

  .override-row {
    align-items: flex-start;
    flex-direction: column;
  }
}
</style>
