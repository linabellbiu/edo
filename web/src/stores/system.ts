import { ref } from 'vue'
import { defineStore } from 'pinia'

import { getReadyStatus, type ReadyResponse } from '@/api/client'

export const useSystemStore = defineStore('system', () => {
  const ready = ref<ReadyResponse | null>(null)
  const loading = ref(false)
  const error = ref('')
  const updatedAt = ref<Date | null>(null)

  async function refresh() {
    loading.value = true
    error.value = ''
    try {
      ready.value = await getReadyStatus()
      updatedAt.value = new Date()
    } catch {
      ready.value = null
      error.value = '无法连接 EDO API，请检查服务是否已启动。'
    } finally {
      loading.value = false
    }
  }

  return { ready, loading, error, updatedAt, refresh }
})
