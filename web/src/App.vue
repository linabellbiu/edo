<script setup lang="ts">
import { computed } from 'vue'
import { ConfigProvider } from 'ant-design-vue'
import zhCN from 'ant-design-vue/es/locale/zh_CN'
import enUS from 'ant-design-vue/es/locale/en_US'

import { usePreferencesStore } from '@/stores/preferences'

const preferences = usePreferencesStore()
const antLocale = computed(() => preferences.locale === 'zh-CN' ? zhCN : enUS)
const theme = computed(() => ({
  token: {
    colorPrimary: '#4f6ef7',
    borderRadius: 6,
    borderRadiusLG: 8,
    colorBgLayout: preferences.theme === 'dark' ? '#0f1014' : '#f5f6f8',
    colorBgContainer: preferences.theme === 'dark' ? '#17181d' : '#ffffff',
    colorBorderSecondary: preferences.theme === 'dark' ? '#292b33' : '#e9eaee',
  },
  algorithm: preferences.antAlgorithm,
}))
</script>

<template>
  <ConfigProvider :locale="antLocale" :theme="theme">
    <RouterView />
  </ConfigProvider>
</template>
