import assert from 'node:assert/strict'
import {test} from 'node:test'
import {loadFrontend} from './acceptanceHarness.mjs'

const utils = loadFrontend('../src/utils/kubernetesDeployment.ts')

function buildNode(id, name, config = {}, type = 'build') {
  return { id, name, type, config: { inline_build: { kind: 'dockerfile' }, ...config } }
}

test('镜像名称按任务配置解析，留空回退应用名，变量名不进入可选产物', () => {
  const images = utils.workflowBuildImageNames('demo_app', [
    buildNode('api', '构建 API', { image_name: 'demo-api' }),
    buildNode('web', '构建 Web', {}),
    buildNode('app', '应用名构建', { image_name: '${EDO_APPLICATION_NAME}-worker' }),
    buildNode('tagged', 'Tag 变量', { image_name: '${EDO_GIT_TAG}' }),
    buildNode('files', '打包', { image_name: 'ignored', inline_build: { kind: 'script' } }),
    buildNode('deploy', '部署', {}, 'deploy'),
  ])
  assert.deepEqual(images.map(item => [item.node_id, item.image_name]), [
    ['api', 'demo-api'],
    ['web', 'demo_app'],
    ['app', 'demo_app-worker'],
  ])
  assert.equal(images[0].node_name, '构建 API')
})

test('同一镜像名称只保留一次，方案库构建按 build_plan_id 识别', () => {
  const plans = [{ id: 'plan-api', kind: 'dockerfile' }, { id: 'plan-file', kind: 'script' }]
  const images = utils.workflowBuildImageNames('demo_app', [
    { id: 'one', name: '构建一', type: 'build', config: { image_name: 'demo-api', build_plan_id: 'plan-api' } },
    { id: 'two', name: '构建二', type: 'build', config: { image_name: 'demo-api', build_plan_id: 'plan-api' } },
    { id: 'three', name: '文件构建', type: 'build', config: { image_name: 'ignored', build_plan_id: 'plan-file' } },
  ], plans)
  assert.deepEqual(images.map(item => item.image_name), ['demo-api'])
})

test('容器名称默认跟随镜像名称，非法字符被规范化', () => {
  assert.equal(utils.kubernetesResourceName('edo-demo-web'), 'edo-demo-web')
  assert.equal(utils.kubernetesResourceName('Edo_Demo/Web'), 'edo-demo-web')
  assert.equal(utils.kubernetesContainerDefault('demo_app', { config: { image_name: 'demo-api' } }), 'demo-api')
  assert.equal(utils.kubernetesContainerDefault('demo_app', { config: { image_name: '${EDO_GIT_TAG}' } }), '')
})
