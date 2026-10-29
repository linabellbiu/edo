<script setup lang="ts">
import axios from 'axios'
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRoute, useRouter } from 'vue-router'
import { Languages, LockKeyhole, Moon, Sun, UserRound } from 'lucide-vue-next'

import client, { getLoginProviders, type LoginProvider } from '@/api/client'
import { useAuthStore } from '@/stores/auth'
import { usePreferencesStore } from '@/stores/preferences'

const route = useRoute()
const router = useRouter()
const auth = useAuthStore()
const preferences = usePreferencesStore()
const { t } = useI18n()
const username = ref('')
const password = ref('')
const provider = ref('local')
const providers = ref<LoginProvider[]>([])
const loading = ref(false)
const error = ref('')
const email = ref(''), code = ref(''), sendingCode = ref(false), codeMessage = ref('')
const resendAt = ref(0), now = ref(Date.now())
let timer: ReturnType<typeof setInterval> | undefined

const ldapProviders = computed(() => providers.value.filter((item) => item.type === 'ldap'))
const emailProviders = computed(() => providers.value.filter(item => item.type === 'email'))
const isEmail = computed(() => emailProviders.value.some(item => item.id === provider.value))
const formOptions = computed(() => [{ label: t('loginAuth.account'), value: 'local' }, ...[...emailProviders.value, ...ldapProviders.value].map(item => ({ label: item.display_name, value: item.id }))])
const oauthProviders = computed(() => providers.value.filter((item) => item.type !== 'ldap' && item.type !== 'email'))
const resendSeconds = computed(() => Math.max(0, Math.ceil((resendAt.value - now.value) / 1000)))
watch(provider, () => { error.value = ''; codeMessage.value = ''; code.value = '' })

async function sendCode() {
  if (!email.value.trim()) { error.value = t('loginAuth.emailAddressRequired'); return }
  sendingCode.value = true; error.value = ''; codeMessage.value = ''
  try {
    const { data } = await client.post<{ retry_after: number }>(`/auth/email/${encodeURIComponent(provider.value)}/code`, { email: email.value }, { timeout: 15_000 })
    now.value = Date.now(); resendAt.value = now.value + data.retry_after * 1000
    codeMessage.value = t('loginAuth.codeSent')
  } catch (sendError) {
    error.value = axios.isAxiosError(sendError) ? sendError.response?.data?.message || t('loginAuth.unavailable') : t('loginAuth.unavailable')
  } finally { sendingCode.value = false }
}

function safeRedirect(value: unknown) {
  return typeof value === 'string' && value.startsWith('/') && !value.startsWith('//') && !value.includes('\\') ? value : '/'
}

async function submit() {
  if (isEmail.value ? !email.value.trim() || !/^\d{6}$/.test(code.value) : !username.value.trim() || !password.value) {
    error.value = t(isEmail.value ? 'loginAuth.emailRequired' : 'loginAuth.credentialsRequired')
    return
  }
  loading.value = true
  error.value = ''
  try {
    if (isEmail.value) await auth.loginEmail(provider.value, email.value, code.value)
    else if (provider.value === 'local') await auth.login(username.value, password.value)
    else await auth.loginLDAP(provider.value, username.value, password.value)
    await router.replace(safeRedirect(route.query.redirect))
  } catch (submitError) {
    error.value = axios.isAxiosError(submitError)
      ? (submitError.response?.data as { message?: string } | undefined)?.message || t('loginAuth.unavailable')
      : t('loginAuth.unavailable')
  } finally {
    loading.value = false
  }
}

function externalLogin(item: LoginProvider) {
  const returnTo = safeRedirect(route.query.redirect)
  window.location.assign(`/api/v1/auth/oauth/${encodeURIComponent(item.id)}/start?return_to=${encodeURIComponent(returnTo)}`)
}

