<script setup lang="ts">
import { computed, onMounted, reactive, ref, watch } from 'vue'
import { message } from 'ant-design-vue'
import { Boxes, Pencil, Plus, RefreshCw, Server } from 'lucide-vue-next'
import { useI18n } from 'vue-i18n'
import { useRoute, useRouter } from 'vue-router'

import client from '@/api/client'
import {
  environmentIDsOf,
  listEnvironments,
  listHosts,
  type InfrastructureEnvironment,
  type InfrastructureHost,
} from '@/api/infrastructure'
import { apiErrorMessage } from '@/api/resources'
import PageToolbar from '@/components/PageToolbar.vue'
import RuntimeBrandIcon from '@/components/RuntimeBrandIcon.vue'
import ResourceDetailDrawer from '@/components/ResourceDetailDrawer.vue'
import ResourceRowActions from '@/components/ResourceRowActions.vue'
import { useResourceList } from '@/utils/resourceList'
import { formatDateTime } from '@/utils/time'
import { useAuthStore } from '@/stores/auth'

const auth = useAuthStore()
const route = useRoute()
const router = useRouter()
const { t } = useI18n()
type EnvironmentFormMode = 'create' | 'details' | 'hosts'
const environments = ref<InfrastructureEnvironment[]>([])
const hosts = ref<InfrastructureHost[]>([])
const { detailID: selectedID, detailOpen, selected, pagination, openDetails, changePage } = useResourceList(() => environments.value, () => typeof route.query.environment === 'string' ? route.query.environment : '')
const mutatingID = ref('')
const columns = computed(() => [
  { key: 'name', title: t('nav.environments'), width: 260 },
  { key: 'status', title: t('resourceList.status'), width: 110 },
  { key: 'hosts', title: t('resourceList.hosts'), width: 300 },
  { key: 'capabilities', title: t('resourceList.capabilities'), width: 220 },
  { key: 'updated', title: t('resourceList.updated'), width: 175 },
  { key: 'actions', title: t('planList.actions'), width: 235, fixed: 'right' as const, align: 'right' as const },
])
const formOpen = ref(false)
const editingID = ref('')
const loading = ref(false)
const saving = ref(false)
const formMode = ref<EnvironmentFormMode>('create')
const form = reactive({ name: '', description: '', host_ids: [] as string[] })

const formTitle = computed(() => {
  if (formMode.value === 'details') return t('environment.drawer.editDetails')
  if (formMode.value === 'hosts') return t('environment.drawer.adjustHosts', { name: form.name })
  return t('environment.drawer.create')
})
const hostOptions = computed(() => hosts.value
  .map(host => {
    const capabilities = host.capabilities.map(item => capabilityName(item.kind)).join(' + ') || '未配置能力'
    const environmentCount = environmentIDsOf(host).length
    return {
      value: host.id,
      label: host.name,
      location: host.is_builtin ? '本地' : `${host.address}:${host.ssh_port}`,
      capabilities,
      environmentCount,
      disabled: !host.is_active,
    }
  }))

function resourceViewHref(path: string, query: Record<string, string>) {
  return router.resolve({ path, query }).href
}

async function refresh() {
  loading.value = true
  try {
    const [environmentItems, hostItems] = await Promise.all([listEnvironments(), listHosts()])
    environments.value = environmentItems
    hosts.value = hostItems
  } catch (error) {
    message.error(apiErrorMessage(error))
  } finally {
    loading.value = false
  }
}

function reset() {
  Object.assign(form, { name: '', description: '', host_ids: [] })
  editingID.value = ''
  formMode.value = 'create'
}

function create() {
  reset()
  formMode.value = 'create'
  formOpen.value = true
}

function edit(environment: InfrastructureEnvironment, mode: Exclude<EnvironmentFormMode, 'create'>) {
  editingID.value = environment.id
  formMode.value = mode
  Object.assign(form, {
    name: environment.name,
    description: environment.description || '',
    host_ids: environment.hosts.map(host => host.id),
  })
  formOpen.value = true
}

