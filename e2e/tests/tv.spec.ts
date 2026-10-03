import { expect, test } from '@playwright/test'
import { adminCookie, BASE, createRoom, enqueue, newUser, songId } from './helpers'

test('TV pairing: a guest pairs the first TV, only admins can add more', async ({ browser }) => {
  const cookie = await adminCookie()
  const room = await createRoom(cookie)
  const guest = await newUser(room, 'guest')
  await enqueue(room, guest, await songId('晴天'))

  const tv = await (await browser.newContext({ viewport: { width: 1280, height: 720 } })).newPage()
  await tv.goto('/tv')
  const code = (await tv.locator('.code').textContent())!.trim()
  expect(code).toMatch(/^\d{4}$/)

  // The room page offers pairing while no player is connected.
  const phoneCtx = await browser.newContext({ viewport: { width: 390, height: 844 } })
  await phoneCtx.addInitScript((t) => {
    localStorage.setItem('zkaraver.token', t)
    localStorage.setItem('zkaraver.nickname', 'guest')
  }, guest.token)
  const phone = await phoneCtx.newPage()
  await phone.goto(`/r/${room}`)
  await phone.locator('.pair input').fill(code)
  await phone.locator('.pair button[type=submit]').click()
  await tv.waitForURL(`**/r/${room}/player`)
  await expect(phone.locator('.pair')).toHaveCount(0)

  // Any key on the remote enters fullscreen and starts playback.
  await expect(tv.locator('video:not(.nosleep)')).toBeAttached({ timeout: 15_000 })
  await tv.keyboard.press('Enter')
  await expect.poll(() => tv.evaluate(() => !!document.fullscreenElement)).toBe(true)
  await expect
    .poll(() => tv.evaluate(() => (document.querySelector('video:not(.nosleep)') as HTMLVideoElement).currentTime))
    .toBeGreaterThan(0.3)

  // A second TV: guests are refused, admins may pair it and it waits in line.
  const tv2 = await browser.newPage()
  await tv2.goto('/tv')
  const code2 = (await tv2.locator('.code').textContent())!.trim()
  const guestTry = await fetch(`${BASE}/api/rooms/${room}/tv/pair`, { method: 'POST', headers: guest.headers, body: JSON.stringify({ code: code2 }) })
  expect(guestTry.status).toBe(409)
  const adminTry = await fetch(`${BASE}/api/admin/rooms/${room}/tv/pair`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', Cookie: cookie },
    body: JSON.stringify({ code: code2 }),
  })
  expect(adminTry.status).toBe(204)
  await tv2.waitForURL(`**/r/${room}/player`)
  await expect(tv2.locator('.idle-hint')).toContainText('排隊')

  // Every visit to /tv pairs afresh with a new code.
  await tv2.goto('/tv')
  await expect(tv2.locator('.code')).not.toHaveText(code2)
})

test('home page tells TVs to use /tv', async ({ page }) => {
  await page.goto('/')
  await expect(page.locator('main')).toContainText('/tv')
  await expect(page.locator('main img.logo')).toBeVisible()
})

test('a TV tab that reconnects keeps its place instead of queueing behind itself', async ({ browser }) => {
  const cookie = await adminCookie()
  const room = await createRoom(cookie)
  const instance = `tab-${Date.now()}`

  // The server still holds this tab's old connection (half-open after a network
  // blip): simulate it with a socket that never closes on its own.
  const stale = new WebSocket(`${BASE.replace(/^http/, 'ws')}/api/rooms/${room}/player/ws?instance=${instance}`)
  const staleMessages: string[] = []
  stale.onmessage = (e) => staleMessages.push(JSON.parse(String(e.data)).type)
  await expect.poll(() => staleMessages.length).toBeGreaterThan(0)

  // The same tab connects again (sessionStorage keeps its instance id).
  const ctx = await browser.newContext({ viewport: { width: 1280, height: 720 } })
  await ctx.addInitScript((id) => sessionStorage.setItem('zkaraver.playerInstance', id), instance)
  const tv = await ctx.newPage()
  await tv.goto(`/r/${room}/player`)
  await expect(tv.locator('.idle h1')).toHaveText(room) // the idle screen, not "waiting in line"
  await expect(tv.locator('.idle-hint')).not.toContainText('排隊')
  await expect.poll(() => staleMessages.includes('superseded')).toBe(true)

  // A duplicated tab (same id) takes over; the older one stops instead of fighting back.
  const dup = await ctx.newPage()
  await dup.goto(`/r/${room}/player`)
  await expect(dup.locator('.idle h1')).toHaveText(room)
  await expect(tv.locator('main h2')).toHaveText('這個播放端已在另一個分頁開啟')
  stale.close()
})
