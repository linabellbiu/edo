<script setup lang="ts">
import { reactive, ref, watch } from 'vue'
import { message } from 'ant-design-vue'
import { useI18n } from 'vue-i18n'

import client from '@/api/client'
import { apiErrorMessage } from '@/api/resources'

type AuthType = 'password' | 'private_key'

interface SSHTestResult {
  fingerprint: string
  docker_version: string
}

const props = withDefaults(defineProps<{ open?: boolean }>(), { open: false })
const emit = defineEmits<{ created: []; 'update:open': [value: boolean] }>()
const { t } = useI18n()

const formRef = ref<{ validate: () => Promise<void> }>()
const form = reactive({
  name: '',
  host: '',
  port: 22,
  username: '',
  authType: 'private_key' as AuthType,
  password: '',
  privateKey: '',
  passphrase: '',
  useSudo: false,
})
const testing = ref(false)
const submitting = ref(false)
const tested = ref(false)
const fingerprint = ref('')
const dockerVersion = ref('')

watch(form, () => {
  tested.value = false
  fingerprint.value = ''
  dockerVersion.value = ''
}, { deep: true })

watch(() => props.open, (open) => {
  if (open) reset()
})

function reset() {
  Object.assign(form, {
    name: '',
    host: '',
    port: 22,
    username: '',
    authType: 'private_key',
    password: '',
    privateKey: '',
    passphrase: '',
    useSudo: false,
  })
  tested.value = false
  fingerprint.value = ''
  dockerVersion.value = ''
}

function endpointHost() {
  const host = form.host.trim()
  const formattedHost = host.includes(':') && !host.startsWith('[') ? `[${host}]` : host
  return `ssh://${encodeURIComponent(form.username.trim())}@${formattedHost}:${form.port}`
}

function requestPayload() {
  const ssh = form.authType === 'password'
    ? { password: form.password }
    : {
        private_key: form.privateKey,
        ...(form.passphrase ? { passphrase: form.passphrase } : {}),
      }
  Object.assign(ssh, {
    ...(form.useSudo ? { use_sudo: true } : {}),
  })
  return {
    name: form.name.trim(),
    host: endpointHost(),
    ssh,
  }
}

async function validate() {
  await formRef.value?.validate()
}

async function testConnection() {
  try {
    await validate()
  } catch {
    return
  }
  testing.value = true
  try {
    const response = await client.post<SSHTestResult>('/docker/endpoints/test', requestPayload(), {
      timeout: 35_000,
    })
    fingerprint.value = response.data.fingerprint
    dockerVersion.value = response.data.docker_version
    tested.value = true
    message.success(t('dockerConnection.testSuccess'))
  } catch (error) {
    message.error(apiErrorMessage(error))
  } finally {
    testing.value = false
  }
}

