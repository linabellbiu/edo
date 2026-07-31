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
import { useAuthStore } from '@/stores/auth'

const auth = useAuthStore()
const route = useRoute()
const router = useRouter()
const { t } = useI18n()
type EnvironmentFormMode = 'create' | 'details' | 'hosts'
const environments = ref<InfrastructureEnvironment[]>([])
const hosts = ref<InfrastructureHost[]>([])
const selectedID = ref('')
const formOpen = ref(false)
const editingID = ref('')
const loading = ref(false)
const saving = ref(false)
const formMode = ref<EnvironmentFormMode>('create')
const form = reactive({ name: '', description: '', host_ids: [] as string[] })

const selected = computed(() => environments.value.find(item => item.id === selectedID.value))
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
      label: `${host.name} · ${host.is_builtin ? '本地 · ' : ''}${capabilities} · ${t('environment.hostPicker.environmentCount', { count: environmentCount })}`,
      disabled: !host.is_active,
    }
  }))

async function refresh() {
  loading.value = true
  try {
    const [environmentItems, hostItems] = await Promise.all([listEnvironments(), listHosts()])
    environments.value = environmentItems
    hosts.value = hostItems
    if (!environments.value.some(item => item.id === selectedID.value)) selectedID.value = environments.value[0]?.id ?? ''
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
  try {
    await client.patch(`/environments/${environment.id}/status`, { active: !environment.is_active })
    await refresh()
  } catch (error) {
    message.error(apiErrorMessage(error))
  }
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
  if (value !== '1' || !auth.canAny(['deployment.manage'])) return
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
      <a-button v-if="auth.canAny(['deployment.manage'])" type="primary" @click="create"><Plus :size="15" />创建环境</a-button>
    </PageToolbar>

    <div class="environment-layout vben-card">
      <aside class="environment-list">
        <header>
          <div><strong>环境</strong><small>自定义基础设施分组</small></div>
          <span>{{ environments.length }}</span>
        </header>
        <div class="environment-list-scroll">
          <button
            v-for="environment in environments"
            :key="environment.id"
            :class="{ active: selectedID === environment.id }"
            @click="selectedID = environment.id"
          >
            <span class="environment-icon"><Boxes /></span>
            <span>
              <strong>{{ environment.name }}</strong>
              <small>{{ environment.hosts.length }} 台主机</small>
            </span>
            <i :class="{ inactive: !environment.is_active }" />
          </button>
          <a-empty v-if="!environments.length && !loading" description="还没有环境" />
        </div>
      </aside>

      <main v-if="selected" class="environment-detail">
        <header class="detail-header">
          <div>
            <span>基础设施环境</span>
            <h3>{{ selected.name }}</h3>
            <p>{{ selected.description || '尚未填写说明' }}</p>
          </div>
          <div class="detail-actions">
            <a-tag :color="selected.is_active ? 'success' : 'default'">{{ selected.is_active ? '已启用' : '已停用' }}</a-tag>
            <a-button v-if="auth.canAny(['deployment.manage'])" @click="edit(selected, 'details')"><Pencil :size="14" />{{ t('environment.actions.editDetails') }}</a-button>
            <a-button v-if="auth.canAny(['deployment.manage'])" @click="toggle(selected)">{{ selected.is_active ? '停用' : '启用' }}</a-button>
          </div>
        </header>

        <div class="environment-summary">
          <div><Server /><span><small>主机数量</small><strong>{{ selected.hosts.length }}</strong></span></div>
          <div><Boxes /><span><small>主机能力</small><strong>{{ selected.hosts.reduce((total, host) => total + host.capabilities.length, 0) }}</strong></span></div>
        </div>

        <section class="assigned-hosts">
          <header>
            <div><h4>所属主机</h4><p>{{ t('environment.hosts.sharedDescription') }}</p></div>
            <a-button v-if="auth.canAny(['deployment.manage'])" size="small" @click="edit(selected, 'hosts')">{{ t('environment.actions.adjustHosts') }}</a-button>
          </header>
          <div class="host-grid">
            <article v-for="host in selected.hosts" :key="host.id">
              <span class="host-status" :class="{ inactive: !host.is_active }" />
              <div class="host-main">
                <strong>{{ host.name }}</strong>
                <small>{{ host.is_builtin ? '本地' : `${host.address}:${host.ssh_port} · ${host.ssh_username}` }}</small>
              </div>
              <div class="host-runtime-icons">
                <span v-for="capability in host.capabilities" :key="capability.kind" :title="capabilityName(capability.kind)">
                  <RuntimeBrandIcon :kind="capability.kind" />
                </span>
              </div>
              <small class="capability-label">{{ capabilityLabel(host) }}</small>
            </article>
            <a-empty v-if="!selected.hosts.length" description="该环境还没有主机" />
          </div>
        </section>
      </main>
      <div v-else class="empty-panel"><a-empty description="选择或创建环境" /></div>
    </div>

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
              />
              <a-button
                v-if="auth.canAny(['cluster.manage'])"
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
.environment-layout{display:grid;min-height:590px;grid-template-columns:290px minmax(0,1fr);overflow:hidden}.environment-list{border-right:1px solid var(--edo-border);background:var(--edo-surface-soft)}.environment-list>header{display:flex;height:64px;align-items:center;justify-content:space-between;padding:0 16px}.environment-list>header strong,.environment-list>header small{display:block}.environment-list>header small{margin-top:2px;color:var(--edo-muted);font-size:12px}.environment-list>header>span{min-width:27px;padding:3px 8px;border-radius:999px;color:var(--edo-muted);background:var(--edo-surface);text-align:center}.environment-list-scroll{max-height:calc(100vh - 230px);overflow-y:auto;padding:0 8px 10px}.environment-list-scroll>button{display:grid;width:100%;min-height:68px;align-items:center;grid-template-columns:38px minmax(0,1fr) 8px;gap:10px;margin:3px 0;padding:9px 10px;border:0;border-radius:10px;background:transparent;cursor:pointer;text-align:left}.environment-list-scroll>button:hover{background:var(--edo-surface)}.environment-list-scroll>button.active{background:var(--edo-primary-soft);box-shadow:inset 3px 0 var(--edo-primary)}.environment-list-scroll>button span:nth-child(2){min-width:0}.environment-list-scroll>button strong,.environment-list-scroll>button small{display:block;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}.environment-list-scroll>button small{margin-top:3px;color:var(--edo-muted);font-size:12px}.environment-icon{display:grid;width:38px;height:38px;place-items:center;border-radius:11px;color:#4f6ef7;background:var(--edo-surface)}.environment-icon svg{width:19px}.environment-list-scroll>button i{width:8px;height:8px;border-radius:50%;background:#28b66e}.environment-list-scroll>button i.inactive{background:#a8adb7}.environment-detail{min-width:0;padding:24px}.detail-header{display:flex;align-items:flex-start;justify-content:space-between;gap:20px}.detail-header>div:first-child>span,.detail-header p{color:var(--edo-muted)}.detail-header h3{margin:3px 0 1px;font-size:22px}.detail-header p{margin:0}.detail-actions{display:flex;align-items:center;gap:8px}.environment-summary{display:grid;grid-template-columns:repeat(2,minmax(0,1fr));gap:12px;margin-top:24px}.environment-summary>div{display:flex;align-items:center;gap:12px;padding:15px;border:1px solid var(--edo-border);border-radius:11px;background:var(--edo-surface-soft)}.environment-summary svg{width:21px;color:var(--edo-primary)}.environment-summary small,.environment-summary strong{display:block}.environment-summary small{color:var(--edo-muted)}.environment-summary strong{font-size:18px}.assigned-hosts{margin-top:28px}.assigned-hosts>header{display:flex;align-items:flex-start;justify-content:space-between}.assigned-hosts h4,.assigned-hosts p{margin:0}.assigned-hosts p{margin-top:3px;color:var(--edo-muted)}.host-grid{display:grid;grid-template-columns:repeat(2,minmax(0,1fr));gap:12px;margin-top:14px}.host-grid article{display:grid;min-height:86px;align-items:center;grid-template-columns:9px minmax(0,1fr) auto;gap:10px;padding:14px;border:1px solid var(--edo-border);border-radius:12px;background:var(--edo-surface-soft)}.host-status{width:8px;height:8px;border-radius:50%;background:#28b66e}.host-status.inactive{background:#a8adb7}.host-main{min-width:0}.host-main strong,.host-main small{display:block;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}.host-main small,.capability-label{color:var(--edo-muted)}.host-runtime-icons{display:flex;gap:5px}.host-runtime-icons span{display:grid;width:28px;height:28px;place-items:center;border-radius:8px;background:var(--edo-surface)}.host-runtime-icons :deep(svg){width:18px;height:18px}.capability-label{grid-column:2/-1;font-size:12px}.form-grid{display:grid;grid-template-columns:1fr 1fr;gap:0 14px}.span-2{grid-column:1/-1}.relation-select{display:flex;gap:8px}.relation-select>.ant-select{min-width:0;flex:1}.create-relation{width:34px;flex:0 0 34px;padding:0}.field-hint{margin-top:6px;color:var(--edo-muted);font-size:12px}.host-selection-hint{display:flex;align-items:flex-start;justify-content:space-between;gap:12px}.host-selection-hint strong{flex:0 0 auto;color:var(--edo-text);font-weight:600}@media(max-width:900px){.environment-layout{grid-template-columns:1fr}.environment-list{max-height:270px;border-right:0;border-bottom:1px solid var(--edo-border)}.environment-list-scroll{max-height:200px}.environment-summary,.host-grid{grid-template-columns:1fr}}@media(max-width:640px){.environment-detail{padding:16px}.detail-header{flex-direction:column}.form-grid{grid-template-columns:1fr}.span-2{grid-column:auto}.host-selection-hint{flex-direction:column;gap:3px}}
</style>