onMounted(async () => {
  timer = setInterval(() => { now.value = Date.now() }, 1000)
  try { providers.value = await getLoginProviders() } catch { providers.value = [] }
  if (route.query.reason === 'unavailable') error.value = t('loginAuth.statusUnavailable')
  if (route.query.external_error) {
    const errors: Record<string, string> = { expired: 'expired', not_bound: 'notBound', cancelled: 'cancelled', email_unverified: 'unverified' }
    error.value = t(`loginAuth.${errors[String(route.query.external_error)] || 'externalFailed'}`)
  }
})
onUnmounted(() => { if (timer) clearInterval(timer) })
</script>

<template>
  <main class="vben-login-page">
    <div class="login-tools">
      <a-dropdown><button type="button" :aria-label="t('common.language')"><Languages /></button><template #overlay><a-menu @click="({ key }: { key: string }) => preferences.setLocale(key as 'zh-CN' | 'en-US')"><a-menu-item key="zh-CN">简体中文</a-menu-item><a-menu-item key="en-US">English</a-menu-item></a-menu></template></a-dropdown>
      <button type="button" :aria-label="t('common.theme')" @click="preferences.toggleTheme()"><Sun v-if="preferences.theme === 'dark'" /><Moon v-else /></button>
    </div>
    <section class="login-visual">
      <div class="visual-brand"><span class="logo-mark"><span>Z</span></span><strong>EDO</strong></div>
      <div class="visual-copy"><p>EasyDevOps</p><h1>应用构建<br>自动部署</h1><span>连接仓库，配置流水线，自动完成测试、构建和发布。</span></div>
      <div class="visual-orbits"><i /><i /><i /></div>
    </section>
    <section class="login-panel">
      <form class="login-card" @submit.prevent="submit">
        <div class="login-card-head"><h2>{{ t('loginAuth.welcome') }}</h2><p>{{ t('loginAuth.welcomeHint') }}</p></div>
        <a-alert v-if="error" type="error" show-icon :message="error" />
        <a-select v-if="formOptions.length > 1" v-model:value="provider" size="large" :aria-label="t('loginAuth.title')" :options="formOptions" />
        <template v-if="isEmail">
          <a-alert v-if="codeMessage" type="success" show-icon :message="codeMessage" />
          <label><span>{{ t('loginAuth.emailAddress') }}</span><a-input v-model:value="email" type="email" size="large" autocomplete="email" :placeholder="t('loginAuth.emailPlaceholder')" :maxlength="254" /></label>
          <label><span>{{ t('loginAuth.code') }}</span><div class="email-code-row"><a-input v-model:value="code" inputmode="numeric" size="large" autocomplete="one-time-code" :maxlength="6" :placeholder="t('loginAuth.codePlaceholder')" /><a-button size="large" :loading="sendingCode" :disabled="resendSeconds > 0" @click="sendCode">{{ resendSeconds > 0 ? t('loginAuth.resend', { seconds: resendSeconds }) : t('loginAuth.sendCode') }}</a-button></div></label>
        </template>
        <template v-else>
          <label><span>{{ t('loginAuth.username') }}</span><a-input v-model:value="username" size="large" autocomplete="username" :placeholder="t('loginAuth.usernamePlaceholder')"><template #prefix><UserRound /></template></a-input></label>
          <label><span>{{ t('loginAuth.password') }}</span><a-input-password v-model:value="password" size="large" autocomplete="current-password" :placeholder="t('loginAuth.passwordPlaceholder')"><template #prefix><LockKeyhole /></template></a-input-password></label>
        </template>
        <a-button type="primary" html-type="submit" size="large" block :loading="loading">{{ t('loginAuth.login') }}</a-button>
        <template v-if="oauthProviders.length"><a-divider>{{ t('loginAuth.other') }}</a-divider><div class="oauth-list"><a-button v-for="item in oauthProviders" :key="item.id" @click="externalLogin(item)">{{ item.display_name }}</a-button></div></template>
      </form>
      <p class="login-copyright">EDO · 安全、清晰、可追踪</p>
    </section>
  </main>
