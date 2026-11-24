import { ref } from 'vue'
import { defineStore } from 'pinia'

import { getReadyStatus, type ReadyResponse } from '@/api/client'

export const useSystemStore = defineStore('system', () => {
  const ready = ref<ReadyResponse | null>(null)
  const loading = ref(false)
  const failed = ref(false)
  const updatedAt = ref<Date | null>(null)
  let revision = 0

  async function refresh() {
    // 仅最新回执可通知页面记录采样或失败断点。
    const request = ++revision
    loading.value = true
    try {
      const response = await getReadyStatus()
      if (request !== revision) return false
      ready.value = response
      failed.value = false
      updatedAt.value = new Date()
      return true
    } catch {
      if (request !== revision) return false
      failed.value = true
      return true
    } finally {
      if (request === revision) loading.value = false
    }
  }

  return { ready, loading, failed, updatedAt, refresh }
})
