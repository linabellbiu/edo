#!/usr/bin/env node
import { randomBytes } from 'node:crypto';
import { chmod, lstat, mkdir, readFile, realpath, rename, writeFile } from 'node:fs/promises';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

// 只保存专项账号的私有凭据；Cookie、响应正文和原始错误始终留在内存。
const options = {};
const args = process.argv.slice(2);
for (let index = 0; index < args.length; index++) {
  if (args[index] === '--help') {
    console.log('node tools/rbac-acceptance/revocation.mjs --admin-file /私有/admin.json --source-manifest /私有/http-01/manifest.json --out /私有/revoke-01 [--phase all|verify] [--base-url http://127.0.0.1:18180]');
    process.exit(0);
  }
  if (!['--admin-file', '--source-manifest', '--out', '--phase', '--base-url'].includes(args[index]) || !args[index + 1]) throw new Error('参数无效，请使用 --help');
  options[args[index].slice(2)] = args[++index];
}
const base = new URL(options['base-url'] || 'http://127.0.0.1:18180');
if (!['127.0.0.1', '[::1]'].includes(base.hostname) || !['http:', 'https:'].includes(base.protocol) || base.username || base.password || base.pathname !== '/' || base.search || base.hash) throw new Error('只允许明确的 loopback IP 根地址');
const phase = options.phase || 'all';
if (!['all', 'verify'].includes(phase) || !options['admin-file'] || !options['source-manifest'] || !options.out) throw new Error('缺少私有凭据、来源清单或输出目录');
const out = resolve(options.out);
const sourceRoot = resolve(dirname(fileURLToPath(import.meta.url)), '../..');
const uuid = (value) => typeof value === 'string' && /^[0-9a-f]{8}-[0-9a-f-]{27}$/i.test(value);
const safeCode = (value) => typeof value === 'string' && /^[a-zA-Z0-9_.:-]{1,100}$/.test(value) ? value : undefined;
async function privateDirectory(path, create = false) {
  if (create) await mkdir(path, { recursive: true, mode: 0o700 });
  const stat = await lstat(path);
  if (!stat.isDirectory() || stat.isSymbolicLink() || (stat.mode & 0o077) || process.getuid && stat.uid !== process.getuid()) throw new Error('目录必须是当前用户所有的 700 私有真实目录');
}
async function privateJSON(path) {
  await privateDirectory(dirname(path));
  const stat = await lstat(path);
  if (!stat.isFile() || stat.isSymbolicLink() || (stat.mode & 0o077) || process.getuid && stat.uid !== process.getuid()) throw new Error('JSON 必须是当前用户所有的 600 私有普通文件');
  try { return JSON.parse(await readFile(path, 'utf8')); } catch { throw new Error('私有 JSON 格式无效，内容不输出'); }
}
async function saveJSON(path, data) {
  const temporary = `${path}.${randomBytes(4).toString('hex')}.tmp`;
  await writeFile(temporary, `${JSON.stringify(data, null, 2)}\n`, { mode: 0o600, flag: 'wx' });
  await chmod(temporary, 0o600);
  await rename(temporary, path);
}
await privateDirectory(out, true);
const actualOut = await realpath(out);
if (actualOut === sourceRoot || actualOut.startsWith(`${sourceRoot}/`)) throw new Error('私有证据不能写入源码目录');
const adminCredential = await privateJSON(resolve(options['admin-file']));
if (typeof adminCredential.username !== 'string' || typeof adminCredential.password !== 'string' || !adminCredential.password) throw new Error('管理员文件格式无效');
const source = await privateJSON(resolve(options['source-manifest']));
if (source.baseURL !== base.origin || !uuid(source.departments?.D1) || !uuid(source.departments?.D2) || source.departments.D1 === source.departments.D2) throw new Error('来源清单与目标或两个部门不匹配');
const manifestPath = join(out, 'manifest.json');
const credentialPath = join(out, 'credentials.json');
let manifest, credentials = {};
try { manifest = await privateJSON(manifestPath); } catch (error) { if (error.code !== 'ENOENT') throw error; }
if (phase === 'all' && manifest) throw new Error('本轮已经存在，重复验收请使用 --phase verify');
if (phase === 'verify' && !manifest) throw new Error('没有专项清单，不能重复验收');
if (manifest && (manifest.baseURL !== base.origin || !/^rbac_revoke_[a-z0-9_]+$/.test(manifest.prefix))) throw new Error('专项清单与目标不匹配');
if (manifest) credentials = await privateJSON(credentialPath);
const report = { schema: 1, baseURL: base.origin, phase, startedAt: new Date().toISOString(), cases: [], limitations: [
  '只操作本轮 rbac_revoke 标记的角色、用户、项目和成员，不创建应用、不执行任务、不重载服务、不直接读写数据库。',
  '业务正向读取必须在同一专项用户会话中先取得真实 HTTP 200；拒绝不接受 404 或未建立窗口。',
  '换部门和停用账号引起的会话失效单独记录；恢复后重新登录不算原会话继续使用。',
  '状态消息的局部资源过滤需要真实业务变更触发；本轮无授权业务写入目标时明确记为前置未满足。',
] };
function record(id, passed, details = {}) {
  report.cases.push({ id, status: passed ? '通过' : '失败', at: new Date().toISOString(), ...details });
  console.log(`${passed ? 'PASS' : 'FAIL'} ${id}`);
}
function skip(id, reason) { report.cases.push({ id, status: '前置未满足', reason }); console.log(`SKIP ${id}`); }
function expect(id, result, status, predicate = () => true, details = {}) {
  const expected = Array.isArray(status) ? status : [status];
  const passed = expected.includes(result.status) && predicate(result.data);
  record(id, passed, { expected: status, actual: result.status, code: result.code, ...details });
  return passed;
}
function requireResult(id, result, status, predicate) {
  if (!expect(id, result, status, predicate)) throw new Error('必要步骤没有满足，停止后续依赖操作');
  return result;
}
async function checkpoint() {
  if (manifest) await saveJSON(manifestPath, manifest);
  if (Object.keys(credentials).length) await saveJSON(credentialPath, credentials);
}
const sharedPermissions = ['project.read', 'application.read', 'pipeline_run.read', 'deployment.read'];
const independentPermissions = ['project.read', 'application.read'];
const ownID = (kind, value) => uuid(value) && manifest?.created?.some((item) => item.kind === kind && item.id === value);
function authorizeMutation(method, path, body) {
  if (method === 'GET' || path === '/auth/login' || path === '/auth/logout') return;
  if (method === 'POST' && ['/roles', '/users', '/projects'].includes(path)) {
    const marker = path === '/users' ? body?.nickname : path === '/roles' ? body?.display_name : body?.name;
    if (typeof marker === 'string' && marker.startsWith(manifest.prefix) && body?.description === manifest.prefix) return;
  }
  let match;
  if (method === 'PUT' && (match = path.match(/^\/roles\/([^/]+)\/permissions$/)) && ownID('role', match[1])) return;
  if (['PUT', 'PATCH'].includes(method) && (match = path.match(/^\/users\/([^/]+)\/(permissions|department|status)$/)) && ownID('user', match[1])) {
    if (match[2] !== 'department' || Object.values(manifest.departments).includes(body?.department_id)) return;
  }
  if (method === 'PUT' && (match = path.match(/^\/projects\/([^/]+)\/members\/([^/]+)$/)) && ownID('project', match[1]) && ownID('user', match[2]) && (body?.role_id === '' || ownID('role', body?.role_id))) return;
  if (method === 'PUT' && (match = path.match(/^\/projects\/([^/]+)$/)) && ownID('project', match[1]) && body?.name?.startsWith(manifest.prefix) && body?.description === manifest.prefix) return;
  throw new Error('写操作不属于专项资源白名单');
}
class Client {
  constructor(label, credential) { this.label = label; this.credential = credential; this.cookies = new Map(); }
  cookieHeader() { return [...this.cookies].map(([key, value]) => `${key}=${value}`).join('; '); }
  async request(method, path, { project, body } = {}) {
    if (!path.startsWith('/') || path.startsWith('//') || path.includes('?')) throw new Error('请求路径无效');
    authorizeMutation(method, path, body);
    const url = new URL(`/api/v1${path}`, base);
    const headers = { Accept: 'application/json' };
    if (project) headers['X-EDO-Project-ID'] = project;
    if (body !== undefined) headers['Content-Type'] = 'application/json';
    if (this.cookies.size) headers.Cookie = this.cookieHeader();
    const response = await fetch(url, { method, headers, body: body === undefined ? undefined : JSON.stringify(body), redirect: 'manual', signal: AbortSignal.timeout(30000) });
    if (response.status >= 300 && response.status < 400) throw new Error('接口重定向，停止验收');
    for (const item of response.headers.getSetCookie?.() || []) {
      const first = item.split(';', 1)[0], equals = first.indexOf('=');
      if (equals > 0) this.cookies.set(first.slice(0, equals), first.slice(equals + 1));
    }
    const raw = await response.text();
    let data = null;
    try { if (raw) data = JSON.parse(raw); } catch { /* 非 JSON 不能被当作成功业务数据。 */ }
    return { status: response.status, data, code: safeCode(data?.code) };
  }
  async login(id = `login.${this.label}`) {
    this.cookies.clear();
    const result = requireResult(id, await this.request('POST', '/auth/login', { body: this.credential }), 200, (data) => uuid(data?.user?.id) && this.cookies.size > 0);
    return result.data.user;
  }
}
const admin = new Client('admin', adminCredential);
const clients = [];
let worker, peer, ownershipConfirmed = false;
async function create(id, path, body, key) {
  const result = await admin.request('POST', path, { body });
  requireResult(id, result, 201, (data) => uuid(data?.[key]?.id));
  const value = result.data[key].id;
  manifest.created.push({ kind: key, id: value });
  await checkpoint();
  return value;
}
async function seed() {
  const runID = `${new Date().toISOString().replace(/[-:TZ.]/g, '').slice(0, 14)}_${randomBytes(3).toString('hex')}`;
  manifest = { schema: 1, baseURL: base.origin, sourceRunID: source.runID, prefix: `rbac_revoke_${runID}`, departments: { D1: source.departments.D1, D2: source.departments.D2 }, projects: {}, roles: {}, users: {}, created: [], seeded: false };
  await checkpoint();
  for (const key of ['A', 'B']) manifest.projects[key] = await create(`seed.project.${key}`, '/projects', { name: `${manifest.prefix}_${key}`, description: manifest.prefix, department_id: manifest.departments.D1, is_active: true }, 'project');
  manifest.roles.shared = await create('seed.role.shared', '/roles', { name: `${manifest.prefix}_shared`, display_name: `${manifest.prefix}_shared`, description: manifest.prefix, permissions: sharedPermissions }, 'role');
  manifest.roles.independent = await create('seed.role.independent', '/roles', { name: `${manifest.prefix}_independent`, display_name: `${manifest.prefix}_independent`, description: manifest.prefix, permissions: independentPermissions }, 'role');
  for (const key of ['worker', 'peer']) {
    const credential = { username: `rv${Date.now().toString(36)}${randomBytes(3).toString('hex')}`, password: `${randomBytes(24).toString('base64url')}!a9` };
    credentials[key] = credential;
    manifest.users[key] = await create(`seed.user.${key}`, '/users', { ...credential, nickname: `${manifest.prefix}_${key}`, description: manifest.prefix, department_id: manifest.departments.D1, role_ids: [] }, 'user');
    for (const project of ['A', 'B']) await setMember(key, project, manifest.roles[key === 'worker' ? 'shared' : 'independent'], `seed.member.${key}.${project}`);
  }
  manifest.seeded = true;
  await checkpoint();
}
async function setMember(user, project, role, id) {
  return requireResult(id, await admin.request('PUT', `/projects/${manifest.projects[project]}/members/${manifest.users[user]}`, { body: { role_id: role } }), 204);
}
async function setShared(permissions, id) {
  return requireResult(id, await admin.request('PUT', `/roles/${manifest.roles.shared}/permissions`, { body: { permissions } }), 200, (data) => uuid(data?.role?.id) && data.role.id === manifest.roles.shared && Array.isArray(data.role.permissions) && JSON.stringify([...data.role.permissions].sort()) === JSON.stringify([...permissions].sort()));
}
async function setProject(project, active, id) {
  return requireResult(id, await admin.request('PUT', `/projects/${manifest.projects[project]}`, { body: { name: `${manifest.prefix}_${project}`, description: manifest.prefix, is_active: active } }), 204);
}
async function read(client, id, project = 'A', path = '/applications', status = 200) {
  return expect(id, await client.request('GET', path, { project: manifest.projects[project] }), status, (data) => status !== 200 || data !== null && typeof data === 'object');
}
async function baseline(id, client = worker, project = 'A') {
  if (!await read(client, `${id}.precondition-200`, project)) { skip(id, '专项用户未在此会话获得目标接口 200，不执行撤权断言'); return false; }
  return true;
}
async function validateOwnership() {
  if (!manifest.seeded) throw new Error('专项种子不完整，拒绝修改');
  const roles = requireResult('ownership.roles-readable', await admin.request('GET', '/roles'), 200, (data) => Array.isArray(data?.roles)).data.roles;
  for (const id of Object.values(manifest.roles)) if (!ownID('role', id) || !roles.some((item) => item.id === id && item.name.startsWith(manifest.prefix) && item.description === manifest.prefix)) throw new Error('专项角色身份校验失败');
  const projects = requireResult('ownership.projects-readable', await admin.request('GET', '/projects'), 200, (data) => Array.isArray(data?.projects)).data.projects;
  for (const id of Object.values(manifest.projects)) if (!ownID('project', id) || !projects.some((item) => item.id === id && item.name.startsWith(manifest.prefix) && item.description === manifest.prefix)) throw new Error('专项项目身份校验失败');
  const users = requireResult('ownership.users-readable', await admin.request('GET', '/users'), 200, (data) => Array.isArray(data?.users)).data.users;
  for (const [key, id] of Object.entries(manifest.users)) if (!ownID('user', id) || !users.some((item) => item.id === id && item.nickname.startsWith(manifest.prefix) && !item.is_superuser && item.username === credentials[key]?.username && typeof credentials[key]?.password === 'string' && credentials[key].password.length > 0)) throw new Error('专项用户身份校验失败');
  ownershipConfirmed = true;
}
async function resetOwnState(prefix) {
  await setShared(sharedPermissions, `${prefix}.shared-role`);
  for (const project of ['A', 'B']) await setProject(project, true, `${prefix}.project.${project}`);
  for (const key of ['worker', 'peer']) {
    requireResult(`${prefix}.user.${key}.active`, await admin.request('PATCH', `/users/${manifest.users[key]}/status`, { body: { active: true } }), 204);
    const users = requireResult(`${prefix}.user.${key}.inspect`, await admin.request('GET', '/users'), 200, (data) => Array.isArray(data?.users)).data.users;
    const current = users.find((item) => item.id === manifest.users[key]);
    if (!current) throw new Error('专项恢复目标缺失');
    if (current.department_id !== manifest.departments.D1) requireResult(`${prefix}.user.${key}.department`, await admin.request('PUT', `/users/${manifest.users[key]}/department`, { body: { department_id: manifest.departments.D1 } }), 204);
    requireResult(`${prefix}.user.${key}.overrides`, await admin.request('PUT', `/users/${manifest.users[key]}/permissions`, { body: { allow: [], deny: [] } }), 204);
    for (const project of ['A', 'B']) await setMember(key, project, manifest.roles[key === 'worker' ? 'shared' : 'independent'], `${prefix}.member.${key}.${project}`);
  }
}
async function snapshotOwnState() {
  const users = requireResult('final-state.users-readable', await admin.request('GET', '/users'), 200, (data) => Array.isArray(data?.users)).data.users;
  const roles = requireResult('final-state.roles-readable', await admin.request('GET', '/roles'), 200, (data) => Array.isArray(data?.roles)).data.roles;
  const state = { users: [], roles: [], projects: [], memberships: [] };
  for (const user of users) if (ownID('user', user.id)) state.users.push({ id: user.id, departmentID: user.department_id, active: user.is_active, allow: (user.permission_overrides?.allow || []).map(safeCode).filter(Boolean), deny: (user.permission_overrides?.deny || []).map(safeCode).filter(Boolean) });
  for (const role of roles) if (ownID('role', role.id)) state.roles.push({ id: role.id, permissions: (role.permissions || []).map(safeCode).filter(Boolean) });
  for (const projectID of Object.values(manifest.projects)) {
    const response = requireResult('final-state.project-readable', await admin.request('GET', `/projects/${projectID}`), 200, (data) => data?.project?.id === projectID && Array.isArray(data?.members));
    state.projects.push({ id: projectID, active: response.data.project.is_active });
    for (const member of response.data.members) if (ownID('user', member.user_id)) state.memberships.push({ projectID, userID: member.user_id, roleID: member.role_id });
  }
  report.finalState = state;
  const restored = state.users.length === 2 && state.users.every((user) => user.active && user.departmentID === manifest.departments.D1 && !user.allow.length && !user.deny.length)
    && state.projects.length === 2 && state.projects.every((project) => project.active)
    && state.roles.length === 2 && state.roles.every((role) => JSON.stringify([...role.permissions].sort()) === JSON.stringify([...(role.id === manifest.roles.shared ? sharedPermissions : independentPermissions)].sort()))
    && state.memberships.length === 4 && state.memberships.every((member) => member.roleID === manifest.roles[member.userID === manifest.users.worker ? 'shared' : 'independent']);
  record('final-state.own-resources-restored', restored);
}
async function roleCases() {
  if (!await baseline('role.reduce')) return;
  await setShared(sharedPermissions.filter((item) => item !== 'application.read'), 'role.reduce.mutation');
  await read(worker, 'role.reduce.same-session-denied', 'A', '/applications', 403);
  await read(worker, 'role.reduce.other-permission-kept', 'A', '/pipeline-runs');
  await setShared(sharedPermissions, 'role.reduce.restore');
  await read(worker, 'role.reduce.same-session-restored');
  if (!await baseline('role.cross-project.A') || !await baseline('role.cross-project.B', worker, 'B') || !await baseline('role.cross-project.independent-A', peer) || !await baseline('role.cross-project.independent-B', peer, 'B')) return;
  await setShared(sharedPermissions.filter((item) => item !== 'application.read'), 'role.cross-project.mutation');
  await read(worker, 'role.cross-project.A-denied', 'A', '/applications', 403);
  await read(worker, 'role.cross-project.B-denied', 'B', '/applications', 403);
  await read(peer, 'role.cross-project.independent-A-kept');
  await read(peer, 'role.cross-project.independent-B-kept', 'B');
  await setShared(sharedPermissions, 'role.cross-project.restore');
  await read(worker, 'role.cross-project.A-restored');
  await read(worker, 'role.cross-project.B-restored', 'B');
}
async function overrideCases() {
  if (!await baseline('personal-deny')) return;
  requireResult('personal-deny.mutation', await admin.request('PUT', `/users/${manifest.users.worker}/permissions`, { body: { allow: [], deny: ['application.read'] } }), 204);
  await read(worker, 'personal-deny.same-session-denied', 'A', '/applications', 403);
  await read(worker, 'personal-deny.shared-role-B-denied', 'B', '/applications', 403);
  await read(worker, 'personal-deny.other-permission-kept', 'A', '/pipeline-runs');
  requireResult('personal-deny.restore', await admin.request('PUT', `/users/${manifest.users.worker}/permissions`, { body: { allow: [], deny: [] } }), 204);
  await read(worker, 'personal-deny.same-session-restored');
}
async function membershipCases() {
  if (!await baseline('membership.remove')) return;
  await setMember('worker', 'A', '', 'membership.remove.mutation');
  await read(worker, 'membership.remove.same-session-denied', 'A', '/applications', 403);
  await read(worker, 'membership.remove.B-kept', 'B');
  await setMember('worker', 'A', manifest.roles.shared, 'membership.remove.restore');
  await read(worker, 'membership.remove.same-session-restored');
}
async function departmentCases() {
  if (!await baseline('department.move')) return;
  requireResult('department.move.mutation', await admin.request('PUT', `/users/${manifest.users.worker}/department`, { body: { department_id: manifest.departments.D2 } }), 204);
  await read(worker, 'department.move.old-session-invalid', 'A', '/applications', 401);
  const moved = new Client('moved-worker', credentials.worker); clients.push(moved);
  const me = await moved.login('department.move.new-session-login');
  record('department.move.identity-updated', me.department_id === manifest.departments.D2);
  await read(moved, 'department.move.new-session-old-project-denied', 'A', '/applications', 403);
  requireResult('department.move.restore', await admin.request('PUT', `/users/${manifest.users.worker}/department`, { body: { department_id: manifest.departments.D1 } }), 204);
  await read(worker, 'department.move.old-session-stays-invalid', 'A', '/applications', 401);
  await worker.login('department.move.relogin-after-restoration');
  await read(worker, 'department.move.new-session-restored');
}
async function accountCases() {
  if (!await baseline('account.disable')) return;
  const dormant = new Client('dormant-worker', credentials.worker); clients.push(dormant);
  await dormant.login('account.disable.dormant-session-login');
  if (!await baseline('account.disable.dormant-session', dormant)) return;
  requireResult('account.disable.mutation', await admin.request('PATCH', `/users/${manifest.users.worker}/status`, { body: { active: false } }), 204);
  await read(worker, 'account.disable.old-session-invalid', 'A', '/applications', 401);
  const blocked = new Client('disabled-worker', credentials.worker); clients.push(blocked);
  expect('account.disable.login-denied', await blocked.request('POST', '/auth/login', { body: credentials.worker }), [401, 403]);
  requireResult('account.disable.restore', await admin.request('PATCH', `/users/${manifest.users.worker}/status`, { body: { active: true } }), 204);
  await read(worker, 'account.disable.old-session-stays-invalid', 'A', '/applications', 401);
  await read(dormant, 'account.disable.dormant-old-session-stays-invalid', 'A', '/applications', 401);
  await worker.login('account.disable.relogin-after-restoration');
  await read(worker, 'account.disable.new-session-restored');
}
async function projectCases() {
  if (!await baseline('project.disable')) return;
  await setProject('A', false, 'project.disable.mutation');
  await read(worker, 'project.disable.same-session-denied', 'A', '/applications', 403);
  await read(worker, 'project.disable.B-kept', 'B');
  await setProject('A', true, 'project.disable.restore');
  await read(worker, 'project.disable.same-session-restored');
}
const delay = (ms) => new Promise((resolveDelay) => setTimeout(resolveDelay, ms));
async function until(predicate, timeout) {
  const end = Date.now() + timeout;
  while (Date.now() < end) { if (predicate()) return true; await delay(100); }
  return predicate();
}
async function websocketCases() {
  if (!await baseline('ws.revoke')) return;
  if (typeof WebSocket !== 'function') { skip('ws.revoke', '当前 Node 没有内置 WebSocket 客户端'); return; }
  const url = new URL('/api/v1/events/ws', base);
  url.protocol = base.protocol === 'https:' ? 'wss:' : 'ws:';
  url.searchParams.set('project_id', manifest.projects.A);
  let socket;
  const state = { open: false, error: false, closed: false, code: null, events: [] };
  try {
    // Node 内置客户端支持带请求头的 WebSocketInit；不把 Cookie 放入 URL 或文件。
    socket = new WebSocket(url, { protocols: ['edo-state-v1'], headers: { Cookie: worker.cookieHeader(), 'X-EDO-Project-ID': manifest.projects.A } });
    socket.addEventListener('open', () => { state.open = true; });
    socket.addEventListener('error', () => { state.error = true; });
    socket.addEventListener('close', (event) => { state.closed = true; state.code = event.code; });
    socket.addEventListener('message', (event) => {
      try {
        const data = JSON.parse(String(event.data));
        if (['resync', 'heartbeat', 'changed'].includes(data.type)) state.events.push({ type: data.type, resources: Array.isArray(data.resources) ? data.resources.filter((value) => ['applications', 'application_sync', 'pipeline_runs', 'release_plans', 'deployment_plans', 'deployments'].includes(value)) : [] });
      } catch { /* 不保存未知消息或内容。 */ }
    });
    const connected = await until(() => state.error || state.closed || state.open && state.events.some((event) => event.type === 'resync'), 12000);
    const valid = connected && state.open && socket.protocol === 'edo-state-v1' && state.events.some((event) => event.type === 'resync');
    if (!valid) { skip('ws.revoke', '正常状态 WS 没有成功完成协议与 resync 前置，不断言撤权'); return; }
    record('ws.revoke.connected-and-resync', true, { protocol: socket.protocol });
    const started = Date.now();
    await setShared(['project.read'], 'ws.revoke.permissions-mutation');
    await read(worker, 'ws.revoke.http-confirmed', 'A', '/applications', 403);
    const closed = await until(() => state.closed, 28000);
    record('ws.revoke.same-connection-policy-close', closed && state.code === 1008, { closeCode: state.code, elapsedMs: Date.now() - started, observedTypes: [...new Set(state.events.map((event) => event.type))] });
  } finally {
    if (socket && socket.readyState < 2) socket.close(1000);
    await setShared(sharedPermissions, 'ws.revoke.restore');
    await read(worker, 'ws.revoke.http-same-session-restored');
  }
  skip('ws.partial-resource-filter', '只允许 RBAC 配置变更，当前没有合法业务写入可触发应用/运行/发布消息；不把静默窗口当过滤通过');
}
async function verify() {
  await validateOwnership();
  await resetOwnState('prepare');
  worker = new Client('worker', credentials.worker); peer = new Client('peer', credentials.peer); clients.push(worker, peer);
  await worker.login(); await peer.login();
  await roleCases(); await overrideCases(); await membershipCases(); await departmentCases(); await accountCases(); await projectCases(); await websocketCases();
}
try {
  const identity = await admin.login();
  if (!identity.is_superuser) throw new Error('专项管理员必须具有独立恢复权限');
  requireResult('instance.ready', await admin.request('GET', '/health/ready'), 200, (data) => data && typeof data === 'object');
  const info = requireResult('instance.info', await admin.request('GET', '/system/info'), 200, (data) => data && typeof data === 'object');
  if (typeof info.data.version === 'string' && /^[a-zA-Z0-9_.+() -]{1,100}$/.test(info.data.version)) report.instanceVersion = info.data.version;
  report.baseline = '本次请求实测的当前本地 API；不等同于源码修复已重载';
  if (phase === 'all') await seed();
  await verify();
} catch {
  record('runner.completed', false, { reason: '执行中止；不输出原始异常、请求响应或凭据。检查已记录的安全状态码和私有清单。' });
  process.exitCode = 1;
} finally {
  if (manifest?.seeded && ownershipConfirmed && admin.cookies.size) {
    try { await resetOwnState('final-restore'); await snapshotOwnState(); } catch { record('final-restore.completed', false, { reason: '专项状态未完全恢复，请仅按本轮 UUID 人工核查' }); }
  }
  for (const client of [admin, ...clients]) {
    try { if (client.cookies.size) await client.request('POST', '/auth/logout'); } catch { /* 退出失败不隐藏测试结果。 */ }
    client.cookies.clear();
  }
  report.finishedAt = new Date().toISOString();
  report.resources = manifest?.created || [];
  report.summary = Object.fromEntries(['通过', '失败', '前置未满足'].map((status) => [status, report.cases.filter((item) => item.status === status).length]));
  await saveJSON(join(out, `report-${phase}-${Date.now()}.json`), report);
  if (manifest) await checkpoint();
  console.log(`撤权验收完成：通过 ${report.summary['通过']}，失败 ${report.summary['失败']}，前置未满足 ${report.summary['前置未满足']}。`);
  if (report.summary['失败']) process.exitCode = 1;
}
