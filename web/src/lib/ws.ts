import type { RoomState } from './types'

export type Terminal = 'kicked' | 'deleted' | 'rejected'

export interface RoomSocketHandlers {
  onState: (s: RoomState) => void
  onConnection?: (connected: boolean) => void
  /** Called once when the server ends the subscription for good; no reconnect follows. */
  onTerminal?: (reason: Terminal, code?: string) => void
}

/** Subscribes to room state with automatic reconnect. Returns a function that closes it. */
export function connectRoom(path: string, h: RoomSocketHandlers): () => void {
  let ws: WebSocket | null = null
  let stopped = false
  let delay = 500
  let timer: ReturnType<typeof setTimeout> | undefined

  const stop = () => {
    stopped = true
    clearTimeout(timer)
    ws?.close()
  }

  const open = () => {
    const proto = location.protocol === 'https:' ? 'wss:' : 'ws:'
    ws = new WebSocket(`${proto}//${location.host}${path}`)
    ws.onopen = () => {
      delay = 500
      h.onConnection?.(true)
    }
    ws.onmessage = (ev) => {
      const msg = JSON.parse(ev.data)
      if (msg.type === 'state') {
        h.onState(msg)
      } else if (msg.type === 'kicked' || msg.type === 'deleted') {
        stop()
        h.onTerminal?.(msg.type)
      }
    }
    ws.onclose = (ev) => {
      if (stopped) return
      h.onConnection?.(false)
      if (ev.code === 4003) {
        stop()
        h.onTerminal?.('rejected', ev.reason)
        return
      }
      timer = setTimeout(open, delay)
      delay = Math.min(delay * 2, 10_000)
    }
  }

  // Phones suspend sockets in the background; reconnect immediately on return.
  const onVisible = () => {
    if (document.visibilityState === 'visible' && !stopped && ws?.readyState === WebSocket.CLOSED) {
      clearTimeout(timer)
      open()
    }
  }
  document.addEventListener('visibilitychange', onVisible)

  open()
  return () => {
    document.removeEventListener('visibilitychange', onVisible)
    stop()
  }
}
