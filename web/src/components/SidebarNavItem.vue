<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import type { NavItem } from '@/router/navigation'

defineProps<{ item: NavItem }>()
const emit = defineEmits<{ navigate: [path: string] }>()
const { t } = useI18n()
</script>

<template>
  <a-sub-menu v-if="item.children" :key="item.path">
    <template #icon><component :is="item.icon" :size="16" /></template>
    <template #title>{{ t(item.label) }}</template>
    <SidebarNavItem v-for="child in item.children" :key="child.path" :item="child" @navigate="emit('navigate', $event)" />
  </a-sub-menu>
  <a-menu-item v-else :key="item.path">
    <template #icon><component :is="item.icon" :size="16" /></template>
    <a :href="item.path" @click.left.exact.prevent="emit('navigate', item.path)">{{ t(item.label) }}</a>
  </a-menu-item>
</template>
