#!/usr/bin/env node
import { randomBytes } from 'node:crypto';
import { chmod, lstat, mkdir, readFile, realpath, rename, writeFile } from 'node:fs/promises';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { listCases, platformListCases, resolveRoles } from './roles.mjs';

// 请求和响应内容只留在内存；证据输出由白名单字段单独构造。
const args = process.argv.slice(2);
const options = {};
for (let index = 0; index < args.length; index++) {
  if (args[index] === '--help') { usage(); process.exit(0); }
  if (!['--admin-file', '--out', '--phase', '--base-url'].includes(args[index]) || !args[index + 1]) {
    throw new Error('参数无效，请使用 --help 查看说明');
  }
  options[args[index].slice(2)] = args[++index];
}
function usage() {
  console.log('node tools/rbac-acceptance/run.mjs --admin-file /私有目录/admin.json --out /私有目录/本轮 --phase seed|matrix|all [--base-url http://127.0.0.1:18180]');
}

const base = new URL(options['base-url'] || 'http://127.0.0.1:18180');
if (!['127.0.0.1', '[::1]'].includes(base.hostname) || !['http:', 'https:'].includes(base.protocol) || base.username || base.password || base.pathname !== '/' || base.search || base.hash) {
  throw new Error('验收目标必须是明确的 loopback IP 根地址');
}
const phase = options.phase || 'all';
if (!['seed', 'matrix', 'all'].includes(phase) || !options['admin-file'] || !options.out) {
  usage(); process.exit(2);
}
const out = resolve(options.out);
const adminFile = resolve(options['admin-file']);
const privateMode = (stat) => (stat.mode & 0o077) === 0;
async function ensurePrivateDirectory(path, create = false) {
  if (create) await mkdir(path, { recursive: true, mode: 0o700 });
  const stat = await lstat(path);
  if (!stat.isDirectory() || stat.isSymbolicLink() || !privateMode(stat)) throw new Error('验收目录必须是私有的真实目录，权限应为 700');
  if (process.getuid && stat.uid !== process.getuid()) throw new Error('验收目录必须属于当前用户');
}
async function privateJSON(path) {
  const stat = await lstat(path);
  if (!stat.isFile() || stat.isSymbolicLink() || !privateMode(stat)) throw new Error('凭据或清单文件必须是私有普通文件，权限应为 600');
  if (process.getuid && stat.uid !== process.getuid()) throw new Error('凭据或清单文件必须属于当前用户');
  const content = await readFile(path, 'utf8');
  try { return JSON.parse(content); } catch { throw new Error('私有 JSON 文件格式无效；不输出内容'); }
}
async function saveJSON(path, data) {
  const temporary = `${path}.${randomBytes(4).toString('hex')}.tmp`;
  await writeFile(temporary, `${JSON.stringify(data, null, 2)}\n`, { mode: 0o600, flag: 'wx' });
  await chmod(temporary, 0o600);
  await rename(temporary, path);
}
await ensurePrivateDirectory(dirname(adminFile));
await ensurePrivateDirectory(out, true);
const sourceRoot = resolve(dirname(fileURLToPath(import.meta.url)), '../..');
const actualOut = await realpath(out);
if (actualOut === sourceRoot || actualOut.startsWith(`${sourceRoot}/`)) throw new Error('私有清单和报告不能写入源码目录');
const adminCredential = await privateJSON(adminFile);
if (typeof adminCredential.username !== 'string' || typeof adminCredential.password !== 'string' || !adminCredential.password) throw new Error('管理员文件格式无效');

const manifestPath = join(out, 'manifest.json');
let manifest;
try { manifest = await privateJSON(manifestPath); } catch (error) {
  if (error.code !== 'ENOENT') throw error;
}
if (manifest && manifest.baseURL !== base.origin) throw new Error('清单与目标实例不匹配');
if (manifest && (phase === 'seed' || phase === 'all')) throw new Error('本轮已存在；重跑矩阵请使用 --phase matrix，另起一轮请使用新目录');
if (!manifest && phase === 'matrix') throw new Error('没有本轮清单，请先执行 seed');
const report = { schema: 1, baseURL: base.origin, startedAt: new Date().toISOString(), phase, cases: [], limitations: [
  '首版只执行 seed 和真实 HTTP 基础权限矩阵，不运行构建、发布、SQL、Kubernetes、SSH、终端及文件传输。',
  '没有凭无效对象的 404 断言实际外部执行成功；所有写入目标均为本轮专属对象。',
  '当前应用创建 API 永远启用新应用，空工作流仍会轮询仓库；因此禁止本脚本创建应用，相关正向及关联场景记为前置未满足。',
] };
const safeCode = (value) => typeof value === 'string' && /^[a-zA-Z0-9_.:-]{1,100}$/.test(value) ? value : undefined;
function record(id, passed, details = {}) {
  report.cases.push({ id, status: passed ? '通过' : '失败', at: new Date().toISOString(), ...details });
  console.log(`${passed ? 'PASS' : 'FAIL'} ${id}`);
}
function skip(id, reason = '种子或动作前置未满足') { report.cases.push({ id, status: '前置未满足', reason }); }
async function checkpoint() { await saveJSON(manifestPath, manifest); }

