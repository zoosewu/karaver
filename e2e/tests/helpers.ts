import { expect, type Browser, type Page } from '@playwright/test'

export const BASE = process.env.BASE_URL ?? 'http://localhost:8088'
const ADMIN_PASSWORD = process.env.ADMIN_PASSWORD ?? 'test-pass'
const JSON_H = { 'Content-Type': 'application/json' }

export interface Song {
  id: number
  title: string
  artist: string
}

export interface User {
  token: string
  userId: string
  headers: Record<string, string>
}

/** A room name no other test (or earlier run) uses. */
export function uniqueRoom(prefix = 'e2e'): string {
  return `${prefix}-${Date.now().toString(36)}${Math.random().toString(36).slice(2, 6)}`
}

export async function adminCookie(): Promise<string> {
  const r = await fetch(`${BASE}/api/admin/login`, { method: 'POST', headers: JSON_H, body: JSON.stringify({ password: ADMIN_PASSWORD }) })
  expect(r.status, 'admin login').toBe(204)
  return r.headers.get('set-cookie')!.split(';')[0]
}

export async function createRoom(cookie: string, name = uniqueRoom()): Promise<string> {
  const r = await fetch(`${BASE}/api/admin/rooms`, {
    method: 'POST',
    headers: { ...JSON_H, Cookie: cookie },
    body: JSON.stringify({ name }),
  })
  expect(r.status, 'create room').toBe(201)
  return name
}

export async function newUser(room: string, nickname: string): Promise<User> {
  const s = await (await fetch(`${BASE}/api/session`, { method: 'POST', headers: JSON_H, body: '{}' })).json()
  const user = { token: s.token, userId: s.userId, headers: { ...JSON_H, Authorization: `Bearer ${s.token}` } }
  const r = await fetch(`${BASE}/api/rooms/${room}/join`, { method: 'POST', headers: user.headers, body: JSON.stringify({ nickname }) })
  expect(r.status, 'join').toBe(204)
  return user
}

export async function enqueue(room: string, user: Pick<User, 'headers'>, songId: number): Promise<number> {
  const r = await fetch(`${BASE}/api/rooms/${room}/queue`, { method: 'POST', headers: user.headers, body: JSON.stringify({ songId }) })
  return r.status
}

let songCache: Song[] | null = null
export async function allSongs(): Promise<Song[]> {
  if (songCache) return songCache
  const out: Song[] = []
  for (let offset = 0; ; offset += 100) {
    const page = await (await fetch(`${BASE}/api/songs?limit=100&offset=${offset}`)).json()
    out.push(...page.items)
    if (!page.more) break
  }
  return (songCache = out)
}

export async function songId(needle: string): Promise<number> {
  const s = (await allSongs()).find((x) => x.title.includes(needle))
  if (!s) throw new Error(`test song "${needle}" missing: run e2e/make-media.sh`)
  return s.id
}

/** The room's current state, read like a phone does (member WebSocket). */
export function roomState(room: string, user: Pick<User, 'token'>): Promise<any> {
  return new Promise((resolve, reject) => {
    const ws = new WebSocket(`${BASE.replace(/^http/, 'ws')}/api/rooms/${room}/ws?token=${user.token}`)
    ws.onmessage = (e) => {
      resolve(JSON.parse(String(e.data)))
      ws.close()
    }
    ws.onerror = () => reject(new Error('websocket failed'))
  })
}

export async function skipCurrent(room: string, cookie: string, viewer: Pick<User, 'token'>) {
  const cur = (await roomState(room, viewer)).current
  if (!cur) return
  const r = await fetch(`${BASE}/api/rooms/${room}/skip`, {
    method: 'POST',
    headers: { ...JSON_H, Cookie: cookie },
    body: JSON.stringify({ itemId: cur.id }),
  })
  expect(r.status, 'skip').toBe(204)
}

export async function control(room: string, cookie: string, action: string, value = 0) {
  const r = await fetch(`${BASE}/api/rooms/${room}/control`, {
    method: 'POST',
    headers: { ...JSON_H, Cookie: cookie },
    body: JSON.stringify({ action, value }),
  })
  expect(r.status, `control ${action}`).toBe(204)
}

/** A phone-sized page that joins `room` as `nickname` and returns that user. */
export async function openPhone(browser: Browser, room: string, nickname: string, storageKey = 'zkaraver.nickname') {
  const ctx = await browser.newContext({ viewport: { width: 375, height: 760 }, deviceScaleFactor: 2, isMobile: true, hasTouch: true })
  const page = await ctx.newPage()
  await page.addInitScript(([k, v]) => localStorage.setItem(k, v), [storageKey, nickname])
  await page.goto(`/r/${room}`)
  await expect(page.locator('input[type=search]')).toBeVisible()
  const token = await page.evaluate(() => localStorage.getItem('zkaraver.token'))
  const user = { token: token!, userId: '', headers: { ...JSON_H, Authorization: `Bearer ${token}` } }
  return { page, user }
}

/** Answers the in-page confirmation dialog. */
export async function confirmDialog(page: Page, accept = true) {
  const panel = page.locator('.panel .buttons')
  await expect(panel).toBeVisible()
  await panel.locator(accept ? 'button:last-child' : 'button:first-child').click()
}
