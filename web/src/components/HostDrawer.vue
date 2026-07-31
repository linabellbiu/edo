<script setup lang="ts">
import { computed, reactive, ref, watch } from 'vue'
import { message } from 'ant-design-vue'

import client from '@/api/client'
import type { HostCapabilityKind, InfrastructureHost } from '@/api/infrastructure'
import { apiErrorMessage } from '@/api/resources'
import RuntimeBrandIcon from '@/components/RuntimeBrandIcon.vue'

interface KubernetesClusterOption {
  id: string
  name: string
  is_active: boolean
}

interface HostTestResult {
  test_token: string
  expires_at: string
  fingerprint: string
  docker_version?: string
  kubernetes_version?: string
}

const props = withDefaults(defineProps<{
  open?: boolean
  host?: InfrastructureHost
  clusters?: KubernetesClusterOption[]
}>(), {
  open: false,
  host: undefined,
  clusters: () => [],
})

const emit = defineEmits<{
  'create-cluster': []
  saved: []
  'update:open': [value: boolean]
}>()

const formRef = ref<{ validate: () => Promise<void> }>()
const form = reactive({
  name: '',
  address: '',
  ssh_port: 22,
  ssh_username: '',
  ssh_auth_type: 'private_key' as 'password' | 'private_key',
  password: '',
  private_key: '',
  passphrase: '',
  use_sudo: false,
  capability_kinds: ['ssh'] as HostCapabilityKind[],
  kubernetes_cluster_id: '',
})
const testing = ref(false)
const submitting = ref(false)
const tested = ref<HostTestResult>()
const hydrating = ref(false)
const replaceCredential = ref(false)

const editing = computed(() => Boolean(props.host))
const isLocal = computed(() => props.host?.mode === 'local')
const editingRemote = computed(() => editing.value && !isLocal.value)
const needsCredential = computed(() => !editingRemote.value || replaceCredential.value)
const hasDocker = computed(() => form.capability_kinds.includes('docker'))
const hasKubernetes = computed(() => form.capability_kinds.includes('kubernetes'))

function localCapabilityOption(kind: 'docker' | 'local_exec') {
  return props.host?.capability_options?.find(item => item.kind === kind)
}

function localCapabilityDisabled(kind: 'docker' | 'local_exec') {
  const option = localCapabilityOption(kind)
  return (!option || !option.available) && !form.capability_kinds.includes(kind)
}

function localCapabilityHint(kind: 'docker' | 'local_exec') {
  const option = localCapabilityOption(kind)
  if (option?.available) {
    if (kind === 'docker' && option.version) return `已检测到 Docker ${option.version}`
    return kind === 'docker' ? '已检测到本地 Docker' : '当前运行方式支持直接终端执行'
  }
  return option?.reason || '当前运行环境不支持此能力'
}

watch(form, () => {
  if (!hydrating.value) tested.value = undefined
}, { deep: true })

watch(replaceCredential, () => {
  if (!hydrating.value) tested.value = undefined
})

watch(() => [props.open, props.host] as const, ([open]) => {
  if (!open) return
  hydrating.value = true
  const host = props.host
  Object.assign(form, {
    name: host?.name ?? '',
    address: host?.address ?? '',
    ssh_port: host?.ssh_port || 22,
    ssh_username: host?.ssh_username ?? '',
    ssh_auth_type: host?.ssh_auth_type === 'password' ? 'password' : 'private_key',
    password: '',
    private_key: '',
    passphrase: '',
    use_sudo: Boolean(host?.capabilities.find(item => item.kind === 'docker')?.use_sudo),
    capability_kinds: host
      ? host.capabilities.map(item => item.kind)
      : ['ssh'],
    kubernetes_cluster_id: host?.capabilities.find(item => item.kind === 'kubernetes')?.runtime_id ?? '',
  })
  replaceCredential.value = Boolean(host?.mode === 'ssh' && host.credential_configured === false)
  tested.value = undefined
  queueMicrotask(() => { hydrating.value = false })
}, { immediate: true })

