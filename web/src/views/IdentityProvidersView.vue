<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { message } from 'ant-design-vue'
import { Plus, RefreshCw } from 'lucide-vue-next'

import client, { type LoginProvider } from '@/api/client'
import { apiErrorMessage, type ResourceRecord } from '@/api/resources'
import ResourceTable from '@/components/ResourceTable.vue'
import { useAuthStore } from '@/stores/auth'

type ProviderType = LoginProvider['type']
interface EmailSettings { host: string; port: number; username: string; from: string; tls_mode: string; allowed_domains: string[]; has_password?: boolean }
interface Provider extends ResourceRecord {
  id: string; type: ProviderType; name: string; display_name: string; is_active: boolean; auto_create: boolean; default_role_id: string
  client_id: string; authorization_url: string; token_url: string; user_info_url: string; redirect_url: string; scopes: string
  subject_field: string; username_field: string; nickname_field: string; email_field: string; email_verified_field: string
  ldap_url: string; ldap_base_dn: string; ldap_bind_dn: string; ldap_user_filter: string; ldap_username_attribute: string
  ldap_nickname_attribute: string; ldap_email_attribute: string; ldap_start_tls: boolean; allow_insecure: boolean; email?: EmailSettings
}
interface Role { id: string; display_name: string }
const { t } = useI18n()
const auth = useAuthStore()
const items = ref<Provider[]>([]), roles = ref<Role[]>([]), loading = ref(false), saving = ref(false), open = ref(false), editingID = ref('')
const types: ProviderType[] = ['feishu', 'dingtalk', 'email', 'ldap', 'generic_oauth', 'google', 'github', 'gitlab']
const typeOptions = computed(() => types.map(value => ({ value, label: t(`loginAuth.${value}`) })))
function initial(type: ProviderType = 'feishu') {
  return {
    type, name: type === 'generic_oauth' ? 'company_oauth' : type, display_name: t(`loginAuth.${type}`), is_active: true, auto_create: false, default_role_id: '',
    client_id: '', client_secret: '', authorization_url: '', token_url: '', user_info_url: '', redirect_url: '', scopes: '',
    subject_field: '', username_field: '', nickname_field: '', email_field: '', email_verified_field: '',
    ldap_url: 'ldaps://ldap.example.com:636', ldap_base_dn: '', ldap_bind_dn: '', ldap_bind_password: '', ldap_user_filter: '(uid={username})',
    ldap_username_attribute: 'uid', ldap_nickname_attribute: 'displayName', ldap_email_attribute: 'mail', ldap_start_tls: false, allow_insecure: false,
    email_host: '', email_port: 465, email_username: '', email_password: '', email_from: '', email_tls: 'tls', email_domains: '',
  }
}
const form = reactive(initial())
const isLDAP = computed(() => form.type === 'ldap')
const isEmail = computed(() => form.type === 'email')
const callback = computed(() => `${location.origin}/api/v1/auth/oauth/${encodeURIComponent(form.name)}/callback`)
const columns = computed(() => [
  { key: 'display_name', label: t('loginAuth.name') }, { key: 'type', label: t('loginAuth.type') },
  { key: 'name', label: t('loginAuth.identifier') }, { key: 'auto_create', label: t('loginAuth.autoCreate') }, { key: 'is_active', label: t('loginAuth.status') },
])
async function refresh() {
  loading.value = true
  try {
    const [providersResult, rolesResult] = await Promise.all([
      client.get<{ providers: Provider[] }>('/identity-providers'),
      client.get<{ roles: Role[] }>('/roles').catch(() => ({ data: { roles: [] as Role[] } })),
    ])
    items.value = providersResult.data.providers
    roles.value = rolesResult.data.roles
  } catch (error) { message.error(apiErrorMessage(error)) }
  finally { loading.value = false }
}
function create() { editingID.value = ''; Object.assign(form, initial()); open.value = true }
function edit(item: Provider) {
  editingID.value = item.id
  Object.assign(form, initial(item.type), item, { client_secret: '', ldap_bind_password: '' })
  if (item.email) Object.assign(form, {
    email_host: item.email.host, email_port: item.email.port, email_username: item.email.username, email_from: item.email.from,
    email_tls: item.email.tls_mode, email_domains: item.email.allowed_domains.join(', '), email_password: '',
  })
  open.value = true
}
async function save() {
  saving.value = true
  try {
    const { email_host, email_port, email_username, email_password, email_from, email_tls, email_domains, ...base } = form
    const payload = {
      ...base, default_role_id: base.default_role_id || '', client_secret: form.client_secret || undefined, ldap_bind_password: form.ldap_bind_password || undefined,
      redirect_url: isLDAP.value || isEmail.value ? '' : form.redirect_url || callback.value,
      email: isEmail.value ? {
        host: email_host, port: email_port, username: email_username, password: email_password || undefined,
        from: email_from, tls_mode: email_tls, allowed_domains: email_domains.split(/[,，\s]+/).filter(Boolean),
      } : undefined,
    }
    if (editingID.value) await client.put(`/identity-providers/${editingID.value}`, payload)
    else await client.post('/identity-providers', payload)
    message.success(t('loginAuth.saved')); open.value = false; await refresh()
  } catch (error) { message.error(apiErrorMessage(error)) }
  finally { saving.value = false }
}
async function toggle(item: Provider) {
  try { await client.patch(`/identity-providers/${item.id}/status`, { is_active: !item.is_active }); await refresh() }
  catch (error) { message.error(apiErrorMessage(error)) }
}
onMounted(refresh)
</script>

