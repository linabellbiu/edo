<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { ConfigProvider } from 'ant-design-vue'

import type { NavBranch, NavItem } from '@/router/navigation'
import SidebarNavItem from '@/components/SidebarNavItem.vue'

export interface SidebarSection {
  key: string
  branches: NavBranch[]
}

defineProps<{
  overviewItems: NavItem[]
  sections: SidebarSection[]
  selectedKeys: string[]
  openKeys: string[]
  collapsed: boolean
}>()
const emit = defineEmits<{
  navigate: [path: string]
  'open-change': [keys: string[]]
  'toggle-branch': [key: string]
}>()
const { t } = useI18n()

const sidebarTheme = {
  components: {
    Menu: {
      colorItemBgHover: 'var(--edo-nav-hover-bg)',
      colorItemTextHover: 'var(--edo-nav-hover-color)',
      colorSubItemBg: 'var(--edo-nav-submenu-bg)',
      colorItemBgSelected: 'var(--edo-nav-selected-bg)',
      colorItemTextSelected: 'var(--edo-nav-selected-color)',
      colorPrimaryBorder: 'var(--edo-primary)',
    },
  },
}
</script>

<template>
  <ConfigProvider :theme="sidebarTheme">
    <a-menu
      :selected-keys="selectedKeys"
      :open-keys="openKeys"
      mode="inline"
      :inline-collapsed="collapsed"
      :trigger-sub-menu-action="collapsed ? 'click' : 'hover'"
      @open-change="(keys: string[]) => emit('open-change', keys)"
    >
      <a-menu-item v-for="item in overviewItems" :key="item.path">
        <template #icon><component :is="item.icon" :size="18" /></template>
        <a :href="item.path" @click.left.exact.prevent="emit('navigate', item.path)">{{ t(item.label) }}</a>
      </a-menu-item>
    </a-menu>

    <a-menu
      v-for="section in sections"
      :key="section.key"
      :selected-keys="selectedKeys"
      :open-keys="openKeys"
      mode="inline"
      :inline-indent="16"
      :inline-collapsed="collapsed"
      :trigger-sub-menu-action="collapsed ? 'click' : 'hover'"
      @open-change="(keys: string[]) => emit('open-change', keys)"
    >
      <a-sub-menu v-for="branch in section.branches" :key="branch.key">
        <template #icon>
          <button
            v-if="collapsed"
            type="button"
            class="navigation-group-icon"
            :aria-label="t(branch.label)"
            :aria-expanded="openKeys.includes(branch.key)"
            aria-haspopup="menu"
            @click.stop="emit('toggle-branch', branch.key)"
          ><component :is="branch.icon" :size="18" /></button>
          <component :is="branch.icon" v-else :size="18" />
        </template>
        <template #title>
          <button
            v-if="!collapsed"
            type="button"
            class="navigation-group-title"
            :aria-expanded="openKeys.includes(branch.key)"
            @click.stop="emit('toggle-branch', branch.key)"
          >{{ t(branch.label) }}</button>
          <span v-else>{{ t(branch.label) }}</span>
        </template>
        <SidebarNavItem v-for="item in branch.items" :key="item.path" :item="item" @navigate="emit('navigate', $event)" />
      </a-sub-menu>
    </a-menu>
  </ConfigProvider>
</template>

<style scoped>
.navigation-group-title, .navigation-group-icon { padding: 0; border: 0; background: transparent; color: inherit; font: inherit; cursor: pointer; text-align: inherit; }
.navigation-group-icon { display: inline-flex; align-items: center; vertical-align: middle; }
.navigation-group-title:focus-visible, .navigation-group-icon:focus-visible { outline: 1px solid var(--edo-primary); outline-offset: 3px; border-radius: 2px; }
</style>
