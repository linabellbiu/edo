#!/usr/bin/env node
import { randomBytes } from 'node:crypto';
import { chmod, lstat, mkdir, readFile, realpath, rename, writeFile } from 'node:fs/promises';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

// 只对专属普通账号和 E2E 容器本地能力操作；终端输入输出不保存。
if (typeof globalThis.Bun !== 'object' || typeof globalThis.Bun.version !== 'string') throw new Error('终端专项请使用 Bun 运行，不会在不支持的运行时修改资源');
const options = {};
for (let index = 2; index < process.argv.length; index += 2) {
  const key = process.argv[index];
  if (!['--admin-file', '--manifest', '--out', '--phase'].includes(key) || !process.argv[index + 1]) throw new Error('参数无效');
  options[key.slice(2)] = process.argv[index + 1];
}
if (!['enable', 'verify', 'restore'].includes(options.phase) || !options['admin-file'] || !options.manifest || !options.out) throw new Error('请提供私有管理员、身份清单、输出目录和 enable/verify/restore 阶段');
async function privateRead(path) {
  const parent = await lstat(dirname(path)), info = await lstat(path);
  if (!parent.isDirectory() || parent.isSymbolicLink() || (parent.mode & 0o077) || !info.isFile() || info.isSymbolicLink() || (info.mode & 0o077) || process.getuid && (info.uid !== process.getuid() || parent.uid !== process.getuid())) throw new Error('仅接受当前用户的私有普通文件');
  try { return JSON.parse(await readFile(path, 'utf8')); } catch { throw new Error('私有 JSON 格式无效，不输出文件内容'); }
}
const out = resolve(options.out);
await mkdir(out, { recursive: true, mode: 0o700 });
const directory = await lstat(out), actual = await realpath(out), root = resolve(dirname(fileURLToPath(import.meta.url)), '../..');
if (!directory.isDirectory() || directory.isSymbolicLink() || (directory.mode & 0o077) || process.getuid && directory.uid !== process.getuid() || actual === root || actual.startsWith(root + '/')) throw new Error('输出目录必须私有且位于源码之外');
const manifest = await privateRead(resolve(options.manifest)), credential = await privateRead(resolve(options['admin-file']));
const uuid = (value) => typeof value === 'string' && /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i.test(value);
const prefix = (value) => typeof value === 'string' && /^rbac[-_][a-z0-9][a-z0-9_-]{5,110}$/.test(value);
if (manifest.baseURL !== 'http://127.0.0.1:18180' || !prefix(manifest.prefix) || !prefix(manifest.fixturePrefix) || !(manifest.fixturePrefix === manifest.prefix || manifest.fixturePrefix.startsWith(manifest.prefix + '-')) || !uuid(manifest.users?.terminal_operator?.id) || !uuid(manifest.projects?.DEFAULT)) throw new Error('实例与专项身份不匹配');
const identity = manifest.users.terminal_operator, project = manifest.projects.DEFAULT;
if (!Array.isArray(manifest.created) || !manifest.created.some((item) => item.kind === 'user' && item.id === identity.id) || ![identity, credential].every((item) => typeof item.username === 'string' && item.username && typeof item.password === 'string' && item.password)) throw new Error('账号不属于本轮专属用户白名单或凭据格式无效');
const statePath = join(out, 'state.json');
let state;
try { state = await privateRead(statePath); } catch (error) { if (error.code !== 'ENOENT') throw error; }
if (state && (state.user !== identity.id || state.project !== project || state.prefix !== manifest.fixturePrefix || !/^\/tmp\/rbac-terminal-[0-9a-f]{20}\.txt$/.test(state.path))) throw new Error('状态不属于本轮专项');
const validOverrides = (value) => value && ['allow', 'deny'].every((key) => Array.isArray(value[key]) && value[key].length <= 512 && value[key].every((code) => typeof code === 'string' && /^[a-z][a-z0-9_.]{1,95}$/.test(code)));
if (state && (!validOverrides(state.originalOverrides) || state.appliedOverrides && !validOverrides(state.appliedOverrides) || typeof state.originalHost?.name !== 'string' || JSON.stringify(state.originalHost?.kinds) !== JSON.stringify(['docker']) || !Array.isArray(state.originalHost?.environments) || !state.originalHost.environments.every(uuid))) throw new Error('原始权限或主机恢复快照无效，不执行恢复');
if (options.phase === 'enable' && state || options.phase !== 'enable' && !state) throw new Error('专项阶段与状态不匹配');
state ||= { user: identity.id, project, prefix: manifest.fixturePrefix, path: `/tmp/rbac-terminal-${randomBytes(10).toString('hex')}.txt`, cases: [] };
async function save() {
  const temporary = statePath + '.' + randomBytes(4).toString('hex') + '.tmp';
  await writeFile(temporary, JSON.stringify(state, null, 2) + '\n', { mode: 0o600, flag: 'wx' });
  await chmod(temporary, 0o600); await rename(temporary, statePath);
}
function record(id, passed, status) { state.cases.push({ id, passed, status, at: new Date().toISOString() }); console.log(`${passed ? 'PASS' : 'FAIL'} ${id}`); if (!passed) throw new Error('专项断言未满足'); }
class Client {
  constructor(value) { this.value = value; this.cookie = ''; }
  async request(method, path, body, headers = {}) {
    if (!path.startsWith('/') || path.startsWith('//')) throw new Error('接口路径无效');
    const ownFile = (value) => value === state.path || value === state.path + '.second';
    const target = new URL(path, 'http://127.0.0.1:18180');
    const fileRequest = target.pathname.startsWith('/terminals/hosts/edo-local-host/files/');
    const allowed = method === 'POST' && ['/auth/login', '/auth/logout'].includes(path)
      || method === 'GET' && ['/hosts/edo-local-host', '/users?limit=200', '/terminals/hosts/edo-local-host/ws'].includes(path)
      || method === 'PUT' && path === `/users/${identity.id}/permissions`
      || method === 'PUT' && path === '/hosts/edo-local-host' && body?.name === state.originalHost?.name && [JSON.stringify(['docker']), JSON.stringify(['docker', 'local_exec'])].includes(JSON.stringify(body.capability_kinds))
      || fileRequest && method === 'GET' && ['content', 'stat'].some((action) => target.pathname.endsWith('/' + action)) && ownFile(target.searchParams.get('path'))
      || fileRequest && method === 'POST' && target.pathname.endsWith('/upload') && ownFile(target.searchParams.get('path'))
      || fileRequest && method === 'PUT' && path === '/terminals/hosts/edo-local-host/files/content' && ownFile(body?.path);
    if (!allowed) throw new Error('请求超出本轮终端专项白名单');
    const response = await fetch('http://127.0.0.1:18180/api/v1' + path, { method, headers: { Accept: 'application/json', 'X-EDO-Project-ID': project, ...(this.cookie ? { Cookie: this.cookie } : {}), ...(body === undefined ? {} : { 'Content-Type': typeof body === 'string' ? 'application/octet-stream' : 'application/json' }), ...headers }, body: body === undefined ? undefined : typeof body === 'string' ? body : JSON.stringify(body), redirect: 'manual', signal: AbortSignal.timeout(30000) });
    if (response.status >= 300 && response.status < 400) throw new Error('接口返回重定向');
    if (response.headers.getSetCookie().length) this.cookie = response.headers.getSetCookie().map((value) => value.split(';', 1)[0]).join('; ');
    const raw = await response.text(); let data;
    try { data = JSON.parse(raw); } catch { /* 不保存未知正文。 */ }
    return { status: response.status, data };
  }
  async login() {
    const result = await this.request('POST', '/auth/login', { username: this.value.username, password: this.value.password });
    const current = result.data?.user;
    const correctIdentity = uuid(current?.id) && (this === admin ? current.is_superuser === true : current.id === identity.id && current.is_superuser === false);
    record(this === admin ? 'admin.login' : 'terminal-user.login', result.status === 200 && Boolean(this.cookie) && correctIdentity, result.status);
  }
}
const admin = new Client(credential), user = new Client(identity);
let identityVerified = false;
async function call(client, id, method, path, body, expected = 200, predicate = () => true, headers) {
  const result = await client.request(method, path, body, headers);
  record(id, result.status === expected && predicate(result.data), result.status); await save(); return result;
}
const hostPath = '/hosts/edo-local-host', files = '/terminals/hosts/edo-local-host/files';
async function host(id) { return (await call(admin, id, 'GET', hostPath, undefined, 200, (data) => data?.host?.is_builtin && data.host.mode === 'local')).data.host; }
async function ownUser() {
  const users = await call(admin, 'user.ownership-snapshot', 'GET', '/users?limit=200', undefined, 200, (data) => Array.isArray(data?.users));
  const own = users.data.users.find((item) => item.id === identity.id);
  record('user.own-identity-confirmed', Boolean(own && own.is_superuser === false && own.username === identity.username && own.nickname === manifest.fixturePrefix + '-terminal_operator'));
  identityVerified = true;
  return own;
}
const kinds = (value) => (value.capabilities || []).map((item) => item.kind).sort();
const equal = (left, right) => JSON.stringify(left) === JSON.stringify(right);
const normalizedOverrides = (value) => value && Array.isArray(value.allow) && Array.isArray(value.deny) ? { allow: [...new Set(value.allow)].sort(), deny: [...new Set(value.deny)].sort() } : null;
async function overrides(value, id) {
  if (!identityVerified) throw new Error('尚未核实专属账号身份，不修改或恢复权限');
  const current = await ownUser();
  record('permission.restore-conflict-guard', equal(normalizedOverrides(current.permission_overrides), normalizedOverrides(state.appliedOverrides || state.originalOverrides)));
  await call(admin, id, 'PUT', `/users/${identity.id}/permissions`, value, 204);
  state.appliedOverrides = normalizedOverrides(value); await save();
}
const deny = async (...permissions) => overrides({ allow: state.originalOverrides.allow, deny: [...new Set([...state.originalOverrides.deny, ...permissions])] }, 'permission.deny.' + permissions.join(','));
async function restoreUser() {
  if (!state.originalOverrides || !identityVerified) return;
  if (equal(normalizedOverrides(state.appliedOverrides || state.originalOverrides), normalizedOverrides(state.originalOverrides))) {
    const current = await ownUser();
    record('permission.restore-conflict-guard', equal(normalizedOverrides(current.permission_overrides), normalizedOverrides(state.originalOverrides)));
    return;
  }
  await overrides(state.originalOverrides, 'permission.restore');
}
async function content(id, path = state.path) { return call(user, id, 'GET', files + '/content?path=' + encodeURIComponent(path)); }
const delay = (ms) => new Promise((done) => setTimeout(done, ms));
async function wait(predicate, milliseconds) { const end = Date.now() + milliseconds; while (Date.now() < end) { if (predicate()) return true; await delay(100); } return predicate(); }
async function terminal() {
  const url = new URL('ws://127.0.0.1:18180/api/v1/terminals/hosts/edo-local-host/ws');
  url.searchParams.set('project_id', project);
  const socket = new WebSocket(url, { protocols: ['edo-terminal-v1'], headers: { Cookie: user.cookie, 'X-EDO-Project-ID': project } });
  socket.binaryType = 'arraybuffer';
  let ready = false, closed = false, marker = false, failed = false, output = '';
  socket.addEventListener('close', () => { closed = true; }); socket.addEventListener('error', () => { failed = true; });
  socket.addEventListener('message', (event) => {
    if (typeof event.data === 'string') { try { const value = JSON.parse(event.data); ready ||= value.type === 'ready'; failed ||= value.type === 'error'; } catch { failed = true; } }
    else { output = (output + new TextDecoder().decode(event.data)).slice(-8192); marker ||= /(?:\r?\n)RBAC_TERMINAL_READY\r?\n/.test(output); }
  });
  try {
    await wait(() => ready || closed || failed, 10000); record('host-terminal.real-ready', ready && !closed && !failed);
    socket.send(new TextEncoder().encode("printf 'RBAC_TERMINAL_READY\\n'\n"));
    await wait(() => marker || closed || failed, 10000); record('host-terminal.real-command-output', marker);
    await deny('host.terminal.open');
    await wait(() => closed, 16000); record('host-terminal.same-connection-revoked', closed);
  } finally { if (socket.readyState < 2) socket.close(1000); output = ''; }
}
try {
  await admin.login();
  const own = await ownUser();
  if (options.phase === 'enable') {
    const current = await host('host.snapshot');
    state.originalOverrides = own.permission_overrides;
    state.appliedOverrides = normalizedOverrides(own.permission_overrides);
    state.originalHost = { name: current.name, kinds: kinds(current), environments: [...current.environment_ids].sort() };
    record('host.original-docker-only', equal(state.originalHost.kinds, ['docker'])); await save();
    await call(admin, 'host.enable-local-terminal', 'PUT', hostPath, { name: current.name, capability_kinds: ['docker', 'local_exec'] });
    const after = await host('host.enabled-verify'); record('host.docker-preserved', equal(kinds(after), ['docker', 'local_exec']) && after.name === current.name);
  } else if (options.phase === 'verify') {
    await user.login(); await restoreUser();
    const first = 'RBAC_UPLOAD_V1\n', second = 'RBAC_EDIT_V2\n', other = state.path + '.second';
    const uploaded = await call(user, 'file.upload-allowed', 'POST', files + '/upload?path=' + encodeURIComponent(state.path), first);
    const initial = await content('file.read-allowed'); record('file.read-real-content', initial.data?.file?.content === first);
    await call(user, 'file.edit-allowed', 'PUT', files + '/content', { path: state.path, content: second, etag: uploaded.data?.file?.etag });
    await call(user, 'file.stale-edit-rejected', 'PUT', files + '/content', { path: state.path, content: 'MUST_NOT_COMMIT', etag: uploaded.data?.file?.etag }, 409);
    record('file.stale-edit-no-side-effect', (await content('file.content-after-stale')).data?.file?.content === second);
    await deny('terminal.file.upload');
    await call(user, 'file.upload-permission-required', 'POST', files + '/upload?path=' + encodeURIComponent(other), first, 403);
    await call(user, 'file.denied-upload-did-not-create', 'GET', files + '/stat?path=' + encodeURIComponent(other), undefined, 404);
    await restoreUser(); await deny('terminal.file.read');
    await call(user, 'file.read-permission-required', 'GET', files + '/content?path=' + encodeURIComponent(state.path), undefined, 403);
    const created = await call(user, 'file.upload-independent-of-read', 'POST', files + '/upload?path=' + encodeURIComponent(other), first);
    await call(user, 'file.overwrite-also-needs-read', 'POST', files + '/upload?path=' + encodeURIComponent(other), second, 403, undefined, { 'If-Match': created.data?.file?.etag });
    await call(user, 'file.edit-also-needs-read', 'PUT', files + '/content', { path: other, content: second, etag: created.data?.file?.etag }, 403);
    await restoreUser(); record('file.denied-overwrite-no-side-effect', (await content('file.content-after-denied-overwrite', other)).data?.file?.content === first);
    await deny('terminal.file.edit');
    await call(user, 'file.edit-permission-required', 'PUT', files + '/content', { path: other, content: second, etag: created.data?.file?.etag }, 403);
    await call(user, 'file.overwrite-also-needs-edit', 'POST', files + '/upload?path=' + encodeURIComponent(other), second, 403, undefined, { 'If-Match': created.data?.file?.etag });
    await restoreUser(); await deny('host.terminal.open', 'terminal.open');
    await call(user, 'host-terminal.requires-independent-action', 'GET', '/terminals/hosts/edo-local-host/ws', undefined, 403, undefined, { 'Sec-WebSocket-Protocol': 'edo-terminal-v1' });
    await content('file.read-independent-of-terminal-open');
    await restoreUser(); await terminal();
    state.verified = true;
  } else {
    await restoreUser();
    const current = await host('host.before-restore');
    record('host.restore-conflict-guard', current.name === state.originalHost.name && equal(kinds(current), ['docker', 'local_exec']) && equal([...current.environment_ids].sort(), state.originalHost.environments));
    await call(admin, 'host.restore-original-capabilities', 'PUT', hostPath, { name: state.originalHost.name, capability_kinds: state.originalHost.kinds });
    const after = await host('host.after-restore'); record('host.exact-capabilities-restored', equal(kinds(after), state.originalHost.kinds)); state.restored = true;
  }
} catch { state.failedPhase = options.phase; console.error('专项未完成；只检查私有状态中的安全断言，不输出请求、凭据或终端内容。'); process.exitCode = 1; }
finally {
  if (options.phase === 'verify') { try { await restoreUser(); } catch { state.restoreUserFailed = true; process.exitCode = 1; } }
  for (const client of [admin, user]) { if (client.cookie) { try { await client.request('POST', '/auth/logout'); } catch { /* 保留原测试状态。 */ } client.cookie = ''; } }
  await save(); console.log(JSON.stringify({ phase: options.phase, passed: state.cases.filter((item) => item.passed).length, failed: state.cases.filter((item) => !item.passed).length, restored: Boolean(state.restored) }));
}