async function save() {
  if (formMode.value !== 'hosts' && !form.name.trim()) {
    message.error(t('environment.message.nameRequired'))
    return
  }
  saving.value = true
  try {
    let response: { data: { environment: InfrastructureEnvironment } }
    if (!editingID.value) {
      response = await client.post<{ environment: InfrastructureEnvironment }>('/environments', {
        name: form.name.trim(), description: form.description.trim(), host_ids: form.host_ids,
      })
    } else if (formMode.value === 'details') {
      response = await client.patch<{ environment: InfrastructureEnvironment }>(`/environments/${editingID.value}`, {
        name: form.name.trim(), description: form.description.trim(),
      })
    } else {
      response = await client.put<{ environment: InfrastructureEnvironment }>(`/environments/${editingID.value}/hosts`, {
        host_ids: form.host_ids,
      })
    }
    selectedID.value = response.data.environment.id
    message.success(editingID.value
      ? t(formMode.value === 'hosts' ? 'environment.message.hostsUpdated' : 'environment.message.detailsUpdated')
      : t('environment.message.created'))
    formOpen.value = false
    reset()
    await refresh()
  } catch (error) {
    message.error(apiErrorMessage(error))
  } finally {
    saving.value = false
  }
}

async function toggle(environment: InfrastructureEnvironment) {
  if (mutatingID.value) return
  mutatingID.value = environment.id
  try {
    await client.patch(`/environments/${environment.id}/status`, { active: !environment.is_active })
    await refresh()
  } catch (error) {
    message.error(apiErrorMessage(error))
  } finally { mutatingID.value = '' }
}

function createHost() {
  formOpen.value = false
  void router.push({ path: '/hosts', query: { create: '1' } })
}

function capabilityLabel(host: InfrastructureHost) {
  return host.capabilities.map(item => capabilityName(item.kind)).join(' + ') || '未配置能力'
}

function capabilityName(kind: InfrastructureHost['capabilities'][number]['kind']) {
  if (kind === 'ssh') return 'SSH 命令部署'
  if (kind === 'docker') return 'Docker'
  if (kind === 'local_exec') return '直接终端执行'
  return 'Kubernetes'
}

watch([() => route.query.create, () => auth.loaded], ([value]) => {
  if (value !== '1' || !auth.canAny(['deployment.create'])) return
  create()
  const query = { ...route.query }
  delete query.create
  void router.replace({ query })
}, { immediate: true })

onMounted(refresh)
</script>

