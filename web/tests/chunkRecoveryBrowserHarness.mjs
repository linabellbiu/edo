// 生产构建回归服务：仅监听本机，通过 POST /__test/switch?version=b 模拟发布。
import { build } from 'vite'
import vue from '@vitejs/plugin-vue'
import { createServer } from 'node:http'
import { mkdtemp, readFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { dirname, extname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

const root = resolve(dirname(fileURLToPath(import.meta.url)), '..')
const output = await mkdtemp(resolve(tmpdir(), 'edo-navigation-recovery-'))
for (const version of ['a', 'b']) {
  await build({
    configFile: false, root, plugins: [vue()], logLevel: 'error',
    define: { __NAV_REC_VERSION__: JSON.stringify(version) },
    build: { outDir: resolve(output, version), emptyOutDir: true, rollupOptions: { input: resolve(root, 'tests/fixtures/chunk-recovery.html') } },
  })
}
let version = 'a', persistentFailure = false
const requests = []
const server = createServer(async (req, res) => {
  const url = new URL(req.url, 'http://127.0.0.1')
  if (url.pathname === '/__test/switch' && req.method === 'POST') {
    version = url.searchParams.get('version') === 'a' ? 'a' : 'b'
    persistentFailure = url.searchParams.get('fail') === 'true'
    res.end('ok')
    return
  }
  if (url.pathname === '/__test/state') {
    res.setHeader('Content-Type', 'application/json')
    res.end(JSON.stringify({ version, persistentFailure, requests }))
    return
  }
  const asset = url.pathname.startsWith('/assets/')
  const path = resolve(output, version, asset ? url.pathname.slice(1) : 'tests/fixtures/chunk-recovery.html')
  try {
    if (!path.startsWith(resolve(output, version) + '/') || (persistentFailure && /ChunkRecoveryTarget-.*\.js$/.test(path))) throw new Error('模拟缺失资源')
    const body = await readFile(path)
    res.setHeader('Content-Type', ({ '.js': 'text/javascript', '.css': 'text/css', '.html': 'text/html' })[extname(path)] || 'application/octet-stream')
    res.setHeader('Cache-Control', asset ? 'public, max-age=31536000, immutable' : 'no-store')
    requests.push({ path: url.pathname, version, status: 200 })
    res.end(body)
  } catch {
    requests.push({ path: url.pathname, version, status: 404 })
    res.writeHead(404, { 'Cache-Control': 'no-store' })
    res.end('Not found')
  }
})
server.listen(Number(process.env.EDO_NAV_TEST_PORT || 5197), '127.0.0.1', () => console.log('READY http://127.0.0.1:' + server.address().port))
