import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'
import { fileURLToPath, URL } from 'node:url'
import Components from 'unplugin-vue-components/vite'
import { AntDesignVueResolver } from 'unplugin-vue-components/resolvers'

const proxyTarget = process.env.EDO_WEB_PROXY_TARGET ?? 'http://127.0.0.1:8080'
const apiProxyTimeout = 15 * 60 * 1000
const useWSLMountPolling = Boolean(process.env.WSL_DISTRO_NAME) && process.cwd().startsWith('/mnt/')

export default defineConfig({
  plugins: [
    vue(),
    Components({
      dts: false,
      resolvers: [AntDesignVueResolver({ importStyle: false })],
    }),
  ],
  resolve: {
    alias: {
      '@': fileURLToPath(new URL('./src', import.meta.url)),
    },
  },
  server: {
    host: '127.0.0.1',
    port: 5173,
    watch: useWSLMountPolling ? { usePolling: true, interval: 500 } : undefined,
    proxy: {
      '/api': {
        target: proxyTarget,
        ws: true,
        timeout: apiProxyTimeout,
        proxyTimeout: apiProxyTimeout,
      },
    },
  },
  build: {
    outDir: 'dist',
    sourcemap: false,
  },
})
