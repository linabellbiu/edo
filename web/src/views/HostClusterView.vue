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
      <a-segmented v-model:value="section" :options="options" class="edo-page-tabs" aria-label="主机与运行资源页面" />
    </div>

    <Transition name="host-cluster-fade" mode="out-in">
      <HostView v-if="section === 'hosts'" key="hosts" />
      <InfrastructureView v-else key="resources" />
    </Transition>
  </section>
</template>

<style scoped>
.host-cluster-switch{display:flex}
.host-cluster-fade-enter-active,.host-cluster-fade-leave-active{transition:opacity 150ms ease,transform 180ms ease}.host-cluster-fade-enter-from{opacity:0;transform:translateY(4px)}.host-cluster-fade-leave-to{opacity:0;transform:translateY(-2px)}@media(prefers-reduced-motion:reduce){.host-cluster-switch :deep(.ant-segmented-item),.host-cluster-fade-enter-active,.host-cluster-fade-leave-active{transition:none}}
</style>
