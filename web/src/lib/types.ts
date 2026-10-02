export type QueueMode = 'fifo' | 'rr'

export interface Song {
  id: number
  title: string
  artist: string
}

export interface QueueItem {
  id: number
  songId: number
  title: string
  artist: string
  userId: string
  nickname: string
  /** An original-vocal version exists, so the requester can switch to it. */
  hasOriginal: boolean
}

export interface RoomSettings {
  name: string
  mode: QueueMode
  maxPerUser: number
  idleClearMinutes: number
}

export interface Member {
  userId: string
  nickname: string
  online: boolean
  banned?: boolean
}

export interface RoomState {
  type: 'state'
  id: string
  settings: RoomSettings
  current: QueueItem | null
  queue: QueueItem[]
  player: {
    online: boolean
    paused: boolean
    volume: number
    showQR: boolean
    restartNonce: number
    /** true = original vocals (karaoke video + original audio) */
    vocal: boolean
    /** playback speed in percent; pitch is preserved */
    rate: number
    /** changes on every relative seek; seekDelta is how many seconds */
    seekNonce: number
    seekDelta: number
    /** key change in semitones (-6..6), applied on the TV */
    key: number
  }
  members: Member[]
  /** Admin connections only: every connected player, active one first. */
  players?: PlayerConnection[]
  /** Player connections only: 0 = active, 1 = next in line, ... */
  self?: { secret: string; position: number }
  /** Changes when the history is cleared: reload it. */
  historyRev: number
}

export interface PlayerConnection {
  id: string
  active: boolean
  remoteAddr: string
  userAgent: string
  connectedAt: number
}

export interface RoomSummary {
  id: string
  settings: RoomSettings
  queueLength: number
  playing?: string
  playerOnline: boolean
  online: number
}

export interface HistoryEntry {
  id: number
  songId: number
  title: string
  artist: string
  userId: string
  nickname: string
  status: 'done' | 'skipped' | 'failed'
  startedAt: number
  /** still in the library, so it can be queued again */
  present: boolean
}

export interface ScanResult {
  added: number
  updated: number
  removed: number
  total: number
  finishedAt: number
  durationMs: number
  error?: string
}
