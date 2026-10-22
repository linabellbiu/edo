// 供 Go 集成测试启动真实 Vite 代理；仅使用测试服务地址，不读取项目 .env。
import { createServer } from 'vite'

const server = await createServer({
  configFile: false,
  envFile: false,
  clearScreen: false,
  logLevel: 'error',
  server: {host: '127.0.0.1', port: 0, proxy: {'/api': {target: process.argv[2], ws: true}}},
})
await server.listen()
process.stdout.write(`http://127.0.0.1:${server.httpServer.address().port}\n`)
process.stdin.once('data', async () => { await server.close(); process.exit(0) })
