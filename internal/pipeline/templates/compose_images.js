// 通用 Compose 镜像清单；服务与构建节点的关系来自页面配置。
const fs = require('node:fs');
const bindings = __EDO_BINDINGS__;
const artifacts = JSON.parse(process.env.EDO_UPSTREAM_ARTIFACTS_JSON || '[]');
const commit = process.env.EDO_COMMIT_SHA;
const run = process.env.EDO_PIPELINE_RUN_ID;
if (!/^[a-f0-9]{40,64}$/.test(commit || '') || !run) throw new Error('本次运行版本信息缺失');
const services = {};
for (const binding of bindings) {
  const matches = artifacts.filter(item => item.node_id === binding.node_id);
  const image = matches[0];
  if (matches.length !== 1 || image.kind !== 'oci_image' || image.storage_kind !== 'registry' || image.commit_sha !== commit || !/@sha256:[a-f0-9]{64}$/.test(image.image_ref || '') || !image.image_ref.endsWith('@' + image.digest)) {
    throw new Error('缺少本次提交的固定镜像制品：' + binding.service);
  }
  services[binding.service] = {image: image.image_ref, labels: {'org.opencontainers.image.revision': commit, 'io.edo.commit': commit, 'io.edo.pipeline.run.id': run}};
}
fs.writeFileSync('compose.images.json', JSON.stringify({services}, null, 2) + '\n', {mode: 0o600});
console.log('已生成 ' + bindings.length + ' 个服务的固定镜像清单');