function payload() {
  if (isLocal.value) {
    return {
      name: form.name.trim(),
      mode: 'local',
      capability_kinds: form.capability_kinds.filter(kind => kind === 'docker' || kind === 'local_exec'),
    }
  }
  const useDockerSudo = hasDocker.value && form.use_sudo
  const ssh = needsCredential.value
    ? form.ssh_auth_type === 'password'
      ? { password: form.password, use_sudo: useDockerSudo }
      : {
          private_key: form.private_key,
          passphrase: form.passphrase || undefined,
          use_sudo: useDockerSudo,
        }
    : undefined
  return {
    name: form.name.trim(),
    mode: 'ssh',
    address: form.address.trim(),
    ssh_port: form.ssh_port,
    ssh_username: form.ssh_username.trim(),
    ssh_auth_type: form.ssh_auth_type,
    ssh,
    use_sudo: useDockerSudo,
    reuse_credential: editingRemote.value && !replaceCredential.value,
    capability_kinds: form.capability_kinds,
    kubernetes_cluster_id: hasKubernetes.value ? form.kubernetes_cluster_id || undefined : undefined,
  }
}

async function validate() {
  await formRef.value?.validate()
  if (!isLocal.value && !form.capability_kinds.length) throw new Error('请选择至少一种主机能力')
  if (hasKubernetes.value && !form.kubernetes_cluster_id) throw new Error('请选择 Kubernetes 集群')
}

async function testConnection() {
  try {
    await validate()
  } catch (error) {
    if (error instanceof Error && ['请选择至少一种主机能力', '请选择 Kubernetes 集群'].includes(error.message)) message.error(error.message)
    return
  }
  testing.value = true
  try {
    const path = editingRemote.value && props.host ? `/hosts/${props.host.id}/test` : '/hosts/test'
    const response = await client.post<HostTestResult>(path, payload(), { timeout: 35_000 })
    tested.value = response.data
    message.success('主机连接与所选能力检查通过')
  } catch (error) {
    message.error(apiErrorMessage(error))
  } finally {
    testing.value = false
  }
}

async function submit() {
  try {
    await validate()
  } catch (error) {
    if (error instanceof Error && ['请选择至少一种主机能力', '请选择 Kubernetes 集群'].includes(error.message)) message.error(error.message)
    return
  }
  if (!isLocal.value && !tested.value?.test_token) {
    message.error('请先测试主机连接与所选能力')
    return
  }
  submitting.value = true
  try {
    const request = { ...payload(), test_token: tested.value?.test_token }
    if (editing.value && props.host) await client.put(`/hosts/${props.host.id}`, request)
    else await client.post('/hosts', request)
    message.success(editing.value ? '主机已更新' : '主机已添加')
    emit('saved')
    emit('update:open', false)
  } catch (error) {
    message.error(apiErrorMessage(error))
  } finally {
    submitting.value = false
  }
}

function close() {
  if (!testing.value && !submitting.value) emit('update:open', false)
}
</script>

