// cd web && npx vite --config tests/fixtures/session-popout.vite.mjs --port 15176 --strictPort
import { defineConfig } from 'vite'
import base from '../../vite.config.ts'
export default defineConfig({
  ...base,
  plugins: [...base.plugins, {
    name: 'session-popout-preview-route',
    configureServer(server) {
      server.middlewares.use((request, response, next) => {
        if (request.url?.startsWith('/api/')) { response.statusCode = 403; response.end('本地夹具禁止访问真实 API'); return }
        if (request.url?.startsWith('/sessions/view')) request.url = '/tests/fixtures/session-popout.html'
        next()
      })
    },
  }],
  server: { host: '127.0.0.1', port: 15176, strictPort: true, proxy: {} },
})