class Client {
  constructor(label, credential) { this.label = label; this.credential = credential; this.cookies = new Map(); this.user = null; }
  async request(method, path, { project, body, queryProject } = {}) {
    if (!path.startsWith('/') || path.startsWith('//')) throw new Error('请求路径无效');
    // 新应用默认立即参与后台轮询，空工作流也会拉取 Git。不存在原子停用创建入口时禁止创建。
    if (method === 'POST' && path === '/applications') throw new Error('应用创建不能保证关闭后台轮询，安全前置未满足');
    const url = new URL(`/api/v1${path}`, base);
    if (url.origin !== base.origin) throw new Error('请求越过验收实例');
    if (queryProject) url.searchParams.set('project_id', queryProject);
    const headers = { Accept: 'application/json' };
    if (body !== undefined) headers['Content-Type'] = 'application/json';
    if (project) headers['X-EDO-Project-ID'] = project;
    if (this.cookies.size) headers.Cookie = [...this.cookies].map(([key, value]) => `${key}=${value}`).join('; ');
    const response = await fetch(url, { method, headers, body: body === undefined ? undefined : JSON.stringify(body), redirect: 'manual', signal: AbortSignal.timeout(30000) });
    if (response.status >= 300 && response.status < 400) throw new Error('验收接口返回重定向，已停止');
    const setCookies = response.headers.getSetCookie?.() || (response.headers.get('set-cookie') ? [response.headers.get('set-cookie')] : []);
    for (const item of setCookies) {
      const first = item.split(';', 1)[0];
      const equals = first.indexOf('=');
      if (equals > 0) this.cookies.set(first.slice(0, equals), first.slice(equals + 1));
    }
    const raw = await response.text();
    let data = null;
    if (raw) { try { data = JSON.parse(raw); } catch { /* HTML 或无效响应不能冒充 API 成功。 */ } }
    if (manifest && ['POST', 'PUT', 'PATCH'].includes(method) && !path.startsWith('/auth/')) {
      for (const kind of ['department', 'project', 'role', 'user', 'repository', 'application', 'environment', 'build_plan', 'variable', 'policy', 'credential']) {
        const resourceID = data?.[kind]?.id;
        if (uuid(resourceID) && !manifest.created.some((item) => item.kind === kind && item.id === resourceID)) {
          manifest.created.push({ kind, id: resourceID, project });
        }
      }
      await checkpoint();
    }
    return { status: response.status, data, code: safeCode(data?.code), noStore: response.headers.get('cache-control')?.includes('no-store') || false };
  }
  async login() {
    const result = await this.request('POST', '/auth/login', { body: this.credential });
    const passed = result.status === 200 && result.data?.user?.id && this.cookies.size > 0;
    record(`login.${this.label}`, Boolean(passed), { actual: result.status, code: result.code });
    if (!passed) throw new Error(`账号 ${this.label} 登录失败，详情仅保留安全状态码`);
    this.user = result.data.user;
  }
}
const admin = new Client('admin', adminCredential);
const clients = new Map();
let roles = [];
let catalog = [];
let serial = 0;
const uuid = (value) => typeof value === 'string' && /^[0-9a-f]{8}-[0-9a-f-]{27}$/i.test(value);
const idOf = (data, key) => uuid(data?.[key]?.id) ? data[key].id : null;
function expect(id, result, statuses, predicate = () => result.status !== 200 || (result.data !== null && typeof result.data === 'object'), extra = {}) {
  const expected = Array.isArray(statuses) ? statuses : [statuses];
  const passed = expected.includes(result.status) && predicate(result.data);
  record(id, passed, { expected, actual: result.status, code: result.code, ...extra });
  return passed;
}
async function create(client, id, path, body, key, project) {
  const result = await client.request('POST', path, { project, body });
  const resourceID = idOf(result.data, key);
  expect(id, result, [200, 201], () => Boolean(resourceID));
  if (resourceID) {
    if (!manifest.created.some((item) => item.kind === key && item.id === resourceID)) manifest.created.push({ kind: key, id: resourceID, project: project || undefined });
    await checkpoint();
  }
  return resourceID;
}
const ownName = (label) => `${manifest.prefix}-${label}`;
const repositoryBody = (label) => ({ name: ownName(label), provider: 'generic', clone_url: `https://example.invalid/${manifest.prefix}/${label}.git`, default_branch: 'main', auth_type: 'none', webhook_enabled: false });
const environmentBody = (label) => ({ name: ownName(label), description: manifest.prefix, host_ids: [] });
const variableBody = (name, value = randomBytes(16).toString('hex'), isSecret = false) => ({ name, description: manifest.prefix, kind: 'env', usage: 'build', value, is_secret: isSecret, is_active: true, protected_refs: [] });