<template>
  <a-drawer
    :open="open"
    :title="isLocal ? '编辑本地主机' : editing ? '编辑主机' : '添加主机'"
    width="640"
    :mask-closable="!testing && !submitting"
    @close="close"
  >
    <a-form ref="formRef" :model="form" layout="vertical" :disabled="testing || submitting">
      <div class="form-grid" :class="{ local: isLocal }">
        <a-form-item :class="{ 'span-2': isLocal }" label="主机名称" name="name" :rules="[{ required: true, whitespace: true, message: '请输入主机名称' }]">
          <a-input v-model:value="form.name" placeholder="例如：上海构建节点 01" />
        </a-form-item>

        <a-form-item v-if="isLocal" class="span-2" label="本地的主机能力">
          <a-checkbox-group v-model:value="form.capability_kinds" class="capability-grid local-capabilities">
            <a-checkbox
              value="docker"
              class="capability-option"
              :class="{ unavailable: localCapabilityOption('docker')?.available !== true }"
              :disabled="localCapabilityDisabled('docker')"
              :title="localCapabilityHint('docker')"
            >
              <span class="capability-brand docker"><RuntimeBrandIcon kind="docker" /></span>
              <span><strong>本地 Docker</strong><small>{{ localCapabilityHint('docker') }}</small></span>
            </a-checkbox>
            <a-checkbox
              value="local_exec"
              class="capability-option"
              :class="{ unavailable: localCapabilityOption('local_exec')?.available !== true }"
              :disabled="localCapabilityDisabled('local_exec')"
              :title="localCapabilityHint('local_exec')"
            >
              <span class="capability-brand local_exec"><RuntimeBrandIcon kind="local_exec" /></span>
              <span><strong>直接终端执行</strong><small>{{ localCapabilityHint('local_exec') }}</small></span>
            </a-checkbox>
          </a-checkbox-group>
          <div class="field-hint">本地 Docker 由当前 EDO 运行方式自动检测；Linux/macOS 存在 sh 时可以启用直接终端执行。</div>
        </a-form-item>

        <template v-else>
          <a-form-item label="SSH 用户名" name="ssh_username" :rules="[{ required: true, whitespace: true, message: '请输入 SSH 用户名' }]">
            <a-input v-model:value="form.ssh_username" autocomplete="username" placeholder="例如：ubuntu" />
          </a-form-item>
          <a-form-item label="主机地址" name="address" :rules="[{ required: true, whitespace: true, message: '请输入 IP 地址或域名' }]">
            <a-input v-model:value="form.address" placeholder="IP 地址或域名" />
          </a-form-item>
          <a-form-item label="SSH 端口" name="ssh_port" :rules="[{ required: true, type: 'number', min: 1, max: 65535, message: '端口范围为 1–65535' }]">
            <a-input-number v-model:value="form.ssh_port" :min="1" :max="65535" />
          </a-form-item>

          <a-form-item class="span-2" label="主机能力" required>
            <a-checkbox-group v-model:value="form.capability_kinds" class="capability-grid">
              <a-checkbox value="ssh" class="capability-option">
                <span class="capability-brand ssh"><RuntimeBrandIcon kind="ssh" /></span>
                <span><strong>SSH 命令部署</strong><small>执行部署方案中的受审计脚本</small></span>
              </a-checkbox>
              <a-checkbox value="docker" class="capability-option">
                <span class="capability-brand docker"><RuntimeBrandIcon kind="docker" /></span>
                <span><strong>Docker</strong><small>容器运行、日志与终端</small></span>
              </a-checkbox>
              <a-checkbox value="kubernetes" class="capability-option">
                <span class="capability-brand kubernetes"><RuntimeBrandIcon kind="kubernetes" /></span>
                <span><strong>Kubernetes</strong><small>节点能力，可关联现有集群</small></span>
              </a-checkbox>
            </a-checkbox-group>
          </a-form-item>

          <a-form-item v-if="hasKubernetes" class="span-2" label="关联 Kubernetes 集群" required>
            <div style="display:flex;gap:8px">
              <a-select
                v-model:value="form.kubernetes_cluster_id"
                style="min-width:0;flex:1"
                placeholder="选择已有集群"
                :options="clusters.filter(item => item.is_active).map(item => ({ value: item.id, label: item.name }))"
              />
              <a-button style="width:34px;padding:0" aria-label="创建 Kubernetes 集群" title="创建 Kubernetes 集群" @click="emit('create-cluster')">＋</a-button>
            </div>
            <div class="field-hint">集群继续通过 kubeconfig/API 独立接入；主机只记录该集群的节点或管理关系。</div>
          </a-form-item>

          <a-form-item v-if="editingRemote" class="span-2">
            <div class="credential-toggle">
              <div><strong>使用已保存的 SSH 凭据</strong><small>测试和保存时由服务端安全复用，不会返回密码或私钥。</small></div>
              <a-switch v-model:checked="replaceCredential" checked-children="更换" un-checked-children="保留" />
            </div>
          </a-form-item>

          <template v-if="needsCredential">
            <a-form-item class="span-2" label="认证方式" name="ssh_auth_type">
              <a-radio-group v-model:value="form.ssh_auth_type" button-style="solid">
                <a-radio-button value="private_key">SSH 私钥</a-radio-button>
                <a-radio-button value="password">密码</a-radio-button>
              </a-radio-group>
            </a-form-item>
            <a-form-item
              v-if="form.ssh_auth_type === 'password'"
              class="span-2"
              label="SSH 密码"
              name="password"
              :rules="[{ required: true, message: '请输入 SSH 密码' }]"
            >
              <a-input-password v-model:value="form.password" autocomplete="new-password" placeholder="密码只会加密保存" />
            </a-form-item>
            <template v-else>
              <a-form-item class="span-2" label="SSH 私钥" name="private_key" :rules="[{ required: true, whitespace: true, message: '请粘贴 SSH 私钥' }]">
                <a-textarea v-model:value="form.private_key" :rows="8" spellcheck="false" placeholder="-----BEGIN OPENSSH PRIVATE KEY-----" />
              </a-form-item>
              <a-form-item class="span-2" label="私钥口令（可选）" name="passphrase">
                <a-input-password v-model:value="form.passphrase" autocomplete="new-password" placeholder="私钥未加密时留空" />
              </a-form-item>
            </template>
          </template>
          <a-form-item v-if="hasDocker" class="span-2 sudo-toggle" name="use_sudo">
            <a-checkbox v-model:checked="form.use_sudo">Docker 操作使用 sudo</a-checkbox>
            <div class="field-hint">只用于受限的 Docker 操作；密码认证复用 SSH 密码，私钥认证需配置免密 sudo。SSH 部署脚本不会自动提权。</div>
          </a-form-item>
        </template>
      </div>
    </a-form>

    <a-alert
      v-if="!isLocal && tested"
      class="test-result"
      type="success"
      show-icon
      message="主机连接与能力检查通过"
    >
      <template #description>
        <div class="test-details">
          <span v-if="tested.docker_version">Docker {{ tested.docker_version }}</span>
          <span v-if="tested.kubernetes_version">Kubernetes {{ tested.kubernetes_version }}</span>
          <code>{{ tested.fingerprint }}</code>
        </div>
      </template>
    </a-alert>
    <a-alert
      v-else-if="!isLocal"
      class="test-result"
      type="info"
      show-icon
      :message="editingRemote && !replaceCredential ? '将使用已保存的 SSH 凭据测试连接及所选能力。' : '保存前需要测试 SSH 连接及所有勾选的主机能力。'"
    />

    <template #footer>
      <div class="drawer-actions">
        <a-button @click="close">取消</a-button>
        <a-button v-if="!isLocal" :loading="testing" :disabled="submitting" @click="testConnection">测试连接</a-button>
        <a-button type="primary" :loading="submitting" :disabled="testing || (!isLocal && !tested)" @click="submit">保存主机</a-button>
      </div>
    </template>
  </a-drawer>
