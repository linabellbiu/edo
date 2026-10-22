<script setup lang="ts">
import { computed, reactive, ref, watch } from 'vue'
import { message } from 'ant-design-vue'
import { useI18n } from 'vue-i18n'

import client from '@/api/client'
import { apiErrorMessage } from '@/api/resources'
import { useAuthStore } from '@/stores/auth'
import type { GitCredential } from '@/types/credential'

const props = withDefaults(defineProps<{
  open: boolean
  credential?: GitCredential
  provider?: string
  authType?: GitCredential['auth_type']
  lockContext?: boolean
}>(), { provider: 'github', authType: 'token', lockContext: false })
const emit = defineEmits<{
  'update:open': [open: boolean]
  saved: [credential: GitCredential]
}>()
const { t } = useI18n()
const auth = useAuthStore()
const submitting = ref(false)
const formRef = ref<{ validate: () => Promise<void>; clearValidate: () => void }>()
const form = reactive({ name: '', provider: 'github', auth_type: 'token' as GitCredential['auth_type'], username: '', secret: '' })
const providerNames: Record<string, string> = { github: 'GitHub', gitlab: 'GitLab', gitea: 'Gitea', gitee: 'Gitee', generic: 'Git' }
const canSave = computed(() => auth.canAny([props.credential ? 'credential.update' : 'credential.create']))

watch(() => props.open, open => {
  // 关闭时立即清除明文；重新打开只恢复元数据，不读取已保存的凭据内容。
  form.secret = ''
  if (open) {
    Object.assign(form, {
      name: props.credential?.name || '', provider: props.credential?.provider || props.provider,
      auth_type: props.credential?.auth_type || props.authType, username: props.credential?.username || '',
    })
    formRef.value?.clearValidate()
  }
}, { immediate: true })

function close() {
  if (submitting.value) return
  form.secret = ''
  emit('update:open', false)
}

async function submit() {
  if (submitting.value || !canSave.value || !formRef.value) return
  submitting.value = true
  try {
    try { await formRef.value.validate() } catch { return }
    const payload = { ...form, name: form.name.trim(), username: form.username.trim(), secret: form.secret || null }
    const response = props.credential
      ? await client.put<{ credential: GitCredential }>(`/git-credentials/${props.credential.id}`, payload)
      : await client.post<{ credential: GitCredential }>('/git-credentials', payload)
    form.secret = ''
    message.success(t('gitCredential.saved'))
    emit('saved', response.data.credential)
    emit('update:open', false)
  } catch (error) {
    message.error(apiErrorMessage(error))
  } finally {
    submitting.value = false
  }
}
</script>

<template>
  <a-drawer :open="open" :title="t(credential ? 'gitCredential.edit' : 'gitCredential.create')" width="520" :closable="!submitting" :mask-closable="!submitting" :keyboard="!submitting" @close="close">
    <a-alert type="info" show-icon :message="t('gitCredential.securityHint')" class="credential-alert" />
    <a-form ref="formRef" :model="form" layout="vertical" :disabled="submitting">
      <a-form-item :label="t('gitCredential.name')" name="name" :rules="[{ required: true, whitespace: true, message: t('gitCredential.nameRequired') }]">
        <a-input v-model:value="form.name" :maxlength="128" :placeholder="t('gitCredential.namePlaceholder')" />
      </a-form-item>
      <div class="credential-form-row">
        <a-form-item :label="t('gitCredential.provider')" name="provider" :rules="[{ required: true, message: t('gitCredential.providerRequired') }]">
          <a-select v-model:value="form.provider" :disabled="lockContext || submitting" :options="Object.entries(providerNames).map(([value, label]) => ({ value, label }))" />
        </a-form-item>
        <a-form-item :label="t('gitCredential.type')" name="auth_type" :rules="[{ required: true, message: t('gitCredential.typeRequired') }]">
          <a-select v-model:value="form.auth_type" :disabled="lockContext || submitting" :options="[{ value: 'token', label: t('gitCredential.token') }, { value: 'ssh_key', label: t('gitCredential.sshKey') }]" />
        </a-form-item>
      </div>
      <a-form-item :label="t('gitCredential.username')"><a-input v-model:value="form.username" :maxlength="255" :placeholder="t('gitCredential.usernamePlaceholder')" /></a-form-item>
      <a-form-item name="secret" :label="t(form.auth_type === 'token' ? 'gitCredential.token' : 'gitCredential.sshKey')" :rules="credential ? [] : [{ required: true, whitespace: true, message: t(form.auth_type === 'token' ? 'gitCredential.tokenRequired' : 'gitCredential.sshKeyRequired') }]">
        <a-textarea v-model:value="form.secret" :rows="form.auth_type === 'ssh_key' ? 8 : 3" :placeholder="t(credential ? 'gitCredential.keepSecret' : 'gitCredential.secretPlaceholder')" autocomplete="off" />
      </a-form-item>
    </a-form>
    <template #footer>
      <div class="credential-actions">
        <a-button :disabled="submitting" @click="close">{{ t('gitCredential.cancel') }}</a-button>
        <a-button v-if="canSave" type="primary" :loading="submitting" @click="submit">{{ t('gitCredential.save') }}</a-button>
      </div>
    </template>
  </a-drawer>
</template>

<style scoped>
.credential-alert{margin-bottom:18px}.credential-form-row{display:grid;grid-template-columns:1fr 1fr;gap:12px}.credential-actions{display:flex;justify-content:flex-end;gap:8px}
</style>
