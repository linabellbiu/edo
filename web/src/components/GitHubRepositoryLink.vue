<script setup lang="ts">
import { onBeforeUnmount, onMounted, watch } from 'vue'
import { Github } from 'lucide-vue-next'
import { useI18n } from 'vue-i18n'
import { useAuthStore } from '@/stores/auth'
import { useInterfaceSettingsStore } from '@/stores/interfaceSettings'
const auth = useAuthStore(), settings = useInterfaceSettingsStore(), { t } = useI18n()
const refresh = () => { if (auth.user && auth.authorizationState === 'ready') void settings.load() }
const visible = () => { if (!document.hidden) refresh() }
watch(() => auth.user?.id, () => { settings.reset(); refresh() }, { immediate: true })
watch(() => auth.authorizationState, state => { if (state === 'ready' && !settings.githubLogo) refresh() })
onMounted(() => { window.addEventListener('focus', refresh); document.addEventListener('visibilitychange', visible) })
onBeforeUnmount(() => { window.removeEventListener('focus', refresh); document.removeEventListener('visibilitychange', visible); settings.reset() })
</script>
<template>
  <a-tooltip v-if="settings.showGitHubLogo" title="GitHub"><a class="icon-button github-link" href="https://github.com/linabellbiu/edo" target="_blank" rel="noopener noreferrer" :aria-label="t('common.githubRepository')"><Github :size="17" /></a></a-tooltip>
</template>
<style scoped>
.github-link{text-decoration:none}.github-link:focus-visible{outline:2px solid var(--edo-primary);outline-offset:2px}@media(max-width:600px){.github-link{display:none}}
</style>
