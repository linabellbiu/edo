<script setup lang="ts">
import axios from 'axios'
import { computed, onMounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { Languages, LockKeyhole, Moon, Sun, UserRound } from 'lucide-vue-next'

import { getLoginProviders, type LoginProvider } from '@/api/client'
import { useAuthStore } from '@/stores/auth'
import { usePreferencesStore } from '@/stores/preferences'

const route = useRoute()
const router = useRouter()
const auth = useAuthStore()
const preferences = usePreferencesStore()
const username = ref('')
const password = ref('')
const provider = ref('local')
const providers = ref<LoginProvider[]>([])
const loading = ref(false)
const error = ref('')

const ldapProviders = computed(() => providers.value.filter((item) => item.type === 'ldap'))
const oauthProviders = computed(() => providers.value.filter((item) => item.type !== 'ldap'))

function safeRedirect(value: unknown) {
  return typeof value === 'string' && value.startsWith('/') && !value.startsWith('//') && !value.includes('\\') ? value : '/'
}

async function submit() {
  if (!username.value.trim() || !password.value) {
    error.value = '请输入用户名和密码。'
    return
  }
  loading.value = true
  error.value = ''
  try {
    if (provider.value === 'local') await auth.login(username.value, password.value)
    else await auth.loginLDAP(provider.value, username.value, password.value)
    await router.replace(safeRedirect(route.query.redirect))
  } catch (submitError) {
    error.value = axios.isAxiosError(submitError)
      ? (submitError.response?.data as { message?: string } | undefined)?.message || '登录服务暂时不可用，请稍后重试。'
      : '登录服务暂时不可用，请稍后重试。'
  } finally {
    loading.value = false
  }
}

function externalLogin(item: LoginProvider) {
  const returnTo = safeRedirect(route.query.redirect)
  window.location.assign(`/api/v1/auth/oauth/${encodeURIComponent(item.id)}/start?return_to=${encodeURIComponent(returnTo)}`)
}

onMounted(async () => {
  try { providers.value = await getLoginProviders() } catch { providers.value = [] }
  if (route.query.reason === 'unavailable') error.value = '暂时无法读取登录状态，请确认 EDO 服务已启动。'
})
</script>

<template>
  <main class="vben-login-page">
    <div class="login-tools">
      <a-dropdown><button type="button"><Languages /></button><template #overlay><a-menu @click="({ key }: { key: string }) => preferences.setLocale(key as 'zh-CN' | 'en-US')"><a-menu-item key="zh-CN">简体中文</a-menu-item><a-menu-item key="en-US">English</a-menu-item></a-menu></template></a-dropdown>
      <button type="button" @click="preferences.toggleTheme()"><Sun v-if="preferences.theme === 'dark'" /><Moon v-else /></button>
    </div>
    <section class="login-visual">
      <div class="visual-brand"><span class="logo-mark"><span>Z</span></span><strong>EDO</strong></div>
      <div class="visual-copy"><p>持续交付与运行管理平台</p><h1>让每一次变更<br>都有清晰路径。</h1><span>从代码检查、镜像构建到环境发布，把复杂流程收进一个可靠的工作台。</span></div>
      <div class="visual-orbits"><i /><i /><i /></div>
    </section>
    <section class="login-panel">
      <form class="login-card" @submit.prevent="submit">
        <div class="login-card-head"><h2>欢迎回来</h2><p>登录 EDO 继续工作</p></div>
        <a-alert v-if="error" type="error" show-icon :message="error" />
        <a-segmented v-if="ldapProviders.length" v-model:value="provider" block :options="[{ label: 'EDO 账号', value: 'local' }, ...ldapProviders.map((item) => ({ label: item.display_name, value: item.id }))]" />
        <label><span>用户名</span><a-input v-model:value="username" size="large" autocomplete="username" placeholder="请输入用户名"><template #prefix><UserRound /></template></a-input></label>
        <label><span>密码</span><a-input-password v-model:value="password" size="large" autocomplete="current-password" placeholder="请输入密码"><template #prefix><LockKeyhole /></template></a-input-password></label>
        <a-button type="primary" html-type="submit" size="large" block :loading="loading">登录</a-button>
        <template v-if="oauthProviders.length"><a-divider>其他登录方式</a-divider><div class="oauth-list"><a-button v-for="item in oauthProviders" :key="item.id" @click="externalLogin(item)">{{ item.display_name }}</a-button></div></template>
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
@media (max-width: 820px) { .vben-login-page { display: block; background: var(--edo-bg); }.login-visual { display: none; }.login-panel { background: var(--edo-bg); }.login-card { padding: 26px; border: 1px solid var(--edo-border); border-radius: 10px; background: var(--edo-surface); box-shadow: var(--edo-shadow); } }
</style>
