import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { test } from 'node:test'
import ts from 'typescript'

const source = readFileSync(new URL('../src/utils/infrastructureRelations.ts', import.meta.url), 'utf8')
const output = ts.transpileModule(source, { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 } }).outputText
const module = { exports: {} }
new Function('module', 'exports', output)(module, module.exports)
const { hostsForCluster, clusterForHost, clusterHostIDs } = module.exports
const host = (id, runtimeID = '') => ({ id, name: `host-${id}`, address: `10.0.0.${id}`, is_active: true, capabilities: runtimeID ? [{ kind: 'kubernetes', runtime_id: runtimeID }] : [] })
const cluster = (id, hosts) => ({ id, name: `cluster-${id}`, api_server: 'https://cluster:6443', default_namespace: 'default', is_active: true, ...(hosts === undefined ? {} : { hosts }) })

test('只有集群时无需主机记录，独立集群不从旧能力记录恢复关联', () => {
  const standalone = cluster('standalone', [])
  assert.deepEqual(hostsForCluster(standalone, []), [])
  assert.deepEqual(hostsForCluster(standalone, [host('1', 'standalone')]), [])
  assert.equal(clusterForHost(host('1', 'standalone'), [standalone]), undefined)
})

test('集群摘要可在没有主机列表权限时展示关联，主机换绑后以新摘要为准', () => {
  const old = cluster('old', [])
  const assigned = cluster('new', [host('1')])
  assert.deepEqual(hostsForCluster(assigned, []), [host('1')])
  assert.equal(clusterForHost(host('1', 'old'), [old, assigned]), assigned)
})

test('旧接口没有主机摘要时兼容已有 Kubernetes 关联，不混入 Docker 运行时', () => {
  const legacy = cluster('legacy')
  const dockerHost = { ...host('2'), capabilities: [{ kind: 'docker', runtime_id: 'legacy' }] }
  assert.deepEqual(hostsForCluster(legacy, [host('1', 'legacy'), dockerHost, host('3', 'other')]), [host('1', 'legacy')])
  assert.equal(clusterForHost(host('1', 'legacy'), [legacy]), legacy)
})

test('关联、换绑和解除单台主机时保留集群其余主机，并去除重复标识', () => {
  const item = cluster('team', [host('1'), host('2'), host('2')])
  assert.deepEqual(clusterHostIDs(item, [], '3', true), ['1', '2', '3'])
  assert.deepEqual(clusterHostIDs(item, [], '1', true), ['2', '1'])
  assert.deepEqual(clusterHostIDs(item, [], '1', false), ['2'])
  assert.deepEqual(clusterHostIDs(cluster('empty', []), [], '1', false), [])
  assert.equal(item.hosts.length, 3)
})
