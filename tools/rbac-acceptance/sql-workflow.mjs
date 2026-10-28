#!/usr/bin/env node
import { randomBytes, createHash } from 'node:crypto';
import { spawn } from 'node:child_process';
import { chmod, lstat, mkdir, readFile, realpath, rename, writeFile } from 'node:fs/promises';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

// 默认仅生成计划；真实目标与任务只在明确选择相应阶段后创建，不自动重试外部写入。
const options = {}, args = process.argv.slice(2);
for (let index = 0; index < args.length; index++) {
  if (args[index] === '--help') {
    console.log('node tools/rbac-acceptance/sql-workflow.mjs --admin-file /私有/admin.json --source-manifest /私有/http-01/manifest.json --out /私有/sql-01 [--phase prepare|target|submit|recheck|review|execute|verify|cleanup|all] [--application-id UUID --pipeline-run-id UUID --environment-id UUID --branch refs/heads/main]');
    process.exit(0);
  }
  if (!['--admin-file', '--source-manifest', '--out', '--phase', '--base-url', '--application-id', '--pipeline-run-id', '--environment-id', '--branch'].includes(args[index]) || !args[index + 1]) throw new Error('参数无效，请使用 --help');
  options[args[index].slice(2)] = args[++index];
}
let base;
try { base = new URL(options['base-url'] || 'http://127.0.0.1:18180'); } catch { throw new Error('目标地址格式无效'); }
if (!['127.0.0.1', '[::1]'].includes(base.hostname) || !['http:', 'https:'].includes(base.protocol) || base.username || base.password || base.pathname !== '/' || base.search || base.hash) throw new Error('EDO 目标仅允许 loopback IP 根地址');
const phase = options.phase || 'prepare';
if (!['prepare', 'target', 'submit', 'recheck', 'review', 'execute', 'verify', 'cleanup', 'all'].includes(phase) || !options['admin-file'] || !options['source-manifest'] || !options.out) throw new Error('缺少私有凭据、来源清单或输出目录');
const out = resolve(options.out), secretsDir = join(out, 'mysql-secrets');
const uuid = (value) => typeof value === 'string' && /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i.test(value);
const identifier = (value) => typeof value === 'string' && /^[a-z][a-z0-9_]{1,62}$/.test(value);
const safeCode = (value) => typeof value === 'string' && /^[a-zA-Z0-9_.:-]{1,100}$/.test(value) ? value : undefined;
async function privateDirectory(path, create = false) {
  if (create) await mkdir(path, { recursive: true, mode: 0o700 });
  const stat = await lstat(path);
  if (!stat.isDirectory() || stat.isSymbolicLink() || (stat.mode & 0o077) || process.getuid && stat.uid !== process.getuid()) throw new Error('证据目录必须是当前用户所有的 700 私有真实目录');
}
async function privateJSON(path) {
  await privateDirectory(dirname(path));
  const stat = await lstat(path);
  if (!stat.isFile() || stat.isSymbolicLink() || (stat.mode & 0o077) || process.getuid && stat.uid !== process.getuid()) throw new Error('JSON 必须是当前用户所有的 600 私有普通文件');
  try { return JSON.parse(await readFile(path, 'utf8')); } catch { throw new Error('私有 JSON 格式无效，不输出内容'); }
}
async function saveJSON(path, data) {
  const temporary = `${path}.${randomBytes(4).toString('hex')}.tmp`;
  await writeFile(temporary, `${JSON.stringify(data, null, 2)}\n`, { mode: 0o600, flag: 'wx' });
  await chmod(temporary, 0o600); await rename(temporary, path);
}
await privateDirectory(out, true);
const sourceRoot = resolve(dirname(fileURLToPath(import.meta.url)), '../..'), actualOut = await realpath(out);
if (actualOut === sourceRoot || actualOut.startsWith(`${sourceRoot}/`)) throw new Error('私有证据不能写入源码目录');
const source = await privateJSON(resolve(options['source-manifest']));
if (source.baseURL !== base.origin || !uuid(source.projects?.A) || !uuid(source.projects?.B) || !uuid(source.departments?.D1)) throw new Error('来源项目清单与本地实例不匹配');
for (const key of ['sql_requester', 'dba_reviewer', 'dba_executor']) if (!uuid(source.users?.[key]?.id) || !source.users[key].username || !source.users[key].password) throw new Error('来源清单缺少三个真实岗位身份');
const manifestPath = join(out, 'manifest.json'), privateTargetPath = join(out, 'target-credentials.json');
let manifest, targetSecret;
try { manifest = await privateJSON(manifestPath); } catch (error) { if (error.code !== 'ENOENT') throw error; }
if (manifest && (manifest.baseURL !== base.origin || manifest.projectID !== source.projects.A || manifest.sourceRunID !== source.runID || !/^rbac_sql_[a-z0-9_]+$/.test(manifest.prefix))) throw new Error('SQL 专项清单不匹配');
if (!manifest) {
  const suffix = `${new Date().toISOString().replace(/[-:TZ.]/g, '').slice(0, 14)}_${randomBytes(3).toString('hex')}`;
  manifest = { schema: 1, baseURL: base.origin, sourceRunID: source.runID, prefix: `rbac_sql_${suffix}`, projectID: source.projects.A, otherProjectID: source.projects.B, departmentID: source.departments.D1,
    applicationID: null, pipelineRunID: null, environmentID: source.resources?.A?.environment || null, branch: 'refs/heads/main',
    container: `edo-rbac-sql-${suffix.replaceAll('_', '-')}`, network: `edo-rbac-sql-${suffix.replaceAll('_', '-')}`, image: 'mysql:8.4', checkImage: 'sqlfluff/sqlfluff:4.4.0', apiHost: 'host.docker.internal',
    databaseName: `rbac_sql_${randomBytes(6).toString('hex')}`, tableName: `rbac_sql_evidence_${randomBytes(5).toString('hex')}`, marker: randomBytes(12).toString('hex'), created: [], targetReady: false, roles: {}, users: {} };
}
for (const [flag, key] of [['application-id', 'applicationID'], ['pipeline-run-id', 'pipelineRunID'], ['environment-id', 'environmentID']]) if (options[flag]) {
  if (!uuid(options[flag]) || manifest.databaseID && manifest[key] !== options[flag]) throw new Error('不能更换已登记目标的资源身份');
  manifest[key] = options[flag];
}
if (options.branch) {
  if (!/^refs\/heads\/[a-zA-Z0-9._/-]{1,180}$/.test(options.branch) || manifest.databaseID && manifest.branch !== options.branch) throw new Error('分支格式无效或已冻结');
  manifest.branch = options.branch;
}
if (!identifier(manifest.databaseName) || !identifier(manifest.tableName) || !/^[0-9a-f]{24}$/.test(manifest.marker)) throw new Error('专用数据库或证据表标识无效');
const targetName = manifest.prefix.replace('rbac_sql_', 'edo-rbac-sql-').replaceAll('_', '-');
if (manifest.container !== targetName || manifest.network !== targetName || manifest.image !== 'mysql:8.4' || manifest.checkImage !== 'sqlfluff/sqlfluff:4.4.0' || manifest.containerID && !/^[0-9a-f]{64}$/.test(manifest.containerID) || manifest.networkID && !/^[0-9a-f]{64}$/.test(manifest.networkID)) throw new Error('专属 Docker 目标标识无效');
const report = { schema: 1, baseURL: base.origin, phase, startedAt: new Date().toISOString(), cases: [], limitations: [
  '使用当前真实的“保存并检查→待审核→执行”流程；不声称存在独立草稿保存或提交接口。',
  '应用与流水线运行必须由正常 EDO 流程提供；本脚本不创建应用、伪造运行或直接写 EDO 数据库。',
  '仅 MySQL bootstrap 创建专库、账号和授权；业务建表与写入只通过经过审核的 EDO SQL 工单。',
  '本脚本只记录真实 HTTP、任务状态和专库只读核验，浏览器 UI 验收另行记录。',
] };
function record(id, passed, details = {}) { report.cases.push({ id, status: passed ? '通过' : '失败', at: new Date().toISOString(), ...details }); console.log(`${passed ? 'PASS' : 'FAIL'} ${id}`); }
function skip(id, reason) { report.cases.push({ id, status: '前置未满足', reason }); console.log(`SKIP ${id}`); }
function expect(id, result, statuses, predicate = () => true) {
  const expected = Array.isArray(statuses) ? statuses : [statuses], passed = expected.includes(result.status) && predicate(result.data);
  record(id, passed, { expected, actual: result.status, code: result.code }); return passed;
}
function requireResult(id, result, statuses, predicate) { if (!expect(id, result, statuses, predicate)) throw new Error('必要步骤未满足'); return result.data; }
async function checkpoint() { await saveJSON(manifestPath, manifest); if (targetSecret) await saveJSON(privateTargetPath, targetSecret); }
const own = (kind, id) => uuid(id) && manifest.created.some((item) => item.kind === kind && item.id === id);
function writeAllowed(method, path, body) {
  if (method === 'GET' || ['/auth/login', '/auth/logout'].includes(path)) return;
  if (method === 'POST' && path === '/application-databases' && body?.name === manifest.prefix && body.application_id === manifest.applicationID && body.environment_id === manifest.environmentID && body.host === manifest.apiHost && body.port === manifest.port && body.database === manifest.databaseName) return;
  if (method === 'POST' && path === '/application-databases/test' && body?.name === manifest.prefix && body.database === manifest.databaseName && body.host === manifest.apiHost && body.port === manifest.port) return;
  if (method === 'PUT' && path === `/application-databases/${manifest.databaseID}` && own('database', manifest.databaseID) && body?.name === manifest.prefix && body.database === manifest.databaseName) return;
  if (method === 'POST' && path === '/sql-changes' && body?.name?.startsWith(manifest.prefix) && body.application_id === manifest.applicationID && body.database_id === manifest.databaseID && body.pipeline_run_id === manifest.pipelineRunID) return;
  const sqlAction = path.match(/^\/sql-changes\/([^/]+)\/(check|reviews|execute)$/);
  if (method === 'POST' && sqlAction && own('change', sqlAction[1])) return;
  if (method === 'POST' && path === '/roles' && body?.name?.startsWith(manifest.prefix) && body.description === manifest.prefix) return;
  if (method === 'POST' && path === '/users' && body?.nickname?.startsWith(manifest.prefix)) return;
  const role = path.match(/^\/roles\/([^/]+)\/permissions$/);
  if (method === 'PUT' && role && own('role', role[1])) return;
  const member = path.match(/^\/projects\/([^/]+)\/members\/([^/]+)$/);
  if (method === 'PUT' && member && [manifest.projectID, manifest.otherProjectID].includes(member[1]) && own('user', member[2]) && own('role', body?.role_id)) return;
  throw new Error('写入不属于本轮 SQL 专项白名单');
}
class Client {
  constructor(key, credential, expectedID) { this.key = key; this.credential = credential; this.expectedID = expectedID; this.cookies = new Map(); }
  async request(method, path, { project = manifest.projectID, body } = {}) {
    if (!path.startsWith('/') || path.startsWith('//') || path.includes('?')) throw new Error('API 路径无效');
    writeAllowed(method, path, body);
    const headers = { Accept: 'application/json' }; if (project) headers['X-EDO-Project-ID'] = project;
    if (body !== undefined) headers['Content-Type'] = 'application/json';
    if (this.cookies.size) headers.Cookie = [...this.cookies].map(([key, value]) => `${key}=${value}`).join('; ');
    const response = await fetch(new URL(`/api/v1${path}`, base), { method, headers, body: body === undefined ? undefined : JSON.stringify(body), redirect: 'manual', signal: AbortSignal.timeout(30000) });
    if (response.status >= 300 && response.status < 400) throw new Error('API 重定向，停止验收');
    for (const cookie of response.headers.getSetCookie?.() || []) { const first = cookie.split(';', 1)[0], at = first.indexOf('='); if (at > 0) this.cookies.set(first.slice(0, at), first.slice(at + 1)); }
    let data = null; try { const raw = await response.text(); if (raw) data = JSON.parse(raw); } catch { /* 不输出无效响应正文。 */ }
    return { status: response.status, data, code: safeCode(data?.code) };
  }
  async login() {
    const value = requireResult(`login.${this.key}`, await this.request('POST', '/auth/login', { project: undefined, body: this.credential }), 200, (data) => uuid(data?.user?.id) && (!this.expectedID || data.user.id === this.expectedID) && this.cookies.size > 0);
    return value.user;
  }
}
const clients = [], roles = {};
let admin;
async function initializeClients() {
  const credential = await privateJSON(resolve(options['admin-file']));
  if (!credential.username || !credential.password) throw new Error('管理员凭据格式无效');
  admin = new Client('admin', credential); clients.push(admin);
  if (!(await admin.login()).is_superuser) throw new Error('必须使用专项恢复管理员');
  requireResult('instance.ready', await admin.request('GET', '/health/ready'), 200, (data) => data && typeof data === 'object');
  const info = requireResult('instance.info', await admin.request('GET', '/system/info'), 200, (data) => data && typeof data === 'object');
  if (typeof info.version === 'string' && /^[a-zA-Z0-9_.+() -]{1,100}$/.test(info.version)) report.instanceVersion = info.version;
  for (const key of phase === 'cleanup' ? [] : ['sql_requester', 'dba_reviewer', 'dba_executor']) {
    const user = source.users[key], client = new Client(key, { username: user.username, password: user.password }, user.id);
    clients.push(client); roles[key] = client; await client.login();
    requireResult(`precondition.${key}.read`, await client.request('GET', '/sql-changes'), 200, (data) => Array.isArray(data?.items));
  }
}
const delay = (ms) => new Promise((done) => setTimeout(done, ms));
function runProcess(command, values, input = '', timeout = 30000) {
  return new Promise((done, reject) => {
    const child = spawn(command, values, { stdio: ['pipe', 'pipe', 'pipe'] });
    let output = '', overflow = false;
    const timer = setTimeout(() => child.kill('SIGKILL'), timeout);
    child.stdout.on('data', (value) => { if (output.length + value.length > 1024 * 1024) { overflow = true; child.kill('SIGKILL'); } else output += value; });
    child.stderr.on('data', () => {});
    child.on('error', () => { clearTimeout(timer); reject(new Error('外部验收命令不可用')); });
    child.on('close', (code) => { clearTimeout(timer); done({ code, output: overflow ? '' : output }); });
    child.stdin.on('error', () => {}); child.stdin.end(input);
  });
}
let dockerContextVerified = false;
async function docker(args, input = '', timeout) {
  if (!dockerContextVerified) {
    const current = manifest.dockerContext ? { code: 0, output: manifest.dockerContext } : await runProcess('docker', ['context', 'show']);
    if (current.code !== 0 || typeof current.output !== 'string' || !/^[a-zA-Z0-9_.-]+$/.test(current.output.trim())) throw new Error('无法确认本地 Docker context');
    const endpoint = await runProcess('docker', ['context', 'inspect', current.output.trim(), '--format', '{{.Endpoints.docker.Host}}']);
    if (endpoint.code !== 0 || !endpoint.output.trim().startsWith('unix://')) throw new Error('SQL 专项仅使用本地 Unix Docker endpoint');
    manifest.dockerContext = current.output.trim(); dockerContextVerified = true; await checkpoint();
  }
  return runProcess('docker', ['--context', manifest.dockerContext, ...args], input, timeout);
}
async function mustDocker(id, args, input = '', timeout) {
  const value = await docker(args, input, timeout); record(id, value.code === 0); if (value.code !== 0) throw new Error('独立 Docker 目标前置失败'); return value.output.trim();
}
async function mysql(config, query, id = 'mysql.read', timeout = 15000) {
  return mustDocker(id, ['exec', '-i', manifest.container, 'mysql', `--defaults-extra-file=/run/edo-rbac-secrets/${config}.cnf`, '--batch', '--raw', '--skip-column-names'], query, timeout);
}
async function containerIdentity({ allowStopped = false, allowAbsent = false } = {}) {
  const result = await docker(['inspect', '--format', '{{.Id}}\t{{index .Config.Labels "edo.rbac.run"}}\t{{.State.Running}}', manifest.container]);
  if (result.code !== 0 && allowAbsent) {
    const listed = await docker(['ps', '--all', '--format', '{{.Names}}']);
    if (listed.code === 0 && !listed.output.trim().split('\n').includes(manifest.container)) return null;
  }
  const values = result.output.trim().split('\t');
  if (result.code !== 0 || !/^[0-9a-f]{64}$/.test(values[0]) || manifest.containerID && values[0] !== manifest.containerID || values[1] !== manifest.prefix || !['true', 'false'].includes(values[2]) || !allowStopped && values[2] !== 'true') throw new Error('独立容器 UUID、标签或运行状态不匹配');
  return values[0];
}
async function networkIdentity() {
  const result = await docker(['network', 'inspect', '--format', '{{.Id}}\t{{index .Labels "edo.rbac.run"}}', manifest.network]);
  if (result.code !== 0) {
    const listed = await docker(['network', 'ls', '--format', '{{.Name}}']);
    if (listed.code === 0 && !listed.output.trim().split('\n').includes(manifest.network)) return null;
  }
  const [id, label] = result.output.trim().split('\t');
  if (result.code !== 0 || !/^[0-9a-f]{64}$/.test(id) || manifest.networkID && id !== manifest.networkID || label !== manifest.prefix) throw new Error('独立网络 UUID 或标签不匹配');
  return id;
}
async function containerBaseline() {
  const ids = (await mustDocker('target.baseline-list', ['ps', '--all', '--quiet', '--no-trunc'])).split('\n').filter(Boolean), result = [];
  if (ids.some((id) => !/^[0-9a-f]{64}$/.test(id))) throw new Error('Docker 基线容器标识无效');
  if (!ids.length) return result;
  const values = await mustDocker('target.baseline-identities', ['inspect', '--format', '{{.Id}}\t{{.State.StartedAt}}\t{{.State.Status}}', ...ids]);
  for (const value of values.split('\n')) { const [id, startedAt, status] = value.split('\t'); result.push({ id, startedAt, status }); }
  return result;
}
async function checkContainerBaseline(id) {
  const before = manifest.beforeContainers || [];
  const current = before.length ? await docker(['inspect', '--format', '{{.Id}}\t{{.State.StartedAt}}\t{{.State.Status}}', ...before.map((item) => item.id)]) : { code: 0, output: '' };
  const values = new Map(current.output.trim().split('\n').map((value) => { const [containerID, startedAt, status] = value.split('\t'); return [containerID, { startedAt, status }]; }));
  const unchanged = current.code === 0 && before.every((item) => values.get(item.id)?.startedAt === item.startedAt && values.get(item.id)?.status === item.status);
  record(id, unchanged);
  if (!unchanged) throw new Error('其他容器基线变化，仅保留现场，不修改或恢复其他服务');
}
async function loadTargetSecret() {
  targetSecret = await privateJSON(privateTargetPath);
  if (!['rootPassword', 'runtimePassword', 'verifierPassword'].every((key) => /^[a-zA-Z0-9_-]{40,100}$/.test(targetSecret[key])) || !identifier(targetSecret.runtimeUser) || !identifier(targetSecret.verifierUser)) throw new Error('专用 MySQL 凭据文件不匹配');
}
async function createTarget() {
  if (manifest.targetReady || manifest.databaseID) throw new Error('目标已经创建，不重复创建或替换');
  if (![manifest.applicationID, manifest.pipelineRunID, manifest.environmentID].every(uuid)) throw new Error('缺少正常流程创建的应用、运行或环境 UUID');
  const choices = requireResult('fixture.options-valid', await roles.sql_requester.request('GET', '/sql-changes/options'), 200, (data) => data?.applications?.some((item) => item.id === manifest.applicationID) && data?.pipeline_runs?.some((item) => item.id === manifest.pipelineRunID && item.application_id === manifest.applicationID && item.ref === manifest.branch));
  if (!choices.reviewers.some((item) => item.id === source.users.dba_reviewer.id)) throw new Error('原 DBA 审核人不在当前合法候选中');
  await privateDirectory(secretsDir, true);
  targetSecret = { rootPassword: randomBytes(36).toString('base64url'), runtimePassword: randomBytes(36).toString('base64url'), verifierPassword: randomBytes(36).toString('base64url'), runtimeUser: `rbac_run_${randomBytes(5).toString('hex')}`, verifierUser: `rbac_verify_${randomBytes(5).toString('hex')}`, shadow: {} };
  await checkpoint();
  await writeFile(join(secretsDir, 'mysql.env'), `MYSQL_ROOT_PASSWORD=${targetSecret.rootPassword}\nTZ=Asia/Shanghai\n`, { mode: 0o600, flag: 'wx' });
  for (const [key, user, password] of [['root', 'root', targetSecret.rootPassword], ['verifier', targetSecret.verifierUser, targetSecret.verifierPassword]]) await writeFile(join(secretsDir, `${key}.cnf`), `[client]\nuser=${user}\npassword=${password}\n${key === 'root' ? 'protocol=socket\nsocket=/var/run/mysqld/mysqld.sock' : `protocol=tcp\nhost=127.0.0.1\nport=3306\ndatabase=${manifest.databaseName}`}\n`, { mode: 0o600, flag: 'wx' });
  for (const image of [manifest.image, manifest.checkImage]) {
    const present = await docker(['image', 'inspect', '--format', '{{.Id}}', image]);
    if (present.code !== 0) await mustDocker('target.pull-required-image', ['pull', image], '', 180000);
  }
  manifest.beforeContainers = await containerBaseline();
  // Docker Desktop 的 internal 网络不提供发布端口；独立 bridge 禁止容器互通，并只发布到宿主回环。
  manifest.networkID = await mustDocker('target.create-private-network', ['network', 'create', '--driver', 'bridge', '--opt', 'com.docker.network.bridge.enable_icc=false', '--label', `edo.rbac.run=${manifest.prefix}`, manifest.network]);
  if (!/^[0-9a-f]{64}$/.test(manifest.networkID)) throw new Error('创建网络未返回准确 UUID');
  await checkpoint();
  manifest.containerID = await mustDocker('target.create-isolated-mysql', ['run', '--detach', '--name', manifest.container, '--label', `edo.rbac.run=${manifest.prefix}`, '--label', 'edo.purpose=rbac-sql-acceptance', '--network', manifest.network,
    '--publish', '127.0.0.1::3306', '--cpus', '1', '--memory', '1g', '--memory-swap', '1g', '--pids-limit', '256', '--read-only', '--security-opt', 'no-new-privileges',
    '--tmpfs', '/var/lib/mysql:rw,nosuid,noexec,size=536870912', '--tmpfs', '/var/run/mysqld:rw,nosuid,noexec,size=16777216', '--tmpfs', '/tmp:rw,nosuid,noexec,size=16777216',
    '--mount', `type=bind,src=${secretsDir},dst=/run/edo-rbac-secrets,readonly`, '--env-file', join(secretsDir, 'mysql.env'), manifest.image,
    '--innodb-buffer-pool-size=64M', '--innodb-redo-log-capacity=64M', '--performance-schema=OFF', '--max-connections=20', '--skip-log-bin']);
  if (!/^[0-9a-f]{64}$/.test(manifest.containerID)) throw new Error('创建容器未返回准确 UUID');
  await checkpoint(); await containerIdentity();
  const mapping = await mustDocker('target.loopback-mapping', ['inspect', '--format', '{{json (index .NetworkSettings.Ports "3306/tcp")}}', manifest.container]);
  const ports = JSON.parse(mapping);
  if (!Array.isArray(ports) || ports.length !== 1 || ports[0].HostIp !== '127.0.0.1' || !/^\d{1,5}$/.test(ports[0].HostPort)) throw new Error('目标没有唯一 loopback 端口');
  manifest.port = Number(ports[0].HostPort); await checkpoint();
  let ready = false;
  for (let attempt = 0; attempt < 90; attempt++) {
    const result = await docker(['exec', '-i', manifest.container, 'mysql', '--defaults-extra-file=/run/edo-rbac-secrets/root.cnf', '--batch', '--skip-column-names'], 'SELECT 1;\n');
    if (result.code === 0 && result.output.trim() === '1') { ready = true; break; }
    await delay(1000);
  }
  record('target.mysql-ready', ready); if (!ready) throw new Error('MySQL 初始化未完成');
  const bootstrap = `CREATE DATABASE \`${manifest.databaseName}\` CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci;\nCREATE USER '${targetSecret.runtimeUser}'@'%' IDENTIFIED BY '${targetSecret.runtimePassword}';\nGRANT SELECT, INSERT, UPDATE, CREATE ON \`${manifest.databaseName}\`.* TO '${targetSecret.runtimeUser}'@'%';\nCREATE USER '${targetSecret.verifierUser}'@'%' IDENTIFIED BY '${targetSecret.verifierPassword}';\nGRANT SELECT ON \`${manifest.databaseName}\`.* TO '${targetSecret.verifierUser}'@'%';\n`;
  await mysql('root', bootstrap, 'target.bootstrap-only-database-and-grants');
  const body = databaseBody(true, 0);
  requireResult('target.normal-form-connectivity', await admin.request('POST', '/application-databases/test', { body }), 200, (data) => uuid(data?.database?.id));
  const saved = requireResult('target.register-project-database', await admin.request('POST', '/application-databases', { body }), 200, (data) => uuid(data?.database?.id) && data.database.project_id === manifest.projectID);
  manifest.databaseID = saved.database.id; manifest.databaseVersion = saved.database.version; manifest.created.push({ kind: 'database', id: saved.database.id }); manifest.targetReady = true; await checkpoint();
  await createShadowIdentities(); await snapshot('target.empty'); await checkContainerBaseline('target.other-containers-unchanged');
}
function databaseBody(active, version) { return { name: manifest.prefix, application_id: manifest.applicationID, environment_id: manifest.environmentID, driver: 'mysql', host: manifest.apiHost, port: manifest.port, database: manifest.databaseName, username: targetSecret.runtimeUser, password: targetSecret.runtimePassword, tls_mode: 'disable', is_active: active, expected_version: version }; }
const shadowPermissions = {
  executor: ['project.read', 'sql_change.read', 'sql_change.execute', 'application_database.read'],
  self: ['project.read', 'sql_change.read', 'sql_change.create', 'sql_change.check', 'sql_change.review', 'application_database.read'],
};
async function createShadowIdentities() {
  for (const key of ['executor', 'self']) {
    const role = requireResult(`shadow.role.${key}`, await admin.request('POST', '/roles', { body: { name: `${manifest.prefix}_${key}`, display_name: `${manifest.prefix}_${key}`, description: manifest.prefix, permissions: shadowPermissions[key] } }), 201, (data) => uuid(data?.role?.id));
    manifest.roles[key] = role.role.id; manifest.created.push({ kind: 'role', id: role.role.id }); await checkpoint();
    const credential = { username: `rs${Date.now().toString(36)}${randomBytes(3).toString('hex')}`, password: `${randomBytes(30).toString('base64url')}!a9` };
    targetSecret.shadow[key] = credential;
    const user = requireResult(`shadow.user.${key}`, await admin.request('POST', '/users', { body: { ...credential, nickname: `${manifest.prefix}_${key}`, department_id: manifest.departmentID, role_ids: [] } }), 201, (data) => uuid(data?.user?.id));
    manifest.users[key] = user.user.id; manifest.created.push({ kind: 'user', id: user.user.id }); await checkpoint();
    for (const project of key === 'executor' ? [manifest.projectID, manifest.otherProjectID] : [manifest.projectID]) requireResult(`shadow.member.${key}`, await admin.request('PUT', `/projects/${project}/members/${user.user.id}`, { body: { role_id: role.role.id } }), 204);
  }
}
async function shadowClient(key) { const client = new Client(`shadow-${key}`, targetSecret.shadow[key], manifest.users[key]); clients.push(client); await client.login(); return client; }
async function snapshot(id) {
  await containerIdentity();
  const names = (await mysql('verifier', `SELECT TABLE_NAME FROM INFORMATION_SCHEMA.TABLES WHERE TABLE_SCHEMA = '${manifest.databaseName}' ORDER BY TABLE_NAME;\n`, `${id}.table-list`)).split('\n').filter(Boolean);
  const allowed = [manifest.tableName, 'edo_sql_change_receipts', 'edo_sql_change_versions'];
  if (names.some((name) => !allowed.includes(name))) throw new Error('专库出现不属于预期流程的表');
  const counts = {};
  for (const name of names) { const value = await mysql('verifier', `SELECT COUNT(*) FROM \`${manifest.databaseName}\`.\`${name}\`;\n`, `${id}.count`); if (!/^\d+$/.test(value)) throw new Error('只读核验未返回有效计数'); counts[name] = Number(value); }
  return { tables: names, counts };
}
async function unchanged(id, action) {
  const before = await snapshot(`${id}.before`), prior = await detail(roles.dba_executor);
  const result = await action();
  const after = await snapshot(`${id}.after`), next = await detail(roles.dba_executor);
  const same = JSON.stringify(before) === JSON.stringify(after) && prior.change.version === next.change.version && prior.change.status === next.change.status && prior.executions.length === next.executions.length;
  record(`${id}.no-external-effect`, same);
  if (result !== true || !same) throw new Error('拒绝场景出现意外状态，停止后续执行并保留现场');
  return result;
}
function input(self = false) { return { name: `${manifest.prefix}_${self ? 'self-review' : 'main'}`, application_id: manifest.applicationID, branch: manifest.branch, pipeline_run_id: manifest.pipelineRunID, database_id: manifest.databaseID, environment_id: manifest.environmentID, phase: 'before', reviewer_id: source.users.dba_reviewer.id, sql: self ? 'SELECT 1 AS verified;\n' : `CREATE TABLE ${manifest.tableName} (\n    id BIGINT NOT NULL,\n    marker VARCHAR(64) NOT NULL,\n    PRIMARY KEY (id)\n);\n\nINSERT INTO ${manifest.tableName} (id, marker)\nVALUES (1, '${manifest.marker}');\n`, expected_version: 0 }; }
function exactReviewedInput(value) {
  const expected = input();
  return value.sql === expected.sql && value.change.digest === createHash('sha256').update(expected.sql).digest('hex')
    && value.change.application_id === manifest.applicationID && value.change.pipeline_run_id === manifest.pipelineRunID && value.change.database_id === manifest.databaseID
    && value.change.environment_id === manifest.environmentID && value.change.branch === manifest.branch && value.change.reviewer_id === source.users.dba_reviewer.id;
}
async function detail(client, id = manifest.changeID, project = manifest.projectID) { return requireResult(`detail.${client.key}`, await client.request('GET', `/sql-changes/${id}`, { project }), 200, (data) => data?.change?.id === id && uuid(data?.revision?.id) && Array.isArray(data?.executions)); }
async function waitStatus(client, id, statuses, timeout = 180000) {
  const deadline = Date.now() + timeout;
  while (Date.now() < deadline) {
    const value = await detail(client, id);
    if (statuses.includes(value.change.status)) return value;
    if (['check_failed', 'failed', 'uncertain', 'rejected', 'skipped'].includes(value.change.status)) { record(`worker.${id}.terminal-state`, false, { workflowStatus: value.change.status, checkStatus: value.revision?.check_status }); throw new Error('真实后台任务未通过'); }
    await delay(1500);
  }
  record('worker.observed-completion', false, { reason: '超时，不把排队或运行当成成功' }); throw new Error('后台任务超时');
}
async function submit() {
  if (!manifest.targetReady || manifest.changeID) throw new Error('目标尚未就绪或工单已存在');
  const options = requireResult('submit.real-options', await roles.sql_requester.request('GET', '/sql-changes/options'), 200, (data) => data?.databases?.some((item) => item.id === manifest.databaseID && item.application_id === manifest.applicationID));
  if (!options.reviewers.some((item) => item.id === source.users.dba_reviewer.id)) throw new Error('审核岗位当前无有效候选资格');
  const before = await snapshot('self-assignment.before');
  const selfAssignment = await roles.sql_requester.request('POST', '/sql-changes', { body: { ...input(), reviewer_id: source.users.sql_requester.id } });
  const rejected = expect('requester.cannot-select-self-reviewer', selfAssignment, 400);
  if (uuid(selfAssignment.data?.change?.id)) { manifest.created.push({ kind: 'change', id: selfAssignment.data.change.id }); await checkpoint(); }
  const same = JSON.stringify(before) === JSON.stringify(await snapshot('self-assignment.after'));
  record('self-assignment.no-external-effect', same);
  if (!rejected || !same) throw new Error('自审绑定没有拒绝，保留现场且不继续提交');
  const saved = requireResult('requester.save-and-check', await roles.sql_requester.request('POST', '/sql-changes', { body: input() }), 200, (data) => uuid(data?.change?.id) && ['checking', 'pending_review'].includes(data.change.status) && exactReviewedInput(data));
  manifest.changeID = saved.change.id; manifest.created.push({ kind: 'change', id: saved.change.id }); await checkpoint();
  const pending = await waitStatus(roles.sql_requester, manifest.changeID, ['pending_review']);
  await completeSubmission(pending);
}
async function recheck() {
  if (!manifest.targetReady || !own('change', manifest.changeID) || manifest.stage || manifest.selfChangeID) throw new Error('只能显式重新检查尚未继续审核的本轮失败工单');
  const failed = await detail(roles.sql_requester);
  if (failed.change.status !== 'check_failed' || failed.executions.length || failed.reviews.length || !exactReviewedInput(failed)) throw new Error('失败工单身份或内容已变化，不重复检查');
  report.previousFailedRevisionID = failed.revision.id;
  requireResult('requester.normal-recheck', await roles.sql_requester.request('POST', `/sql-changes/${manifest.changeID}/check`, { body: { expected_version: failed.change.version } }), 202);
  const pending = await waitStatus(roles.sql_requester, manifest.changeID, ['pending_review']);
  record('recheck.preserved-previous-failure', pending.revision.id !== failed.revision.id && pending.change.version > failed.change.version);
  await completeSubmission(pending);
}
async function completeSubmission(pending) {
  record('requester.real-worker-check-passed', pending.revision.check_status === 'passed', { workflowStatus: pending.change.status });
  await unchanged('executor.cannot-run-before-review', async () => expect('executor.unreviewed-denied', await roles.dba_executor.request('POST', `/sql-changes/${manifest.changeID}/execute`, { body: { expected_version: pending.change.version, confirm_digest: pending.change.digest } }), 409));
  const self = await shadowClient('self');
  const ownChange = requireResult('self-review.save-own-change', await self.request('POST', '/sql-changes', { body: input(true) }), 200, (data) => uuid(data?.change?.id));
  manifest.selfChangeID = ownChange.change.id; manifest.created.push({ kind: 'change', id: ownChange.change.id }); await checkpoint();
  const selfPending = await waitStatus(self, manifest.selfChangeID, ['pending_review']);
  const selfRejected = expect('self-review.real-review-permission-cannot-review-own', await self.request('POST', `/sql-changes/${manifest.selfChangeID}/reviews`, { body: { expected_version: selfPending.change.version, decision: 'approve', reason: '本人申请不得自审' } }), 403);
  const preserved = await detail(self, manifest.selfChangeID);
  const preservedState = preserved.change.status === 'pending_review' && preserved.reviews.length === 0 && preserved.executions.length === 0;
  record('self-review.no-review-or-execution-record', preservedState);
  if (!selfRejected || !preservedState) throw new Error('自审未被拒绝，不继续审核和执行');
  manifest.stage = 'pending_review'; await checkpoint();
}
async function review() {
  const pending = await detail(roles.dba_reviewer);
  if (!exactReviewedInput(pending) || pending.executions.length) throw new Error('不是本轮未执行的准确工单');
  let value;
  if (pending.change.status === 'approved') {
    const legitimate = pending.reviews.length === 1 && pending.reviews[0].reviewer_id === source.users.dba_reviewer.id && pending.reviews[0].decision === 'approve';
    record('reviewer.existing-ui-review-verified', legitimate);
    if (!legitimate) throw new Error('页面审核记录与独立岗位不匹配');
    value = pending;
  } else {
    if (pending.change.status !== 'pending_review') throw new Error('不是本轮已检查并待审核的工单');
    value = requireResult('reviewer.independent-approve', await roles.dba_reviewer.request('POST', `/sql-changes/${manifest.changeID}/reviews`, { body: { expected_version: pending.change.version, decision: 'approve', reason: '核对专属测试库与单行写入' } }), 200, (data) => data?.change?.status === 'approved' && data.reviews?.some((item) => item.reviewer_id === source.users.dba_reviewer.id && item.decision === 'approve'));
  }
  record('reviewer.distinct-from-author', value.change.created_by === source.users.sql_requester.id && value.change.reviewer_id !== value.change.created_by);
  manifest.stage = 'approved'; await checkpoint();
}
async function execute() {
  const approved = await detail(roles.dba_executor);
  if (approved.change.status !== 'approved' || approved.executions.length || !exactReviewedInput(approved)) throw new Error('不是本轮全新已审核工单，不重复执行');
  const body = { expected_version: approved.change.version, confirm_digest: approved.change.digest };
  for (const key of ['sql_requester', 'dba_reviewer']) await unchanged(`${key}.no-execute`, async () => expect(`${key}.execute-denied`, await roles[key].request('POST', `/sql-changes/${manifest.changeID}/execute`, { body }), 403));
  const shadow = await shadowClient('executor');
  await detail(shadow);
  await unchanged('missing-database-execute', async () => expect('executor.missing-database-execute-denied', await shadow.request('POST', `/sql-changes/${manifest.changeID}/execute`, { body }), 403));
  const full = [...shadowPermissions.executor, 'application_database.execute'];
  try {
    requireResult('revocation.grant-private-executor', await admin.request('PUT', `/roles/${manifest.roles.executor}/permissions`, { body: { permissions: full } }), 200);
    requireResult('revocation.same-session-authorized-profile', await shadow.request('GET', '/auth/me'), 200, (data) => data?.user?.permissions?.includes('sql_change.execute') && data.user.permissions.includes('application_database.execute'));
    requireResult('cross-project.shadow-B-precondition', await shadow.request('GET', '/sql-changes', { project: manifest.otherProjectID }), 200, (data) => Array.isArray(data?.items));
    await unchanged('cross-project.object-isolation', async () => expect('cross-project.same-real-object-denied', await shadow.request('POST', `/sql-changes/${manifest.changeID}/execute`, { project: manifest.otherProjectID, body }), 404));
    requireResult('revocation.remove-private-execute', await admin.request('PUT', `/roles/${manifest.roles.executor}/permissions`, { body: { permissions: full.filter((code) => code !== 'sql_change.execute') } }), 200);
    await unchanged('revocation.current-role', async () => expect('revocation.execute-denied', await shadow.request('POST', `/sql-changes/${manifest.changeID}/execute`, { body }), 403));
  } finally { requireResult('revocation.restore-private-role', await admin.request('PUT', `/roles/${manifest.roles.executor}/permissions`, { body: { permissions: shadowPermissions.executor } }), 200); }
  const queued = requireResult('executor.normal-execute-accepted', await roles.dba_executor.request('POST', `/sql-changes/${manifest.changeID}/execute`, { body }), 202, (data) => ['queued', 'running', 'succeeded'].includes(data?.change?.status) && data.executions?.length === 1);
  manifest.executionID = queued.executions[0].id; manifest.digest = approved.change.digest; await checkpoint();
  const succeeded = await waitStatus(roles.dba_executor, manifest.changeID, ['succeeded']);
  record('executor.real-worker-succeeded', succeeded.executions.length === 1 && succeeded.executions[0].status === 'succeeded' && succeeded.executions[0].actor_id === source.users.dba_executor.id);
  manifest.stage = 'succeeded'; await checkpoint(); await verify();
  await unchanged('execution.no-repeat', async () => expect('execution.repeat-denied', await roles.dba_executor.request('POST', `/sql-changes/${manifest.changeID}/execute`, { body: { expected_version: succeeded.change.version, confirm_digest: succeeded.change.digest } }), 409));
}
async function verify() {
  if (!uuid(manifest.executionID) || !/^[0-9a-f]{64}$/.test(manifest.digest)) throw new Error('没有真实执行 UUID 与摘要');
  const state = await snapshot('verify.external-state');
  const matched = await mysql('verifier', `SELECT COUNT(*) FROM \`${manifest.databaseName}\`.\`${manifest.tableName}\` WHERE id = 1 AND marker = '${manifest.marker}';\n`, 'verify.real-record');
  const receipt = await mysql('verifier', `SELECT COUNT(*) FROM \`${manifest.databaseName}\`.edo_sql_change_receipts WHERE execution_id = '${manifest.executionID}' AND digest = '${manifest.digest}' AND state = 'succeeded';\n`, 'verify.real-execution-receipt');
  record('verify.only-reviewed-record-and-receipt', state.counts[manifest.tableName] === 1 && state.counts.edo_sql_change_receipts === 1 && matched === '1' && receipt === '1');
  report.externalState = { ...state, matchedRecordCount: Number(matched), matchedReceiptCount: Number(receipt), containerID: manifest.containerID, targetHost: manifest.apiHost, targetPort: manifest.port };
  for (const key of ['sql_requester', 'dba_reviewer', 'dba_executor']) { const value = await detail(roles[key]); record(`verify.${key}.sees-same-success`, value.change.status === 'succeeded' && value.reviews.length === 1 && value.executions.length === 1); }
}
async function cleanup() {
  const containerID = await containerIdentity({ allowStopped: true, allowAbsent: true }), networkID = await networkIdentity();
  if (containerID && !manifest.containerID) manifest.containerID = containerID;
  if (networkID && !manifest.networkID) manifest.networkID = networkID;
  await checkpoint();
  if (manifest.databaseID) {
    const result = requireResult('cleanup.current-database', await admin.request('GET', '/application-databases'), 200, (data) => Array.isArray(data?.databases));
    const current = result.databases.find((item) => item.id === manifest.databaseID && item.project_id === manifest.projectID && item.application_id === manifest.applicationID && item.environment_id === manifest.environmentID && item.name === manifest.prefix && item.database === manifest.databaseName && item.host === manifest.apiHost && item.port === manifest.port && typeof item.is_active === 'boolean' && Number.isSafeInteger(item.version) && item.version > 0);
    if (!current) throw new Error('无法确认专属目标配置');
    if (current.is_active) {
      let running = false;
      if (containerID) { try { await containerIdentity(); running = true; } catch { /* 无法确认运行中身份时保留现场。 */ } }
      record('cleanup.active-database-target-running', running, { reason: running ? undefined : '已登记数据库仍启用，无法确认目标正在运行；未停用配置，也不移除资源或绕过正常接口。' });
      if (!running) throw new Error('启用中的数据库目标不可连接，保留现场');
      requireResult('cleanup.disable-only-test-database', await admin.request('PUT', `/application-databases/${manifest.databaseID}`, { body: databaseBody(false, current.version) }), 200, (data) => data?.database?.is_active === false);
    } else record('cleanup.test-database-already-inactive', true);
  }
  if (containerID) await mustDocker('cleanup.remove-only-labeled-container', ['rm', '--force', containerID]);
  else record('cleanup.test-container-already-absent', true);
  if (networkID) await mustDocker('cleanup.remove-only-labeled-network', ['network', 'rm', networkID]);
  else record('cleanup.test-network-already-absent', true);
  manifest.targetReady = false; manifest.cleaned = true; await checkpoint();
  await checkContainerBaseline('cleanup.other-containers-unchanged');
}
try {
  await checkpoint();
  if (phase === 'prepare') {
    report.plan = { projectID: manifest.projectID, applicationID: manifest.applicationID, pipelineRunID: manifest.pipelineRunID, environmentID: manifest.environmentID, branch: manifest.branch, image: manifest.image, checkImage: manifest.checkImage, apiHost: manifest.apiHost, container: manifest.container, network: manifest.network,
      phases: ['target:独立目标与正常连接预检', 'submit:保存并由真实后台检查', 'review:独立岗位审核', 'execute:权限负例后真实执行', 'verify:只读表、记录及回执验收', 'cleanup:停用专属配置并清理专属容器网络'], roles: { requester: ['sql_change.create', 'sql_change.update', 'sql_change.check'], reviewer: ['sql_change.review'], executor: ['sql_change.execute', 'application_database.execute'] } };
    if (![manifest.applicationID, manifest.pipelineRunID, manifest.environmentID].every(uuid)) skip('fixture.normal-application-and-run', '等待正常 EDO 流程提供真实应用、分支运行和环境，当前不创建容器或触发任务');
    record('plan.no-http-docker-or-tasks', true);
  } else {
    await initializeClients();
    if (phase !== 'target' && phase !== 'all') { await loadTargetSecret(); if (phase !== 'cleanup') await containerIdentity(); }
    if (['target', 'all'].includes(phase)) await createTarget();
    if (['submit', 'all'].includes(phase)) await submit();
    if (phase === 'recheck') await recheck();
    if (['review', 'all'].includes(phase)) await review();
    if (['execute', 'all'].includes(phase)) await execute();
    if (phase === 'verify') await verify();
    if (phase === 'cleanup') await cleanup();
  }
} catch {
  record('runner.completed', false, { reason: '执行中止；保留专属现场，不打印原始异常、SQL、数据库凭据或响应正文，也不自动重试外部写入。' }); process.exitCode = 1;
} finally {
  for (const client of clients) { try { if (client.cookies.size) await client.request('POST', '/auth/logout', { project: undefined }); } catch { /* 不隐藏原始验收结果。 */ } client.cookies.clear(); }
  report.finishedAt = new Date().toISOString(); report.resources = manifest.created;
  report.summary = Object.fromEntries(['通过', '失败', '前置未满足'].map((status) => [status, report.cases.filter((item) => item.status === status).length]));
  await checkpoint(); await saveJSON(join(out, `report-${phase}-${Date.now()}.json`), report);
  console.log(`SQL 专项阶段 ${phase}：通过 ${report.summary['通过']}，失败 ${report.summary['失败']}，前置未满足 ${report.summary['前置未满足']}。`);
  if (report.summary['失败']) process.exitCode = 1;
}
