import { create } from 'zustand'

import { getReadyStatus, type ReadyResponse } from '@/api/client'

interface SystemState {
  ready: ReadyResponse | null
  loading: boolean
  error: string
  updatedAt: Date | null
  refresh: () => Promise<void>
}

export const useSystemStore = create<SystemState>((set) => ({
  ready: null,
  loading: false,
  error: '',
  updatedAt: null,

  refresh: async () => {
    set({ loading: true, error: '' })
    try {
      const ready = await getReadyStatus()
      set({ ready, updatedAt: new Date() })
    } catch {
      set({ ready: null, error: '无法连接 ZRT API，请检查服务是否已启动。' })
    } finally {
      set({ loading: false })
    }
  },
}))