async function seed() {
  const runID = `${new Date().toISOString().replace(/[-:TZ.]/g, '').slice(0, 14)}-${randomBytes(3).toString('hex')}`;
  manifest = { schema: 1, runID, prefix: `rbac-${runID}`, baseURL: base.origin, createdAt: new Date().toISOString(), departments: {}, projects: {}, roles: {}, users: {}, resources: {}, created: [], seedComplete: false };
  report.runID = runID;
  await checkpoint();
  for (const key of ['D1', 'D2']) {
    manifest.departments[key] = await create(admin, `seed.department.${key}`, '/departments', { name: ownName(key), description: manifest.prefix }, 'department');
    await checkpoint();
  }
  for (const [key, department] of [['A', 'D1'], ['B', 'D1'], ['C', 'D2']]) {
    if (!manifest.departments[department]) { skip(`seed.project.${key}`); continue; }
    manifest.projects[key] = await create(admin, `seed.project.${key}`, '/projects', { name: ownName(key), description: manifest.prefix, department_id: manifest.departments[department], is_active: true }, 'project');
    await checkpoint();
  }
  for (const role of roles) {
    if (role.missing.length || role.wrongScope.length) {
      record(`catalog.role.${role.key}`, false, { missingPermissions: role.missing, wrongScope: role.wrongScope }); continue;
    }
    const roleID = await create(admin, `seed.role.${role.key}`, '/roles', { name: ownName(role.key), display_name: ownName(role.label), description: manifest.prefix, permissions: role.permissions }, 'role');
    manifest.roles[role.key] = { id: roleID, scope: role.scope, permissions: role.permissions };
    await checkpoint();
  }
  const projectPermissions = catalog.filter((item) => item.scope === 'project').map((item) => item.code);
  const complete = await admin.request('POST', '/roles', { body: { name: ownName('complete-project'), display_name: ownName('完整项目权限'), description: manifest.prefix, permissions: projectPermissions } });
  expect('role.complete-project-no-truncation', complete, 201, () => Boolean(idOf(complete.data, 'role')), { permissionCount: projectPermissions.length });
  if (idOf(complete.data, 'role')) {
    manifest.created.push({ kind: 'role', id: complete.data.role.id });
    manifest.completeProjectRole = complete.data.role.id;
    await checkpoint();
  }
  const identities = [
    ...roles.map((role) => ({ key: role.key, role: role.key })),
    { key: 'approver_b', role: 'approver' }, { key: 'credential_other', role: 'credential_owner' },
    { key: 'nonmember' }, { key: 'disabled' }, { key: 'foreign', department: 'D2' },
    { key: 'builtin_developer' }, { key: 'delegated' },
  ];
  for (const identity of identities) {
    const role = manifest.roles[identity.role];
    if (identity.role && !role?.id) { skip(`seed.user.${identity.key}`); continue; }
    const username = `rb${Date.now().toString(36)}${randomBytes(2).toString('hex')}_${(++serial).toString(36)}`;
    const password = `${randomBytes(24).toString('base64url')}!a9`;
    const departmentID = manifest.departments[identity.department || 'D1'];
    if (!departmentID) { skip(`seed.user.${identity.key}`); continue; }
    const userID = await create(admin, `seed.user.${identity.key}`, '/users', {
      username, password, nickname: ownName(identity.key), department_id: departmentID,
      role_ids: role?.scope === 'platform' ? [role.id] : [],
    }, 'user');
    if (userID) {
      manifest.users[identity.key] = { id: userID, username, password, role: identity.role || null, department: identity.department || 'D1', memberships: {} };
      await checkpoint();
      if (role?.scope === 'project' && manifest.projects.A) await member(identity.key, 'A', role.id, admin, `seed.member.${identity.key}.A`);
    }
  }
  if (manifest.users.developer && manifest.roles.viewer?.id && manifest.projects.B) await member('developer', 'B', manifest.roles.viewer.id, admin, 'seed.member.developer.B');
  if (manifest.users.foreign && manifest.roles.viewer?.id && manifest.projects.C) await member('foreign', 'C', manifest.roles.viewer.id, admin, 'seed.member.foreign.C');
  for (const key of ['credential_owner', 'credential_other']) {
    if (manifest.users[key] && manifest.roles.developer?.id && manifest.projects.A) await member(key, 'A', manifest.roles.developer.id, admin, `seed.member.${key}.A`);
  }
  const builtin = await admin.request('GET', '/roles');
  const developer = builtin.data?.roles?.find((item) => item.name === 'edo_project_developer');
  if (uuid(developer?.id)) {
    manifest.builtinDeveloperRole = { id: developer.id, permissions: Array.isArray(developer.permissions) ? developer.permissions : [] };
    if (manifest.users.builtin_developer && manifest.projects.A) await member('builtin_developer', 'A', developer.id, admin, 'seed.member.builtin_developer.A');
  } else skip('seed.builtin-developer', '当前服务未返回内置开发角色');
  if (manifest.users.disabled) expect('seed.user.disabled', await admin.request('PATCH', `/users/${manifest.users.disabled.id}/status`, { body: { active: false } }), 204);
  for (const key of ['A', 'B', 'C']) {
    const project = manifest.projects[key];
    if (!project) continue;
    const resource = {};
    resource.repository = await create(admin, `seed.${key}.repository`, '/repositories', repositoryBody('source'), 'repository', project);
    resource.application = null;
    skip(`seed.${key}.application`, '应用创建 API 不支持原子停用；空工作流仍会轮询 Git，当前不创建');
    resource.environment = await create(admin, `seed.${key}.environment`, '/environments', environmentBody('environment'), 'environment', project);
    resource.buildPlan = await create(admin, `seed.${key}.build-plan`, '/build-plans', { name: ownName('build'), kind: 'script', description: manifest.prefix, script: 'printf rbac-acceptance', working_directory: '.', artifact_path: 'dist', timeout_seconds: 30 }, 'build_plan', project);
    resource.variable = await create(admin, `seed.${key}.variable`, '/delivery-variables', variableBody('RBAC_ACCEPTANCE_VALUE'), 'variable', project);
    resource.secret = await create(admin, `seed.${key}.secret`, '/delivery-variables', variableBody('RBAC_ACCEPTANCE_SECRET', randomBytes(24).toString('hex'), true), 'variable', project);
    manifest.resources[key] = resource;
    await checkpoint();
  }
  manifest.seedComplete = true;
  await checkpoint();
}
async function member(userKey, projectKey, roleID, actor, label) {
  const user = manifest.users[userKey], project = manifest.projects[projectKey];
  const result = await actor.request('PUT', `/projects/${project}/members/${user.id}`, { body: { role_id: roleID } });
  if (expect(label, result, 204)) { user.memberships[projectKey] = roleID; await checkpoint(); }
}
async function getClient(key) {
  if (clients.has(key)) return clients.get(key);
  const user = manifest.users[key];
  if (!user) return null;
  const client = new Client(key, { username: user.username, password: user.password });
  await client.login(); clients.set(key, client); return client;
}
function expectedPermissions(user, projectKey) {
  const platform = manifest.roles[user.role]?.scope === 'platform' ? manifest.roles[user.role].permissions : [];
  const roleID = user.memberships[projectKey];
  const projectRole = Object.values(manifest.roles).find((item) => item.id === roleID);
  const projectCodes = projectRole?.permissions || (roleID === manifest.builtinDeveloperRole?.id ? manifest.builtinDeveloperRole.permissions : []);
  return [...new Set([...platform, ...projectCodes])].sort();
}
async function matrix() {
  report.runID = manifest.runID;
  const project = manifest.projects.A;
  if (!manifest.seedComplete || !project) throw new Error('种子未完整完成，请使用新目录重新 seed');
  // 管理员先证明路径和必要参数可用，404/400 不能被当作任何角色的正常鉴权结果。
  for (const [path] of listCases) expect(`route-baseline.project.${path.slice(1)}`, await admin.request('GET', path, { project }), 200);
  for (const [path] of platformListCases) expect(`route-baseline.platform.${path.slice(1).replaceAll('/', '.')}`, await admin.request('GET', path), 200);
  for (const [key, user] of Object.entries(manifest.users)) {
    if (key === 'disabled') {
      const result = await new Client('disabled', { username: user.username, password: user.password }).request('POST', '/auth/login', { body: { username: user.username, password: user.password } });
      expect('disabled.login-denied', result, [401, 403]); continue;
    }
    const client = await getClient(key);
    const joined = Boolean(user.memberships.A);
    const currentPermissions = expectedPermissions(user, 'A');
    const me = await client.request('GET', '/auth/me', { project });
    expect(`me.${key}.A`, me, joined ? 200 : 403, (data) => !joined || (data?.user?.project_id === project && JSON.stringify([...(data?.user?.permissions || [])].sort()) === JSON.stringify(currentPermissions)));
    const list = await client.request('GET', '/projects');
    expect(`projects.${key}.isolation`, list, 200, (data) => {
      const ids = (data?.projects || []).map((item) => item.id);
      return Object.entries(manifest.projects).every(([projectKey, id]) => ids.includes(id) === Boolean(user.memberships[projectKey]));
    });
    for (const [path, required] of listCases) {
      const allowed = (Array.isArray(required) ? required : [required]).some((code) => currentPermissions.includes(code));
      expect(`list.${key}.${path.slice(1)}`, await client.request('GET', path, { project }), joined && allowed ? 200 : 403);
    }
    for (const [path, required] of platformListCases) {
      const allowed = (Array.isArray(required) ? required : [required]).some((code) => currentPermissions.includes(code));
      expect(`platform-list.${key}.${path.slice(1).replaceAll('/', '.')}`, await client.request('GET', path), allowed ? 200 : 403);
    }
  }
  const developer = await getClient('developer'), viewer = await getClient('viewer'), projectAdmin = await getClient('project_admin');
  const variableAdmin = await getClient('variable_admin');
  const A = manifest.resources.A, B = manifest.resources.B;
  if (developer) {
    expect('scope.project-required', await developer.request('GET', '/applications'), 400);
    expect('scope.other-department', await developer.request('GET', '/applications', { project: manifest.projects.C }), 403);
    expect('scope.A-developer-B-viewer.me', await developer.request('GET', '/auth/me', { project: manifest.projects.B }), 200, (data) => JSON.stringify([...(data?.user?.permissions || [])].sort()) === JSON.stringify(expectedPermissions(manifest.users.developer, 'B')));
    expect('scope.B-viewer-repository-create-denied', await developer.request('POST', '/repositories', { project: manifest.projects.B, body: repositoryBody(`forbidden-${randomBytes(3).toString('hex')}`) }), 403);
    if (A?.environment && B?.environment) {
      expect('scope.header-wins-query-list', await developer.request('GET', '/environments', { project, queryProject: manifest.projects.B }), 200, (data) => (data?.environments || []).some((item) => item.id === A.environment) && !(data?.environments || []).some((item) => item.id === B.environment));
      expect('scope.A-read-B-object', await developer.request('GET', `/environments/${B.environment}`, { project }), 404);
    }
    skip('scope.foreign-repository-association', '应用创建 API 不支持原子停用，避免意外允许后触发后台 Git');
    const repositoryID = await create(developer, 'developer.create-own-repository', '/repositories', repositoryBody(`developer-${randomBytes(3).toString('hex')}`), 'repository', project);
    if (repositoryID) skip('developer.create-own-application', '应用创建 API 不支持原子停用，等待独立无调度入口');
  }
  if (viewer) {
    skip('viewer.application-update-denied', '没有安全创建的本轮应用目标');
    expect('viewer.repository-create-denied', await viewer.request('POST', '/repositories', { project, body: repositoryBody(`denied-${randomBytes(3).toString('hex')}`) }), 403);
    if (A?.environment) expect('viewer.environment-update-denied', await viewer.request('PATCH', `/environments/${A.environment}`, { project, body: { name: ownName('environment'), description: manifest.prefix } }), 403);
  }
  if (projectAdmin) await delegation(projectAdmin);
  if (variableAdmin) await variableCases(variableAdmin, developer, viewer);
  await credentialCases();
  await approvalCases(projectAdmin, developer);
  const builtin = await getClient('builtin_developer');
  if (builtin) {
    const me = await builtin.request('GET', '/auth/me', { project });
    const broad = ['environment_policy.update', 'deployment.create', 'deployment.update', 'cluster.create', 'cluster.update'].filter((code) => me.data?.user?.permissions?.includes(code));
    record('builtin-developer-least-privilege', broad.length === 0, { actual: me.status, excessivePermissions: broad });
  }
  if (projectAdmin) {
    const result = await projectAdmin.request('POST', '/roles', { project, body: { name: ownName(`project-role-${randomBytes(3).toString('hex')}`), display_name: ownName('越权平台角色'), description: manifest.prefix, permissions: ['config.update'] } });
    expect('project-admin-cannot-create-platform-role', result, 403);
  }
}
async function delegation(actor) {
  const project = manifest.projects.A, actorID = manifest.users.project_admin.id;
  const send = (userID, roleID) => actor.request('PUT', `/projects/${project}/members/${userID}`, { project, body: { role_id: roleID } });
  if (manifest.users.delegated && manifest.roles.viewer?.id) {
    const result = await send(manifest.users.delegated.id, manifest.roles.viewer.id);
    if (expect('delegation.allowed-subset', result, 204)) { manifest.users.delegated.memberships.A = manifest.roles.viewer.id; await checkpoint(); }
  }
  expect('delegation.self-change-denied', await send(actorID, manifest.roles.viewer.id), 403);
  expect('delegation.platform-role-denied', await send(manifest.users.delegated.id, manifest.roles.platform_admin.id), [400, 403]);
  expect('delegation.exceeds-actor-denied', await send(manifest.users.delegated.id, manifest.roles.terminal_operator.id), 403);
  expect('delegation.foreign-department-denied', await send(manifest.users.foreign.id, manifest.roles.viewer.id), 400);
  expect('delegation.platform-user-update-denied', await actor.request('PUT', `/users/${manifest.users.delegated.id}/roles`, { body: { role_ids: [manifest.roles.platform_admin.id] } }), 403);
  const platform = await getClient('platform_admin');
  if (platform) {
    expect('delegation.platform-self-access-denied', await platform.request('PUT', `/users/${manifest.users.platform_admin.id}/access`, { body: { role_ids: [manifest.roles.platform_admin.id], allow: [], deny: [] } }), 403);
    expect('delegation.platform-cross-department-denied', await platform.request('PUT', `/users/${manifest.users.delegated.id}/department`, { body: { department_id: manifest.departments.D2 } }), 403);
    expect('delegation.platform-cannot-grant-project-execute', await platform.request('POST', '/roles', { body: { name: ownName(`forbidden-role-${randomBytes(3).toString('hex')}`), display_name: ownName('越权执行角色'), description: manifest.prefix, permissions: ['pipeline_run.execute'] } }), 403);
  }
}
async function variableCases(actor, developer, viewer) {
  const project = manifest.projects.A, suffix = randomBytes(4).toString('hex').toUpperCase();
  const name = `RBAC_MUTABLE_${suffix}`;
  const value = randomBytes(24).toString('hex');
  const input = variableBody(name, value);
  const result = await actor.request('POST', '/delivery-variables', { project, body: input });
  const id = idOf(result.data, 'variable');
  expect('variable.admin-create', result, 200, (data) => Boolean(id) && data.variable.project_id === project);
  if (!id) return;
  manifest.created.push({ kind: 'variable', id, project }); await checkpoint();
  const version = result.data.variable.version;
  const changed = await actor.request('PUT', `/delivery-variables/${id}`, { project, body: { ...input, value: randomBytes(24).toString('hex'), expected_version: version } });
  expect('variable.admin-update', changed, 200);
  expect('variable.stale-version-denied', await actor.request('PUT', `/delivery-variables/${id}`, { project, body: { ...input, expected_version: version } }), 409);
  if (developer) expect('variable.developer-write-denied', await developer.request('PUT', `/delivery-variables/${id}`, { project, body: { ...input, expected_version: changed.data?.variable?.version } }), 403);
  if (viewer) expect('variable.viewer-delete-denied', await viewer.request('DELETE', `/delivery-variables/${id}?version=${changed.data?.variable?.version}`, { project }), 403);
  if (manifest.resources.B?.variable) expect('variable.cross-project-update-denied', await actor.request('PUT', `/delivery-variables/${manifest.resources.B.variable}`, { project, body: { ...input, expected_version: 1 } }), [400, 404]);
  if (manifest.resources.B?.application) expect('variable.foreign-application-denied', await actor.request('POST', '/delivery-variables', { project, body: { ...input, name: `RBAC_FOREIGN_${suffix}`, application_id: manifest.resources.B.application } }), 400);
  else skip('variable.foreign-application-denied', '没有安全创建的本轮应用目标');
  if (manifest.resources.B?.environment) expect('variable.foreign-environment-denied', await actor.request('POST', '/delivery-variables', { project, body: { ...input, name: `RBAC_FOREIGN_ENV_${suffix}`, environment_id: manifest.resources.B.environment } }), 400);
  const secretValue = randomBytes(32).toString('hex');
  const secret = await actor.request('POST', '/delivery-variables', { project, body: variableBody(`RBAC_SECRET_${suffix}`, secretValue, true) });
  const secretID = idOf(secret.data, 'variable');
  expect('variable.secret-create-no-plaintext', secret, 200, (data) => Boolean(secretID) && !JSON.stringify(data).includes(secretValue) && !Object.hasOwn(data.variable, 'value'));
  if (secretID) { manifest.created.push({ kind: 'variable', id: secretID, project }); await checkpoint(); }
  for (const client of [actor, developer, viewer].filter(Boolean)) {
    const listed = await client.request('GET', '/delivery-variables', { project });
    expect(`variable.${client.label}.secret-metadata`, listed, 200, (data) => {
      const item = data?.variables?.find((entry) => entry.id === secretID);
      return listed.noStore && Boolean(item?.is_secret) && !Object.hasOwn(item, 'value') && !JSON.stringify(data).includes(secretValue);
    });
  }
  expect('variable.admin-delete', await actor.request('DELETE', `/delivery-variables/${id}?version=${changed.data?.variable?.version}`, { project }), 204);
  const after = await actor.request('GET', '/delivery-variables', { project });
  expect('variable.delete-confirmed', after, 200, (data) => !(data?.variables || []).some((item) => item.id === id));
}
async function approvalCases(actor, developer) {
  const environment = manifest.resources.A?.environment, project = manifest.projects.A;
  if (!actor || !environment || !manifest.users.tester) { skip('environment-policy.no-permission-reviewer'); return; }
  const reviewer = await actor.request('GET', '/environment-policies/reviewers', { project });
  const choices = reviewer.data?.reviewers || reviewer.data?.users || [];
  const hasUnqualified = choices.some((item) => item.id === manifest.users.tester.id);
  expect('environment-policy.reviewers-exclude-no-review-permission', reviewer, 200, () => !hasUnqualified);
  const existing = await actor.request('GET', '/environment-policies', { project });
  const previous = existing.data?.policies?.find((item) => item.environment_id === environment);
  const invalid = { allowed_refs: ['refs/heads/main'], reviewer_ids: [manifest.users.tester.id], require_approval: true, minimum_approvals: 1, window_start: '', window_end: '', is_active: true, expected_version: previous?.version || 0 };
  const response = await actor.request('PUT', `/environment-policies/${environment}`, { project, body: invalid });
  expect('environment-policy.no-permission-reviewer-rejected', response, 400);
  const policy = response.data?.policy;
  const expectedVersion = policy?.version || previous?.version || 0;
  const valid = { ...invalid, reviewer_ids: [manifest.users.approver.id, manifest.users.approver_b.id], minimum_approvals: 2, expected_version: expectedVersion };
  const saved = await actor.request('PUT', `/environment-policies/${environment}`, { project, body: valid });
  expect('environment-policy.valid-two-reviewers', saved, 200);
  if (uuid(saved.data?.policy?.id)) { manifest.created.push({ kind: 'environment_policy', id: saved.data.policy.id, project }); await checkpoint(); }
  if (developer) expect('environment-policy.developer-cannot-disable', await developer.request('PUT', `/environment-policies/${environment}`, { project, body: { ...valid, is_active: false, expected_version: saved.data?.policy?.version || 0 } }), 403);
}