<template>
  <section>
    <PageToolbar description="环境是可自定义的基础设施分组，用来明确主机归属；命令部署和运行时能力统一在“主机与集群”中维护。">
      <a-button :loading="loading" @click="refresh"><RefreshCw :size="15" />刷新</a-button>
      <a-button v-if="auth.canAny(['deployment.create'])" type="primary" @click="create"><Plus :size="15" />创建环境</a-button>
    </PageToolbar>

    <div class="vben-card">
      <a-table class="resource-list-table" :columns="columns" :data-source="environments" :loading="loading" :pagination="pagination" :scroll="{ x: 1300 }" row-key="id" table-layout="fixed" :locale="{ emptyText: t('resourceList.environmentEmpty') }" :row-class-name="(row: InfrastructureEnvironment) => detailOpen && selectedID === row.id ? 'resource-selected' : ''" @change="changePage">
        <template #bodyCell="{ column, record: environment }">
          <div v-if="column.key === 'name'">
            <button class="resource-name" @click="openDetails(environment)">{{ environment.name }}</button>
            <a-tooltip :title="environment.description" :mouse-enter-delay="0.1"><small class="resource-secondary resource-description">{{ environment.description || t('planList.descriptionEmpty') }}</small></a-tooltip>
          </div>
          <a-tag v-else-if="column.key === 'status'" :color="environment.is_active ? 'success' : 'default'">{{ environment.is_active ? t('planList.enabled') : t('planList.disabled') }}</a-tag>
          <div v-else-if="column.key === 'hosts'">
            <span>{{ environment.hosts.length }} {{ t('resourceList.hosts') }}</span>
            <a-tooltip :title="environment.hosts.map((host: InfrastructureHost) => host.name).join('、')" :mouse-enter-delay="0.1"><small class="resource-secondary">{{ environment.hosts.map((host: InfrastructureHost) => host.name).join('、') || '—' }}</small></a-tooltip>
          </div>
          <span v-else-if="column.key === 'capabilities'">{{ [...new Set(environment.hosts.flatMap((host: InfrastructureHost) => host.capabilities.map(cap => capabilityName(cap.kind))))].join(' / ') || '—' }}</span>
          <time v-else-if="column.key === 'updated'">{{ formatDateTime(environment.updated_at || environment.created_at) }}</time>
          <ResourceRowActions v-else-if="column.key === 'actions'" :more="auth.canAny(['deployment.update'])" :loading="mutatingID === environment.id">
            <a-button size="small" @click="openDetails(environment)">{{ t('resourceList.details') }}</a-button>
            <a-button v-if="auth.canAny(['deployment.update'])" size="small" @click="edit(environment, 'hosts')">{{ t('environment.actions.adjustHosts') }}</a-button>
            <template #more>
              <a-menu-item @click="edit(environment, 'details')">{{ t('environment.actions.editDetails') }}</a-menu-item>
              <a-menu-item :disabled="Boolean(mutatingID)" @click="toggle(environment)">{{ environment.is_active ? t('planList.disable') : t('planList.enable') }}</a-menu-item>
            </template>
          </ResourceRowActions>
        </template>
      </a-table>
    </div>

    <ResourceDetailDrawer v-model:open="detailOpen" :title="selected?.name">
      <template #actions>
        <a-tag v-if="selected" :color="selected.is_active ? 'success' : 'default'">{{ selected.is_active ? t('planList.enabled') : t('planList.disabled') }}</a-tag>
        <a-button v-if="selected && auth.canAny(['deployment.update'])" size="small" @click="edit(selected, 'details')">{{ t('environment.actions.editDetails') }}</a-button>
        <a-button v-if="selected && auth.canAny(['deployment.update'])" size="small" @click="edit(selected, 'hosts')">{{ t('environment.actions.adjustHosts') }}</a-button>
      </template>
      <section v-if="selected" class="resource-detail-section">
        <h4>{{ t('resourceList.basic') }}</h4>
        <dl class="resource-facts">
          <div><dt>{{ t('resourceList.name') }}</dt><dd>{{ selected.name }}</dd></div>
          <div><dt>{{ t('resourceList.status') }}</dt><dd>{{ selected.is_active ? t('planList.enabled') : t('planList.disabled') }}</dd></div>
          <div><dt>{{ t('resourceList.hostCount') }}</dt><dd>{{ selected.hosts.length }}</dd></div>
          <div><dt>{{ t('resourceList.created') }}</dt><dd>{{ formatDateTime(selected.created_at) }}</dd></div>
          <div><dt>{{ t('resourceList.updated') }}</dt><dd>{{ formatDateTime(selected.updated_at) }}</dd></div>
          <div class="resource-facts-wide"><dt>{{ t('resourceList.description') }}</dt><dd>{{ selected.description || '—' }}</dd></div>
        </dl>
      </section>
      <section v-if="selected" class="resource-detail-section">
        <h4>{{ t('resourceList.hosts') }}</h4>
        <a-table class="resource-list-table" :data-source="selected.hosts" row-key="id" :pagination="false" :scroll="{ x: 650 }" :columns="[{ key: 'name', title: t('resourceList.name'), width: 210 }, { key: 'location', title: t('planList.destination'), width: 220 }, { key: 'capabilities', title: t('resourceList.capabilities'), width: 220 }, { key: 'status', title: t('resourceList.status'), width: 100 }]">
          <template #bodyCell="{ column, record: host }">
            <button v-if="column.key === 'name'" class="resource-name" :disabled="!auth.canAny(['cluster.read'])" @click="router.push({ path: '/hosts', query: { host: host.id } })">{{ host.name }}</button>
            <span v-else-if="column.key === 'location'">{{ host.is_builtin ? '本地' : host.address + ':' + host.ssh_port }}</span>
            <div v-else-if="column.key === 'capabilities'" class="resource-inline-tags"><a-tag v-for="capability in host.capabilities" :key="capability.kind"><RuntimeBrandIcon :kind="capability.kind" :size="13" /> {{ capabilityName(capability.kind) }}</a-tag></div>
            <a-tag v-else-if="column.key === 'status'" :color="host.is_active ? 'success' : 'default'">{{ host.is_active ? t('planList.enabled') : t('planList.disabled') }}</a-tag>
          </template>
        </a-table>
      </section>
    </ResourceDetailDrawer>

    <a-drawer v-model:open="formOpen" :title="formTitle" width="600">
      <a-form layout="vertical">
        <div class="form-grid">
          <a-form-item v-if="formMode !== 'hosts'" class="span-2" label="环境名称" required>
            <a-input v-model:value="form.name" placeholder="例如：上海测试、海外生产" />
          </a-form-item>
          <a-form-item v-if="formMode !== 'hosts'" class="span-2" label="说明">
            <a-textarea v-model:value="form.description" :rows="3" placeholder="说明该环境的用途、区域或约束" />
          </a-form-item>
          <a-form-item v-if="formMode !== 'details'" class="span-2" label="所属主机（可多选）">
            <div class="relation-select">
              <a-select
                v-model:value="form.host_ids"
                mode="multiple"
                allow-clear
                placeholder="选择一个或多个主机"
                :options="hostOptions"
              >
                <template #option="{ value, label, location, capabilities, environmentCount }">
                  <span class="managed-resource-option">
                    <span class="host-option-copy">
                      <strong>{{ label }}</strong>
                      <small>位置：{{ location }}</small>
                      <small>能力：{{ capabilities }}；已关联环境：{{ environmentCount }}</small>
                    </span>
                    <a class="managed-resource-option-view" :href="resourceViewHref('/hosts', { host: String(value) })" target="_blank" rel="noopener noreferrer" @mousedown.stop @click.stop>查看</a>
                  </span>
                </template>
              </a-select>
              <a-button
                v-if="auth.canAny(['cluster.create'])"
                class="create-relation"
                aria-label="添加主机"
                title="添加主机"
                @click="createHost"
              ><Plus :size="16" /></a-button>
            </div>
            <div class="field-hint host-selection-hint">
              <span>{{ t('environment.hostPicker.selectionHint') }}</span>
              <strong>已选 {{ form.host_ids.length }} 台</strong>
            </div>
            <div class="field-hint">{{ t('environment.hostPicker.portConflictHint') }}</div>
            <div class="field-hint">Kubernetes 集群仍作为独立资源接入。</div>
          </a-form-item>
        </div>
      </a-form>
      <template #footer>
        <div class="drawer-actions"><a-button @click="formOpen = false">取消</a-button><a-button type="primary" :loading="saving" @click="save">保存</a-button></div>
      </template>
    </a-drawer>
  </section>
</template>

<style scoped>
.form-grid{display:grid;grid-template-columns:1fr 1fr;gap:0 14px}.span-2{grid-column:1/-1}.relation-select{display:flex;gap:8px}.relation-select>.ant-select{min-width:0;flex:1}.create-relation{width:34px;flex:0 0 34px;padding:0}.host-option-copy{min-width:0;flex:1}.host-option-copy strong,.host-option-copy small{display:block;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}.host-option-copy small{color:var(--edo-muted);font-size:12px}.field-hint{margin-top:6px;color:var(--edo-muted);font-size:12px}.host-selection-hint{display:flex;align-items:flex-start;justify-content:space-between;gap:12px}.host-selection-hint strong{flex:0 0 auto;color:var(--edo-text);font-weight:600}.resource-inline-tags :deep(svg){vertical-align:middle}@media(max-width:640px){.form-grid{grid-template-columns:1fr}.span-2{grid-column:auto}.host-selection-hint{flex-direction:column;gap:3px}}
</style>