</template>

<style scoped>
.vben-login-page { position: relative; display: grid; min-height: 100vh; grid-template-columns: minmax(380px,1.08fr) minmax(420px,.92fr); overflow: hidden; background: var(--edo-surface); }
.login-tools { position: absolute; top: 20px; right: 22px; z-index: 3; display: flex; gap: 5px; }
.login-tools button { display: grid; width: 34px; height: 34px; place-items: center; border: 0; border-radius: 6px; color: var(--edo-muted); background: transparent; cursor: pointer; }
.login-tools button:hover { color: var(--edo-primary); background: var(--edo-surface-soft); }
.login-tools svg { width: 18px; }
.login-visual { position: relative; display: flex; min-height: 100vh; flex-direction: column; justify-content: space-between; overflow: hidden; padding: 42px 7vw 64px; color: #fff; background: radial-gradient(circle at 18% 20%,rgb(116 107 255 / 95%),transparent 38%),radial-gradient(circle at 78% 78%,rgb(47 201 190 / 80%),transparent 36%),linear-gradient(145deg,#2d3c86,#5367df 56%,#335e8c); }
.login-visual::after { position: absolute; inset: 0; background-image: linear-gradient(rgb(255 255 255 / 5%) 1px,transparent 1px),linear-gradient(90deg,rgb(255 255 255 / 5%) 1px,transparent 1px); background-size: 42px 42px; content: ""; mask-image: linear-gradient(to bottom,black,transparent); }
.visual-brand, .visual-copy { position: relative; z-index: 1; }
.visual-brand { display: flex; align-items: center; gap: 13px; font-size: 21px; letter-spacing: .06em; }
.visual-copy { max-width: 590px; }
.visual-copy p { margin: 0 0 16px; color: rgb(255 255 255 / 72%); letter-spacing: .08em; }
.visual-copy h1 { margin: 0 0 22px; font-size: clamp(42px,4.2vw,68px); line-height: 1.12; letter-spacing: -.04em; }
.visual-copy > span { display: block; max-width: 500px; color: rgb(255 255 255 / 74%); font-size: 16px; line-height: 1.8; }
.visual-orbits { position: absolute; right: -150px; bottom: -170px; width: 520px; height: 520px; border: 1px solid rgb(255 255 255 / 15%); border-radius: 50%; }
.visual-orbits i { position: absolute; inset: 65px; border: 1px solid rgb(255 255 255 / 12%); border-radius: 50%; }
.visual-orbits i:nth-child(2) { inset: 135px; }.visual-orbits i:nth-child(3) { inset: 205px; background: rgb(255 255 255 / 9%); }
.login-panel { display: flex; min-height: 100vh; align-items: center; justify-content: center; flex-direction: column; padding: 80px 34px 34px; background: var(--edo-surface); }
.login-card { display: grid; width: min(390px,100%); gap: 20px; }
.login-card-head h2 { margin: 0; color: var(--edo-text); font-size: 28px; }.login-card-head p { margin: 6px 0 0; color: var(--edo-muted); }
label { display: grid; gap: 7px; color: var(--edo-text); font-weight: 500; }
label :deep(.ant-input-prefix) svg { width: 17px; color: var(--edo-muted); }
.oauth-list { display: flex; flex-wrap: wrap; gap: 8px; }.login-copyright { margin-top: 58px; color: var(--edo-muted); font-size: 12px; }
.email-code-row { display: flex; gap: 8px; min-width: 0; }.email-code-row > :first-child { min-width: 0; }
@media (max-width: 820px) { .vben-login-page { display: block; background: var(--edo-bg); }.login-visual { display: none; }.login-panel { background: var(--edo-bg); }.login-card { padding: 26px; border: 1px solid var(--edo-border); border-radius: 10px; background: var(--edo-surface); box-shadow: var(--edo-shadow); } }
</style>