<template>
  <div class="identity-settings">
    <div class="identity-heading">
      <div><h3>{{ t('loginAuth.title') }}</h3><p>{{ t('loginAuth.description') }}</p></div>
      <a-space><a-button :loading="loading" @click="refresh"><RefreshCw :size="15" />{{ t('loginAuth.refresh') }}</a-button><a-button v-if="auth.canAny(['identity.create'])" type="primary" @click="create"><Plus :size="15" />{{ t('loginAuth.add') }}</a-button></a-space>
    </div>
    <div class="vben-card">
      <ResourceTable :rows="items" :columns="columns" :loading="loading">
        <template #cell-type="{ value }"><a-tag color="blue">{{ types.includes(value as ProviderType) ? t(`loginAuth.${value}`) : value }}</a-tag></template>
        <template v-if="auth.canAny(['identity.update'])" #actions="{ row }"><a-button type="link" @click="edit(row as Provider)">{{ t('loginAuth.edit') }}</a-button><a-button type="link" @click="toggle(row as Provider)">{{ t(row.is_active ? 'loginAuth.disable' : 'loginAuth.enable') }}</a-button></template>
      </ResourceTable>
    </div>
    <a-drawer v-model:open="open" :title="t(editingID ? 'loginAuth.edit' : 'loginAuth.add')" width="680" :destroy-on-close="true">
      <a-form :model="form" layout="vertical" @finish="save">
        <a-form-item v-if="!editingID" :label="t('loginAuth.type')"><a-select :value="form.type" :options="typeOptions" @change="(value: ProviderType) => Object.assign(form, initial(value))" /></a-form-item>
        <div class="form-grid">
          <a-form-item :label="t('loginAuth.displayName')" required><a-input v-model:value="form.display_name" :maxlength="64" /></a-form-item>
          <a-form-item :label="t('loginAuth.identifier')" required><a-input v-model:value="form.name" :disabled="Boolean(editingID)" :maxlength="64" /></a-form-item>
          <a-form-item :label="t('loginAuth.role')"><a-select v-model:value="form.default_role_id" allow-clear :options="roles.map(role => ({ value: role.id, label: role.display_name }))" /></a-form-item>
          <a-form-item :label="t('loginAuth.policy')"><a-space wrap><a-checkbox v-model:checked="form.is_active">{{ t('loginAuth.active') }}</a-checkbox><a-checkbox v-model:checked="form.auto_create">{{ t('loginAuth.autoCreate') }}</a-checkbox></a-space></a-form-item>
        </div>
        <a-alert type="info" show-icon :message="t('loginAuth.policyHint')" class="provider-hint" />
        <template v-if="isEmail">
          <a-divider>{{ t('loginAuth.smtp') }}</a-divider>
          <div class="form-grid">
            <a-form-item :label="t('loginAuth.host')" required><a-input v-model:value="form.email_host" placeholder="smtp.example.com" /></a-form-item>
            <a-form-item :label="t('loginAuth.port')" required><a-input-number v-model:value="form.email_port" :min="1" :max="65535" /></a-form-item>
            <a-form-item :label="t('loginAuth.smtpUsername')"><a-input v-model:value="form.email_username" autocomplete="off" /></a-form-item>
            <a-form-item :label="t('loginAuth.smtpPassword')"><a-input-password v-model:value="form.email_password" autocomplete="new-password" :placeholder="editingID ? t('loginAuth.keepSecret') : ''" /></a-form-item>
            <a-form-item :label="t('loginAuth.from')" required><a-input v-model:value="form.email_from" type="email" placeholder="noreply@example.com" /></a-form-item>
            <a-form-item :label="t('loginAuth.encryption')"><a-select v-model:value="form.email_tls" :options="[{ value: 'tls', label: 'TLS' }, { value: 'starttls', label: 'STARTTLS' }]" @change="() => form.email_port = form.email_tls === 'tls' ? 465 : 587" /></a-form-item>
            <a-form-item class="span-2" :label="t('loginAuth.domains')" :help="t('loginAuth.domainsHint')"><a-input v-model:value="form.email_domains" placeholder="example.com, example.org" /></a-form-item>
          </div>
          <a-alert type="info" show-icon :message="t('loginAuth.emailHint')" />
        </template>
        <template v-else-if="isLDAP">
          <a-divider>LDAP</a-divider>
          <div class="form-grid">
            <a-form-item :label="t('loginAuth.ldapURL')" required><a-input v-model:value="form.ldap_url" /></a-form-item>
            <a-form-item :label="t('loginAuth.baseDN')" required><a-input v-model:value="form.ldap_base_dn" /></a-form-item>
            <a-form-item :label="t('loginAuth.bindDN')"><a-input v-model:value="form.ldap_bind_dn" /></a-form-item>
            <a-form-item :label="t('loginAuth.bindPassword')"><a-input-password v-model:value="form.ldap_bind_password" :placeholder="editingID ? t('loginAuth.keepSecret') : ''" /></a-form-item>
            <a-form-item :label="t('loginAuth.filter')"><a-input v-model:value="form.ldap_user_filter" /></a-form-item>
            <a-form-item :label="t('loginAuth.usernameField')"><a-input v-model:value="form.ldap_username_attribute" /></a-form-item>
          </div>
          <a-checkbox v-model:checked="form.ldap_start_tls">{{ t('loginAuth.startTLS') }}</a-checkbox><a-checkbox v-model:checked="form.allow_insecure">{{ t('loginAuth.insecureLDAP') }}</a-checkbox>
        </template>
        <template v-else>
          <a-divider>OAuth</a-divider>
          <div class="form-grid">
            <a-form-item :label="t('loginAuth.clientID')" required><a-input v-model:value="form.client_id" /></a-form-item>
            <a-form-item :label="t('loginAuth.clientSecret')" :required="!editingID"><a-input-password v-model:value="form.client_secret" autocomplete="new-password" :placeholder="editingID ? t('loginAuth.keepSecret') : ''" /></a-form-item>
            <a-form-item class="span-2" :label="t('loginAuth.callback')" required :help="t('loginAuth.callbackHint')"><a-input :value="form.redirect_url || callback" @update:value="(value: string) => form.redirect_url = value" /></a-form-item>
          </div>
          <a-alert type="info" show-icon :message="t('loginAuth.presetHint')" class="provider-hint" />
          <a-collapse ghost><a-collapse-panel key="advanced" :header="t('loginAuth.advanced')">
            <div class="form-grid">
              <a-form-item v-for="field in ([['authorization_url', 'authorizeURL'], ['token_url', 'tokenURL'], ['user_info_url', 'userInfoURL'], ['scopes', 'scopes'], ['subject_field', 'subject'], ['username_field', 'usernameField'], ['nickname_field', 'nicknameField'], ['email_field', 'emailField'], ['email_verified_field', 'emailVerifiedField']] as const)" :key="field[0]" :class="{ 'span-2': field[0].endsWith('_url') }" :label="t(`loginAuth.${field[1]}`)"><a-input v-model:value="form[field[0]]" :placeholder="t('loginAuth.defaultValue')" /></a-form-item>
            </div>
            <a-checkbox v-model:checked="form.allow_insecure">{{ t('loginAuth.allowHTTP') }}</a-checkbox>
          </a-collapse-panel></a-collapse>
        </template>
        <a-button type="primary" html-type="submit" block :loading="saving" class="save-provider">{{ t('loginAuth.save') }}</a-button>
      </a-form>
    </a-drawer>
  </div>
</template>

<style scoped>
.identity-heading { display:flex; align-items:center; justify-content:space-between; gap:16px; margin-bottom:14px; flex-wrap:wrap; }
.identity-heading h3, .identity-heading p { margin:0; }
.identity-heading p { margin-top:4px; color:var(--edo-muted); }
.form-grid { display:grid; grid-template-columns:1fr 1fr; gap:0 14px; }
.span-2 { grid-column:1/-1; }
.provider-hint { margin-bottom:16px; }
.save-provider { margin-top:22px; }
@media(max-width:650px) { .form-grid { grid-template-columns:1fr; } .span-2 { grid-column:auto; } }
</style>
