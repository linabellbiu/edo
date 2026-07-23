import { useEffect } from 'react'

import { useSystemStore } from '@/stores/system'

const services = [
  { key: 'database', name: '数据服务', detail: '数据读写与状态保存' },
  { key: 'redis', name: '缓存服务', detail: '登录会话与协同状态' },
  { key: 'nats', name: '任务服务', detail: '后台任务与失败重试' },
] as const

export default function OverviewView() {
  const ready = useSystemStore((state) => state.ready)
  const loading = useSystemStore((state) => state.loading)
  const error = useSystemStore((state) => state.error)
  const updatedAt = useSystemStore((state) => state.updatedAt)
  const refresh = useSystemStore((state) => state.refresh)
  const healthy = ready?.status === 'ok'

  useEffect(() => {
    void refresh()
    const refreshTimer = window.setInterval(() => void refresh(), 15_000)
    return () => window.clearInterval(refreshTimer)
  }, [refresh])

  function serviceStatus(key: 'database' | 'redis' | 'nats') {
    return ready?.checks[key] ?? 'unknown'
  }

  return (
    <section className="overview">
      <div className="hero-panel">
        <div>
          <span className="section-label">平台状态</span>
          <h2>{healthy ? '一切运行正常' : '正在检查运行状态'}</h2>
          <p>{healthy ? '现在可以继续管理应用、构建方案和发布流程。' : 'ZRT 正在确认各项基础服务是否可用。'}</p>
        </div>
        <div className={`health-orb${healthy ? ' healthy' : ''}`}>
          <span>{healthy ? '正常' : '检查中'}</span>
        </div>
      </div>

      {error && <div className="form-alert error system-alert" role="alert">{error}</div>}

      <div className="service-grid">
        {services.map((service) => {
          const status = serviceStatus(service.key)
          return (
            <article key={service.key} className="service-card">
              <div className="service-head">
                <span className={`status-light ${status}`} />
                <span className="status-text">{status === 'ok' ? '正常' : status === 'unknown' ? '待检查' : '异常'}</span>
              </div>
              <h3>{service.name}</h3>
              <p>{service.detail}</p>
            </article>
          )
        })}
      </div>

      <div className="foundation-panel">
        <div>
          <span className="section-label">交付保障</span>
          <h2>每一步都有记录</h2>
        </div>
        <div className="foundation-list">
          <span>代码变更监听</span>
          <span>流程自由组合</span>
          <span>生产发布审批</span>
          <span>操作全程审计</span>
          <span>失败快速回滚</span>
          <span>任务有限重试</span>
        </div>
        <button className="refresh-button" type="button" disabled={loading} onClick={() => void refresh()}>
          {loading ? '检查中…' : '重新检查'}
        </button>
      </div>

      {updatedAt && (
        <p className="updated-at">
          最后检查：{updatedAt.toLocaleTimeString('zh-CN', { hour12: false })}
        </p>
      )}
    </section>
  )
}