async function submit() {
  try {
    await validate()
  } catch {
    return
  }
  if (!tested.value || !fingerprint.value) {
    message.error(t('dockerConnection.testFirst'))
    return
  }
  submitting.value = true
  try {
    await client.post('/docker/endpoints', {
      ...requestPayload(),
      ssh_host_key_fingerprint: fingerprint.value,
    })
    message.success(t('dockerConnection.createSuccess'))
    emit('created')
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
    :title="t('dockerConnection.title')"
    width="600"
    :mask-closable="!testing && !submitting"
    @close="close"
  >
    <a-form ref="formRef" :model="form" layout="vertical" :disabled="testing || submitting">
      <div class="form-grid">
        <a-form-item
          :label="t('dockerConnection.name')"
          name="name"
          :rules="[{ required: true, whitespace: true, message: t('dockerConnection.nameRequired') }]"
        >
          <a-input v-model:value="form.name" :placeholder="t('dockerConnection.namePlaceholder')" />
        </a-form-item>
        <a-form-item
          :label="t('dockerConnection.username')"
          name="username"
          :rules="[{ required: true, whitespace: true, message: t('dockerConnection.usernameRequired') }]"
        >
          <a-input v-model:value="form.username" autocomplete="username" :placeholder="t('dockerConnection.usernamePlaceholder')" />
        </a-form-item>
        <a-form-item
          :label="t('dockerConnection.host')"
          name="host"
          :rules="[{ required: true, whitespace: true, message: t('dockerConnection.hostRequired') }]"
        >
          <a-input v-model:value="form.host" :placeholder="t('dockerConnection.hostPlaceholder')" />
        </a-form-item>
        <a-form-item
          :label="t('dockerConnection.port')"
          name="port"
          :rules="[{ required: true, type: 'number', min: 1, max: 65535, message: t('dockerConnection.portInvalid') }]"
        >
          <a-input-number v-model:value="form.port" :min="1" :max="65535" />
        </a-form-item>
        <a-form-item class="span-2" :label="t('dockerConnection.authType')" name="authType">
          <a-radio-group v-model:value="form.authType" button-style="solid">
            <a-radio-button value="private_key">{{ t('dockerConnection.privateKey') }}</a-radio-button>
            <a-radio-button value="password">{{ t('dockerConnection.password') }}</a-radio-button>
          </a-radio-group>
        </a-form-item>
        <a-form-item
          v-if="form.authType === 'password'"
          class="span-2"
          :label="t('dockerConnection.passwordLabel')"
          name="password"
          :rules="[{ required: true, message: t('dockerConnection.passwordRequired') }]"
        >
          <a-input-password
            v-model:value="form.password"
            autocomplete="new-password"
            :placeholder="t('dockerConnection.passwordPlaceholder')"
          />
        </a-form-item>
        <template v-else>
          <a-form-item
            class="span-2"
            :label="t('dockerConnection.privateKeyLabel')"
            name="privateKey"
            :rules="[{ required: true, whitespace: true, message: t('dockerConnection.privateKeyRequired') }]"
          >
            <a-textarea
              v-model:value="form.privateKey"
              :rows="8"
              spellcheck="false"
              placeholder="-----BEGIN OPENSSH PRIVATE KEY-----"
            />
          </a-form-item>
          <a-form-item class="span-2" :label="t('dockerConnection.passphrase')" name="passphrase">
            <a-input-password
              v-model:value="form.passphrase"
              autocomplete="new-password"
              :placeholder="t('dockerConnection.passphrasePlaceholder')"
            />
          </a-form-item>
        </template>
        <a-form-item class="span-2 sudo-toggle" name="useSudo">
          <a-checkbox v-model:checked="form.useSudo">
            {{ t('dockerConnection.useSudo') }}
          </a-checkbox>
          <div class="field-hint">
            {{ t(form.authType === 'password' ? 'dockerConnection.sudoPasswordHint' : 'dockerConnection.sudoKeyHint') }}
          </div>
        </a-form-item>
      </div>
    </a-form>

    <a-alert
      v-if="tested"
      type="success"
      show-icon
      class="test-result"
      :message="t('dockerConnection.ready', { version: dockerVersion })"
      :description="t('dockerConnection.fingerprint', { fingerprint })"
    />
    <a-alert
      v-else
      type="info"
      show-icon
      class="test-result"
      :message="t('dockerConnection.testHint')"
    />

    <template #footer>
      <div class="drawer-actions">
        <a-button @click="close">{{ t('dockerConnection.cancel') }}</a-button>
        <a-button :loading="testing" :disabled="submitting" @click="testConnection">
          {{ t('dockerConnection.test') }}
        </a-button>
        <a-button type="primary" :loading="submitting" :disabled="testing || !tested" @click="submit">
          {{ t('dockerConnection.create') }}
        </a-button>
      </div>
    </template>
  </a-drawer>
</template>

<style scoped>
.form-grid {
  display: grid;
  grid-template-columns: minmax(0, 1fr) minmax(0, 1fr);
  gap: 0 16px;
}

.span-2 {
  grid-column: 1 / -1;
}

.form-grid :deep(.ant-input-number) {
  width: 100%;
}

.form-grid textarea {
  font-family: "SFMono-Regular", Consolas, monospace;
  font-size: 12px;
  line-height: 1.55;
}

.sudo-toggle {
  margin-top: 2px;
}

.field-hint {
  margin-top: 5px;
  color: var(--ant-color-text-tertiary, rgba(0, 0, 0, 0.45));
  font-size: 12px;
  line-height: 1.55;
}

.test-result {
  margin-top: 4px;
}

.drawer-actions {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
}

@media (max-width: 560px) {
  .form-grid {
    grid-template-columns: 1fr;
  }

  .span-2 {
    grid-column: auto;
  }
}
</style>
