<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { ArrowUpRight } from 'lucide-vue-next'
import { useAuthStore } from '@/stores/auth'
import { assistantNavigationParts } from '@/utils/assistantNavigation'

const props = defineProps<{ content: string }>()
const { t } = useI18n(), auth = useAuthStore()
const parts = computed(() => assistantNavigationParts(props.content, permissions => auth.canAny(permissions)))
const hasLinks = computed(() => parts.value.some(part => part.kind === 'link'))
</script>

<template>
  <div class="assistant-rich-content"><template v-for="(part, index) in parts" :key="index"><span v-if="part.kind === 'text'">{{ part.text }}</span><RouterLink v-else-if="part.kind === 'link'" :to="part.path" class="assistant-navigation-link">{{ t('assistant.openManagement', { page: t(part.labelKey) }) }}<ArrowUpRight :size="13" aria-hidden="true" /></RouterLink><span v-else class="assistant-navigation-unavailable">{{ t(part.labelKey) }}（{{ t('assistant.navigationDenied') }}）</span></template></div>
  <p v-if="hasLinks" class="assistant-navigation-hint">{{ t('assistant.navigationHint') }}</p>
</template>

<style scoped>
.assistant-rich-content{white-space:pre-wrap;overflow-wrap:anywhere}.assistant-navigation-link{display:inline-flex;align-items:center;gap:3px;color:var(--edo-primary);text-decoration:underline;text-underline-offset:3px}.assistant-navigation-link:hover{opacity:.8}.assistant-navigation-link:focus-visible{outline:2px solid var(--edo-primary);outline-offset:3px;border-radius:2px}.assistant-navigation-unavailable,.assistant-navigation-hint{color:var(--edo-muted)}.assistant-navigation-hint{margin:8px 0 0;font-size:12px;line-height:1.6;white-space:normal}
</style>
