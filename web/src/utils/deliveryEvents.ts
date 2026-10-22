import { openLiveSocket, type LiveSocketState } from './liveSocket'

export interface DeliveryEvent { type: 'resync' | 'changed'; resources?: string[] }
interface Subscriber { event: (event: DeliveryEvent) => void; state: (state: LiveSocketState) => void }
const subscribers = new Set<Subscriber>()
let dispose: (() => void) | undefined
let state: LiveSocketState = 'stopped'

// 页面会话共用一个状态连接，日志和终端单独按需连接。
export function subscribeDeliveryEvents(subscriber: Subscriber) {
  subscribers.add(subscriber)
  subscriber.state(state)
  if (!dispose) {
    dispose = openLiveSocket({
      path: '/api/v1/events/ws', protocol: 'edo-state-v1',
      onState: value => { state = value; subscribers.forEach(item => item.state(value)) },
      onEvent: data => {
        const event = data as DeliveryEvent
        if (event?.type === 'resync' || event?.type === 'changed') subscribers.forEach(item => item.event(event))
      },
    })
  }
  return () => {
    subscribers.delete(subscriber)
    if (!subscribers.size) { dispose?.(); dispose = undefined }
  }
}