</template>

<style scoped>
.form-grid{display:grid;grid-template-columns:minmax(0,1fr) minmax(0,1fr);gap:0 16px}.span-2{grid-column:1/-1}.form-grid :deep(.ant-input-number){width:100%}.capability-grid{display:grid;width:100%;grid-template-columns:repeat(3,minmax(0,1fr));gap:10px}.capability-grid.local-capabilities{grid-template-columns:repeat(2,minmax(0,1fr))}.capability-option{display:flex;min-height:82px;align-items:center;padding:10px;border:1px solid var(--edo-border);border-radius:10px;background:var(--edo-surface-soft)}.capability-option:has(.ant-checkbox-checked){border-color:color-mix(in srgb,var(--edo-primary) 55%,var(--edo-border));background:var(--edo-primary-soft)}.capability-option :deep(.ant-checkbox){align-self:center}.capability-option>span:last-child{display:flex;align-items:center;gap:8px}.capability-option strong,.capability-option small{display:block}.capability-option small{margin-top:2px;color:var(--edo-muted);font-size:11px;line-height:1.4}.capability-brand{display:grid;width:34px;height:34px;flex:0 0 34px;place-items:center;border-radius:9px;background:var(--edo-surface)}.capability-brand :deep(svg){width:21px;height:21px}.capability-brand.ssh,.capability-brand.local_exec{color:var(--edo-muted)}.capability-brand.docker{color:#2496ed}.capability-brand.kubernetes{color:#326ce5}.credential-toggle{display:flex;align-items:center;justify-content:space-between;padding:12px 14px;border:1px solid var(--edo-border);border-radius:10px;background:var(--edo-surface-soft)}.credential-toggle>div strong,.credential-toggle>div small{display:block}.credential-toggle>div small{margin-top:2px;color:var(--edo-muted);font-size:12px}.field-hint{margin-top:6px;color:var(--edo-muted);font-size:12px}.test-result{margin-top:8px}.test-details{display:flex;flex-wrap:wrap;gap:8px 14px}.test-details code{width:100%;overflow-wrap:anywhere;color:var(--edo-muted)}.drawer-actions{display:flex;justify-content:flex-end;gap:8px}@media(max-width:640px){.form-grid,.capability-grid,.capability-grid.local-capabilities{grid-template-columns:1fr}.span-2{grid-column:auto}}
.capability-option.unavailable{opacity:.58}
</style>
