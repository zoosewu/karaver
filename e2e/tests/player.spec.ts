import { expect, test, type Page } from '@playwright/test'
import { adminCookie, BASE, control, createRoom, enqueue, newUser, roomState, songId } from './helpers'

// The kiosk setup from the README: sound may start without a tap.
test.use({ launchOptions: { args: ['--autoplay-policy=no-user-gesture-required'] }, viewport: { width: 1280, height: 720 } })

const media = (page: Page) =>
  page.evaluate(() => {
    const v = document.querySelector('video:not(.nosleep)') as HTMLVideoElement | null
    const a = document.querySelector('audio') as HTMLAudioElement | null
    return { vt: v?.currentTime ?? 0, vPaused: v?.paused, muted: v?.muted, at: a?.currentTime ?? 0, aPaused: a?.paused ?? true }
  })

test('player: autoplay, original vocals in sync, overlay, screen kept awake', async ({ page }) => {
  const cookie = await adminCookie()
  const room = await createRoom(cookie)
  const user = await newUser(room, 'singer')
  expect(await enqueue(room, user, await songId('倔強'))).toBe(204) // has an _original companion
  await enqueue(room, user, await songId('晴天'))

  await page.goto(`/r/${room}/player`)
  await expect(page.locator('.intro')).toBeVisible() // "up next" card, no start screen
  await expect.poll(async () => (await media(page)).vt, { timeout: 15_000 }).toBeGreaterThan(1)
  let m = await media(page)
  expect(m.vPaused).toBe(false)
  expect(m.muted).toBe(false)
  expect(m.aPaused).toBe(true) // backing track by default

  // Original vocals: karaoke video muted, original audio playing, clocks in sync.
  await control(room, cookie, 'vocal', 1)
  await expect.poll(async () => (await media(page)).aPaused).toBe(false)
  await expect.poll(async () => (await media(page)).muted).toBe(true)
  m = await media(page)
  expect(Math.abs(m.at - m.vt)).toBeLessThan(0.35)

  await control(room, cookie, 'restart')
  await expect.poll(async () => (await media(page)).vt).toBeLessThan(2)
  m = await media(page)
  expect(Math.abs(m.at - m.vt)).toBeLessThan(0.35)

  await control(room, cookie, 'pause')
  await expect.poll(async () => (await media(page)).vPaused).toBe(true)
  expect((await media(page)).aPaused).toBe(true)
  await control(room, cookie, 'play')
  await control(room, cookie, 'vocal', 0)
  await expect.poll(async () => (await media(page)).muted).toBe(false)
  expect((await media(page)).aPaused).toBe(true)

  // QR + "coming up" sit in the middle of the left edge, clear of the lyrics.
  const box = (await page.locator('aside.overlay').boundingBox())!
  expect(box.x).toBeLessThan(100)
  expect(Math.abs(box.y + box.height / 2 - 360)).toBeLessThan(40)

  // Screen stays awake: Wake Lock where allowed (https / localhost), else a muted looping video.
  const secure = await page.evaluate(() => isSecureContext && 'wakeLock' in navigator)
  if (secure) {
    await expect(page.locator('video.nosleep')).toHaveCount(0)
  } else {
    await expect.poll(() => page.evaluate(() => (document.querySelector('video.nosleep') as HTMLVideoElement)?.paused)).toBe(false)
  }
})

// Dominant frequency (Hz) and its level (dB) at the end of the TV's audio graph.
const outputPitch = (page: Page) =>
  page.evaluate(() => {
    const ks = (window as any).__zkKeyShift
    if (!ks) return null
    const a: AnalyserNode = ks.analyser
    const data = new Float32Array(a.frequencyBinCount)
    a.getFloatFrequencyData(data)
    let best = 0
    for (let i = 1; i < data.length; i++) if (data[i] > data[best]) best = i
    return { hz: (best * ks.ctx.sampleRate) / a.fftSize, db: data[best] }
  })

test('player: key change, speed and ±3 s seeking', async ({ page }) => {
  const cookie = await adminCookie()
  const room = await createRoom(cookie)
  const user = await newUser(room, 'singer')
  await enqueue(room, user, await songId('晴天')) // a 440 Hz tone
  await enqueue(room, user, await songId('遇見'))

  await page.goto(`/r/${room}/player`)
  await expect.poll(async () => (await media(page)).vt, { timeout: 15_000 }).toBeGreaterThan(1)

  // Speed: time-stretched, pitch preserved.
  await control(room, cookie, 'rate', 125)
  await expect.poll(() => page.evaluate(() => (document.querySelector('video:not(.nosleep)') as HTMLVideoElement).playbackRate)).toBe(1.25)
  expect(await page.evaluate(() => (document.querySelector('video:not(.nosleep)') as HTMLVideoElement).preservesPitch)).toBe(true)

  // ±3 seconds.
  const before = (await media(page)).vt
  await control(room, cookie, 'seek', 3)
  await expect.poll(async () => (await media(page)).vt - before).toBeGreaterThan(2.5)
  const ahead = (await media(page)).vt
  await control(room, cookie, 'seek', -3)
  await expect.poll(async () => (await media(page)).vt).toBeLessThan(ahead - 2)

  // Audio is untouched until someone changes the key.
  expect(await outputPitch(page)).toBeNull()

  const near = (hz: number, want: number) => Math.abs(hz - want) / want < 0.03
  await control(room, cookie, 'key', 5) // 440 × 2^(5/12) ≈ 587 Hz
  await expect.poll(async () => near((await outputPitch(page))?.hz ?? 0, 587.3), { timeout: 5000 }).toBe(true)
  await expect(page.locator('.key-badge')).toHaveText('Key +5')

  await control(room, cookie, 'key', -5) // ≈ 330 Hz
  await expect.poll(async () => near((await outputPitch(page))?.hz ?? 0, 329.6), { timeout: 5000 }).toBe(true)

  await control(room, cookie, 'key', 0) // bypassed: the original 440 Hz
  await expect.poll(async () => near((await outputPitch(page))?.hz ?? 0, 440), { timeout: 5000 }).toBe(true)
  await expect(page.locator('.key-badge')).toHaveCount(0)

  // Volume still works with the audio routed through the graph (a clear drop; the
  // exact dB depends on how the browser applies element volume to the graph).
  await control(room, cookie, 'volume', 100)
  await page.waitForTimeout(500)
  const loud = (await outputPitch(page))!.db
  await control(room, cookie, 'volume', 20)
  await expect.poll(async () => loud - (await outputPitch(page))!.db).toBeGreaterThan(5)
  await control(room, cookie, 'volume', 100)

  // Next song: normal speed and the original key again.
  await control(room, cookie, 'key', 3)
  const cur = await fetch(`${BASE}/api/rooms/${room}/skip`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', Cookie: cookie },
    body: JSON.stringify({ itemId: (await roomState(room, user)).current.id }),
  })
  expect(cur.status).toBe(204)
  await expect.poll(async () => (await media(page)).vt, { timeout: 15_000 }).toBeGreaterThan(1)
  expect(await page.evaluate(() => (document.querySelector('video:not(.nosleep)') as HTMLVideoElement).playbackRate)).toBe(1)
  await expect.poll(async () => near((await outputPitch(page))?.hz ?? 0, 440), { timeout: 5000 }).toBe(true)
})
