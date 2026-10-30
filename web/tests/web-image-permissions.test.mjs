import assert from 'node:assert/strict'
import { test } from 'node:test'
import { execFileSync } from 'node:child_process'
import { chmodSync, copyFileSync, existsSync, lstatSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'

const dockerfile = readFileSync(new URL('../Dockerfile', import.meta.url), 'utf8')
const instructions = dockerfile.split('\n').map(line => line.trim()).filter(line => line && !line.startsWith('#'))
const runtime = instructions.slice(instructions.findIndex(line => /^FROM\s+\S+\s+AS\s+web-runtime$/i.test(line)))
const modeOf = file => lstatSync(file).mode & 0o777

function privateContext(t) {
  const root = mkdtempSync(join(tmpdir(), 'edo-nginx-public-mode-'))
  const source = join(root, 'source.conf'), config = join(root, 'runtime.conf'), privateFile = join(root, 'private.txt')
  const publicRoot = join(root, 'public'), assets = join(publicRoot, 'assets'), nested = join(assets, '字体')
  const directories = [publicRoot, assets, nested]
  t.after(() => {
    for (const directory of directories) if (existsSync(directory)) chmodSync(directory, 0o700)
    rmSync(root, { recursive: true, force: true })
  })
  for (const directory of directories) { mkdirSync(directory); chmodSync(directory, 0o700) }
  writeFileSync(source, 'public nginx configuration\n', { mode: 0o600 })
  writeFileSync(privateFile, 'unrelated private fixture\n', { mode: 0o600 })
  const files = [join(publicRoot, 'index.html'), join(assets, 'entry.js'), join(nested, 'font file.woff2')]
  for (const file of files) { writeFileSync(file, 'public fixture\n', { mode: 0o600 }); chmodSync(file, 0o600) }
  chmodSync(source, 0o600)
  chmodSync(privateFile, 0o600)
  return { source, config, privateFile, publicRoot, directories, files }
}

test('NGINX 配置 COPY 在私有 0600 上下文中仍生成非 root 可读的只读配置', t => {
  const fixture = privateContext(t)
  const copy = runtime.find(line => /^COPY\s+(?:--\S+\s+)*web\/nginx\.conf\s+\/etc\/nginx\/nginx\.conf$/.test(line))
  assert.ok(copy, '运行阶段必须复制明确的 NGINX 配置')
  copyFileSync(fixture.source, fixture.config)
  chmodSync(fixture.config, modeOf(fixture.source))
  const permission = copy.match(/(?:^|\s)--chmod=([0-7]{3,4})(?:\s|$)/)?.[1]
  if (permission) chmodSync(fixture.config, Number.parseInt(permission, 8))
  assert.equal(modeOf(fixture.config), 0o444, 'NGINX 101 不能依赖源配置文件的私有权限')
  assert.equal(readFileSync(fixture.config, 'utf8'), 'public nginx configuration\n')
  assert.equal(modeOf(fixture.source), 0o600, '不能修改源上下文权限来掩盖镜像问题')
  assert.equal(modeOf(fixture.privateFile), 0o600)
  assert.equal(readFileSync(fixture.privateFile, 'utf8'), 'unrelated private fixture\n')
})

test('静态产物的真实 chmod 恢复私有目录和文件的公开只读权限且不影响相邻私有文件', t => {
  const fixture = privateContext(t)
  const owner = lstatSync(fixture.files[0]).uid
  const adjustment = runtime.find(line => /^RUN\s+chmod\s+-R\s+\S+\s+\/usr\/share\/nginx\/html\/?$/.test(line))
  if (adjustment) {
    const [, permission] = adjustment.match(/^RUN\s+chmod\s+-R\s+(\S+)\s+\/usr\/share\/nginx\/html\/?$/)
    assert.equal(permission, 'a=rX', '公共内容不可保留写权限或把普通文件全部变为可执行')
    execFileSync('/bin/chmod', ['-R', permission, fixture.publicRoot], { env: { PATH: '/usr/bin:/bin', LANG: 'C' }, timeout: 10_000 })
  }
  for (const directory of fixture.directories) assert.equal(modeOf(directory), 0o555, directory)
  for (const file of fixture.files) {
    assert.equal(modeOf(file), 0o444, file)
    assert.equal(readFileSync(file, 'utf8'), 'public fixture\n')
    assert.equal(lstatSync(file).uid, owner, '不能把公开文件的所有权改为运行用户')
  }
  assert.equal(modeOf(fixture.privateFile), 0o600, '递归权限调整不能越出公开发布目录')
  assert.equal(readFileSync(fixture.privateFile, 'utf8'), 'unrelated private fixture\n')
  assert.equal(modeOf(fixture.source), 0o600)
})

test('运行镜像只调整公开产物权限且继续使用非 root 用户，不增加可写所有权或系统修改', () => {
  assert.match(runtime[0], /^FROM nginx:\S+ AS web-runtime$/)
  assert.deepEqual(runtime.filter(line => /^USER\s/.test(line)), ['USER 101:101'])
  assert.deepEqual(runtime.filter(line => /^RUN\s/.test(line)), ['RUN chmod -R a=rX /usr/share/nginx/html'])
  assert.equal(runtime.some(line => /--chown|\bchown\b/.test(line)), false)
  assert.ok(runtime.includes('COPY --chmod=0444 web/nginx.conf /etc/nginx/nginx.conf'))
  const copiedAssets = runtime.indexOf('COPY --from=web-builder /src/web/dist/ /usr/share/nginx/html/')
  const adjustedAssets = runtime.indexOf('RUN chmod -R a=rX /usr/share/nginx/html')
  assert.ok(copiedAssets >= 0 && adjustedAssets > copiedAssets && adjustedAssets < runtime.indexOf('USER 101:101'))
  assert.ok(runtime.includes('ENTRYPOINT ["nginx"]'))
})