async function credentialCases() {
  const owner = await getClient('credential_owner'), other = await getClient('credential_other');
  if (!owner || !other) { skip('credential.personal-ownership'); return; }
  const suffix = randomBytes(4).toString('hex');
  const secret = randomBytes(32).toString('base64url');
  const input = { name: ownName(`personal-${suffix}`), provider: 'generic', auth_type: 'token', username: 'rbac-test', secret };
  const response = await owner.request('POST', '/git-credentials', { body: input });
  const id = idOf(response.data, 'credential');
  expect('credential.owner-create', response, 201, (data) => Boolean(id) && !JSON.stringify(data).includes(secret));
  if (!id) return;
  const listed = await owner.request('GET', '/git-credentials');
  expect('credential.owner-list-metadata', listed, 200, (data) => data?.credentials?.some((item) => item.id === id) && !JSON.stringify(data).includes(secret));
  expect('credential.other-list-isolated', await other.request('GET', '/git-credentials'), 200, (data) => !data?.credentials?.some((item) => item.id === id));
  expect('credential.owner-reveal-own-test-value', await owner.request('GET', `/git-credentials/${id}/secret`), 200, (data) => data?.secret === secret);
  expect('credential.other-reveal-denied', await other.request('GET', `/git-credentials/${id}/secret`), 404);
  expect('credential.other-update-denied', await other.request('PUT', `/git-credentials/${id}`, { body: { ...input, secret: randomBytes(32).toString('base64url') } }), 404);
  expect('credential.other-delete-denied', await other.request('DELETE', `/git-credentials/${id}`), 404);
  const body = { ...repositoryBody(`foreign-credential-${suffix}`), auth_type: 'token', username: 'rbac-test', credential_id: id };
  expect('credential.foreign-repository-binding-denied', await other.request('POST', '/repositories', { project: manifest.projects.A, body }), [400, 404]);
  expect('credential.owner-delete-test-value', await owner.request('DELETE', `/git-credentials/${id}`), 204);
}

