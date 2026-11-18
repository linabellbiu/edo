<script setup lang="ts">
import { computed, ref } from 'vue'
import { ConfigProvider, theme } from 'ant-design-vue'

const dark = ref(new URLSearchParams(location.search).get('theme') === 'dark')
document.documentElement.dataset.theme = dark.value ? 'dark' : 'light'
const antTheme = computed(() => ({
  token: { colorPrimary: '#4f6ef7', borderRadius: 6, borderRadiusLG: 8, colorBgContainer: dark.value ? '#17181d' : '#ffffff', colorBorderSecondary: dark.value ? '#292b33' : '#e9eaee' },
  algorithm: dark.value ? theme.darkAlgorithm : theme.defaultAlgorithm,
}))

const instances = [
  { name: 'edo-demo-web', location: '测试集群 · demo', kind: 'Kubernetes', status: '运行中', tone: 'success', time: '2026/11/17 22:52:19', namespace: 'demo', workload: 'edo-demo-web', replicas: '1 / 1 就绪', replicaTone: 'is-ready', image: 'uhub.service.ucloud.cn/zrt-application/edo-demo-web:v0.0.8' },
  { name: 'docker-compose 部署', location: '本机 Docker', kind: 'Docker Compose', status: '部署成功', tone: 'success', time: '2026/11/17 22:52:34', project: 'edo-demo', image: 'registry.local/edo-demo:latest' },
  { name: 'admin-web', location: '测试集群 · demo', kind: 'Kubernetes', status: '已停止', tone: 'warning', time: '2026/11/17 17:25:30', namespace: 'demo', workload: 'admin-web', replicas: '0 / 0 就绪', replicaTone: 'is-idle', image: 'uhub.service.ucloud.cn/zrt-application/admin-web:v0.0.3' },
]
</script>

<template>
  <ConfigProvider :theme="antTheme">
    <div class="apps-workspace apps-instance-section preview">
      <h2 class="preview-title">部署实例行内排版</h2>
      <div class="application-deployment-list">
        <div class="application-deployment-columns" aria-hidden="true"><span>实例 / 运行位置</span><span>类型</span><span>状态</span><span>部署时间</span></div>
        <div v-for="item in instances" :key="item.name" class="application-deployment-item">
          <div class="application-deployment-summary is-static">
            <span class="application-deployment-identity"><span class="application-instance-icon"><span>/</span></span><span class="application-instance-name"><strong>{{ item.name }}</strong><span>{{ item.location }}</span></span></span>
            <span class="application-instance-kind">{{ item.kind }}</span>
            <span class="apps-status" :class="'tone-' + item.tone"><i aria-hidden="true"/>{{ item.status }}</span>
            <time>{{ item.time }}</time>
          </div>
          <div class="application-deployment-inline">
            <dl class="application-instance-meta">
              <div v-if="item.namespace"><dt>命名空间</dt><dd>{{ item.namespace }}</dd></div>
              <div v-if="item.workload"><dt>工作负载</dt><dd>{{ item.workload }}</dd></div>
              <div v-if="item.replicas"><dt>副本</dt><dd class="meta-replicas" :class="item.replicaTone">{{ item.replicas }}</dd></div>
              <div v-if="item.project"><dt>Compose 项目</dt><dd>{{ item.project }}</dd></div>
              <div class="meta-image"><dt>镜像</dt><dd>{{ item.image }}</dd></div>
            </dl>
            <div class="application-deployment-actions resource-row-actions">
              <a-button size="small">日志</a-button>
              <a-button size="small" danger>停止</a-button>
              <a-button size="small">重启</a-button>
              <a-button size="small">扩缩容</a-button>
              <a-button size="small">回退上一镜像</a-button>
            </div>
          </div>
        </div>
      </div>
    </div>
  </ConfigProvider>
</template>

<style scoped>
.preview { padding: 24px; background: var(--edo-bg); min-height: 100vh; }
.preview-title { margin: 0 0 12px; font-size: 14px; color: var(--edo-muted); font-weight: 500; }
.application-deployment-list { overflow: hidden; border: 1px solid var(--edo-border); border-radius: 10px; background: var(--edo-surface); }
.application-instance-icon span { color: var(--edo-muted); }
</style>
