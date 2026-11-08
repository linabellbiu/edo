<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { Github, RefreshCw } from 'lucide-vue-next'
import client from '@/api/client'
import { apiErrorMessage } from '@/api/resources'
import { useAuthStore } from '@/stores/auth'
import { useInterfaceSettingsStore, type GitHubLogoSettings } from '@/stores/interfaceSettings'
const auth = useAuthStore(), settings = useInterfaceSettingsStore()
const enabled = ref(true), version = ref(0), loaded = ref(false), loading = ref(false), saving = ref(false), error = ref(''), status = ref('')
const canRead = computed(() => auth.authorizationState === 'ready' && auth.canAny(['config.read']))
const canUpdate = computed(() => canRead.value && auth.canAny(['config.update']))
const identity = () => JSON.stringify([auth.user?.id, auth.user?.permissions, auth.authorizationState, canRead.value, canUpdate.value])
let revision = 0, controller: AbortController | undefined, disposed = false
async function load() {
  const request = ++revision, scope = identity()
  loaded.value = false; loading.value = true; error.value = ''; status.value = ''
  if (!canRead.value) { loading.value = false; return }
  const value = await settings.load()
  if (disposed || scope !== identity() || request !== revision) return
  loading.value = false
  if (!value) { error.value = settings.error || '无法读取界面设置，请重试'; return }
  enabled.value = value.enabled; version.value = value.version; loaded.value = true
}
async function save() {
  if (!loaded.value || !canUpdate.value || saving.value) return
  controller?.abort(); controller = new AbortController()
  const scope = identity(), request = ++revision, signal = controller.signal
  saving.value = true; error.value = ''; status.value = ''
  try {
    const { data } = await client.put<GitHubLogoSettings>('/settings/github-logo', { enabled: enabled.value, expected_version: version.value }, { signal })
    if (disposed || signal.aborted || scope !== identity() || request !== revision) return
    settings.apply(data); enabled.value = data.enabled; version.value = data.version
    status.value = '界面设置已保存，当前页面立即生效'
  } catch (cause) { if (!disposed && !signal.aborted && scope === identity() && request === revision) error.value = apiErrorMessage(cause) }
  finally { if (request === revision) saving.value = false }
}
watch(identity, () => { revision++; controller?.abort(); saving.value = false; void load() }, { immediate: true })
onBeforeUnmount(() => { disposed = true; revision++; controller?.abort() })
</script>
<template>
  <article class="interface-settings vben-card" aria-label="界面设置">
    <header><Github :size="22" /><div><h3>GitHub 标识</h3><p>控制所有用户页面顶部的 GitHub 标识和仓库链接，其他页面刷新或重新进入后生效。</p></div><a-button :disabled="loading||saving" :loading="loading" aria-label="刷新界面设置" @click="load"><RefreshCw :size="15" />刷新</a-button></header>
    <a-alert v-if="error" type="error" show-icon :message="error" />
    <a-alert v-if="status" type="success" show-icon :message="status" />
    <a-form layout="vertical"><a-form-item label="显示 GitHub 标识"><a-switch v-model:checked="enabled" :disabled="!loaded||!canUpdate||loading||saving" aria-label="显示 GitHub 标识" /></a-form-item><a-button type="primary" :loading="saving" :disabled="!loaded||!canUpdate||loading" @click="save">保存设置</a-button></a-form>
  </article>
</template>
<style scoped>
.interface-settings{display:grid;gap:16px;padding:20px;margin-top:16px}.interface-settings header{display:flex;align-items:flex-start;gap:12px}.interface-settings header>svg{flex-shrink:0;color:var(--edo-primary)}.interface-settings header>div{flex:1;min-width:0}.interface-settings h3{margin:0 0 6px;font-size:15px}.interface-settings p{margin:0;color:var(--edo-muted);font-size:13px;line-height:1.7}.interface-settings .ant-form-item{margin-bottom:16px}
</style>