try {
  await admin.login();
  if (!admin.user.is_superuser) throw new Error('种子管理员必须是本轮专用恢复管理员');
  const health = await admin.request('GET', '/health/ready');
  if (!expect('instance.ready', health, 200)) throw new Error('独立验收实例尚未就绪');
  const information = await admin.request('GET', '/system/info');
  if (typeof information.data?.version === 'string' && /^[a-zA-Z0-9_.+-]{1,80}$/.test(information.data.version)) report.instanceVersion = information.data.version;
  const permissions = await admin.request('GET', '/permissions');
  if (!expect('catalog.loaded-with-scope', permissions, 200, (data) => Array.isArray(data?.permissions) && data.permissions.length > 0 && data.permissions.every((item) => ['platform', 'project'].includes(item.scope)))) throw new Error('权限目录缺少明确范围');
  catalog = permissions.data.permissions;
  roles = resolveRoles(catalog);
  report.catalog = catalog.map(({ code, scope }) => ({ code, scope }));
  if (phase === 'seed' || phase === 'all') await seed();
  if (phase === 'matrix' || phase === 'all') await matrix();
} catch (error) {
  // 不输出异常对象、请求/响应、凭据或 URL 中的查询值。
  record('runner.completed', false, { reason: error?.name === 'TimeoutError' ? '请求超时，未自动重试写操作' : '执行中止，请核对脱敏报告与私有清单；不输出原始异常以保护凭据' });
  process.exitCode = 1;
} finally {
  report.finishedAt = new Date().toISOString();
  report.summary = Object.fromEntries(['通过', '失败', '前置未满足', '未执行'].map((status) => [status, report.cases.filter((item) => item.status === status).length]));
  await saveJSON(join(out, `report-${phase}-${Date.now()}.json`), report);
  if (manifest) await checkpoint();
  for (const client of [admin, ...clients.values()]) {
    try { await client.request('POST', '/auth/logout'); } catch { /* 清理会话失败不包含敏感输出，也不隐藏已有验收失败。 */ }
    client.cookies.clear();
  }
  console.log(`验收完成：通过 ${report.summary['通过']}，失败 ${report.summary['失败']}，前置未满足 ${report.summary['前置未满足']}。`);
  if (report.summary['失败'] > 0) process.exitCode = 1;
}
