<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRoute, useRouter } from 'vue-router'

import { useAuthStore } from '@/stores/auth'
import HostView from '@/views/HostView.vue'
import InfrastructureView from '@/views/InfrastructureView.vue'

type Section = 'hosts' | 'resources'

const auth = useAuthStore()
const route = useRoute()
const router = useRouter()
const { t } = useI18n()

const options = computed(() => [
  { label: t('hostCluster.hosts'), value: 'hosts' },
  ...(auth.canAny(['cluster.read']) ? [{ label: t('hostCluster.resources'), value: 'resources' }] : []),
])

const section = computed<Section>({
  get() {
    return route.query.view === 'resources' && auth.canAny(['cluster.read']) ? 'resources' : 'hosts'
  },
  set(value) {
    const query = { ...route.query }
    delete query.create
    if (value === 'resources') query.view = 'resources'
    else {
      delete query.view
      delete query.node
    }
    void router.replace({ path: '/hosts', query })
  },
})
</script>

<template>
  <section class="host-cluster-page">
    <div v-if="options.length > 1" class="host-cluster-switch">
      <a-segmented v-model:value="section" :options="options" />
    </div>

    <Transition name="host-cluster-fade" mode="out-in">
      <HostView v-if="section === 'hosts'" key="hosts" />
      <InfrastructureView v-else key="resources" />
    </Transition>
  </section>
</template>

<style scoped>
.host-cluster-switch{display:flex;margin-bottom:12px}
.host-cluster-switch :deep(.ant-segmented){padding:4px;border:1px solid color-mix(in srgb,var(--zrt-primary) 18%,var(--zrt-border));border-radius:11px;background:var(--zrt-surface-soft);box-shadow:inset 0 1px 2px rgb(30 45 75 / 5%)}
.host-cluster-switch :deep(.ant-segmented-thumb){border-radius:8px;background:var(--zrt-primary);box-shadow:0 4px 12px color-mix(in srgb,var(--zrt-primary) 32%,transparent)}
.host-cluster-switch :deep(.ant-segmented-item){min-width:112px;min-height:40px;border-radius:8px;color:var(--zrt-muted);font-weight:650;transition:color 160ms ease,background-color 160ms ease,box-shadow 160ms ease}
.host-cluster-switch :deep(.ant-segmented-item-label){padding:7px 18px;line-height:26px}
.host-cluster-switch :deep(.ant-segmented-item:not(.ant-segmented-item-selected):hover){color:var(--zrt-text);background:color-mix(in srgb,var(--zrt-primary) 8%,transparent)}
.host-cluster-switch :deep(.ant-segmented-item-selected){color:#fff;background:var(--zrt-primary);box-shadow:0 4px 12px color-mix(in srgb,var(--zrt-primary) 32%,transparent)}
.host-cluster-switch :deep(.ant-segmented-item-selected .ant-segmented-item-label){color:#fff}
.host-cluster-switch :deep(.ant-segmented-item:focus-visible){outline:2px solid color-mix(in srgb,var(--zrt-primary) 48%,transparent);outline-offset:2px}
.host-cluster-fade-enter-active,.host-cluster-fade-leave-active{transition:opacity 150ms ease,transform 180ms ease}.host-cluster-fade-enter-from{opacity:0;transform:translateY(4px)}.host-cluster-fade-leave-to{opacity:0;transform:translateY(-2px)}@media(prefers-reduced-motion:reduce){.host-cluster-switch :deep(.ant-segmented-item),.host-cluster-fade-enter-active,.host-cluster-fade-leave-active{transition:none}}
</style>
