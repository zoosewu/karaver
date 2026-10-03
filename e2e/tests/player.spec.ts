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
  test.setTimeout(120_000)
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

  // Original vocals keep their own key even once the karaoke track is rendered.
  await expect.poll(async () => (await roomState(room, user)).player.keysReady, { timeout: 60_000 }).toContain(1)
  await control(room, cookie, 'key', 1)
  await expect(page.locator('.key-badge')).toHaveText('Key +1（原唱不變調）')
  expect(await audioSrc(page)).toMatch(/\/original$/)
  await control(room, cookie, 'key', 0)

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

// Pitch (Hz) of a rendered key track: zero crossings of the decoded audio over 10 s.
const trackPitch = (page: Page, url: string) =>
  page.evaluate(async (u) => {
    const data = await (await fetch(u)).arrayBuffer()
    const audio = await new OfflineAudioContext(1, 1, 44100).decodeAudioData(data)
    const d = audio.getChannelData(0)
    const from = 5 * audio.sampleRate
    const to = 15 * audio.sampleRate
    let crossings = 0
    for (let i = from + 1; i < to; i++) if (d[i - 1] < 0 !== d[i] < 0) crossings++
    return crossings / 2 / 10
  }, url)

const audioSrc = (page: Page) => page.evaluate(() => document.querySelector('audio')?.getAttribute('src') ?? '')

test('player: key change from server renders, speed and ±3 s seeking', async ({ page }) => {
  test.setTimeout(120_000)
  const cookie = await adminCookie()
  const room = await createRoom(cookie)
  const user = await newUser(room, 'singer')
  const song = await songId('晴天') // a 440 Hz tone
  await enqueue(room, user, song)
  await enqueue(room, user, await songId('遇見'))

  await page.goto(`/r/${room}/player`)
  await expect.poll(async () => (await media(page)).vt, { timeout: 15_000 }).toBeGreaterThan(1)

  // Speed: time-stretched, pitch preserved.
  await control(room, cookie, 'rate', 125)
  await expect.poll(() => page.evaluate(() => (document.querySelector('video:not(.nosleep)') as HTMLVideoElement).playbackRate)).toBe(1.25)
  expect(await page.evaluate(() => (document.querySelector('video:not(.nosleep)') as HTMLVideoElement).preservesPitch)).toBe(true)
  await control(room, cookie, 'rate', 100)

  // ±3 seconds.
  const before = (await media(page)).vt
  await control(room, cookie, 'seek', 3)
  await expect.poll(async () => (await media(page)).vt - before).toBeGreaterThan(2.5)
  const ahead = (await media(page)).vt
  await control(room, cookie, 'seek', -3)
  await expect.poll(async () => (await media(page)).vt).toBeLessThan(ahead - 2)

  // The playing song is rendered first: ±1..±3, then ±4..±6.
  expect((await roomState(room, user)).player.keyAvailable).toBe(true)
  await expect
    .poll(async () => (await roomState(room, user)).player.keysReady, { timeout: 60_000 })
    .toEqual(expect.arrayContaining([-3, -2, -1, 1, 2, 3]))

  // A rendered key plays at once: the video is muted under the track, in sync.
  await control(room, cookie, 'key', 2)
  await expect.poll(() => audioSrc(page)).toBe(`/media/${song}/key/2`)
  await expect.poll(async () => (await media(page)).aPaused).toBe(false)
  await expect.poll(async () => (await media(page)).muted).toBe(true)
  const m = await media(page)
  expect(Math.abs(m.at - m.vt)).toBeLessThan(0.35)
  await expect(page.locator('.key-badge')).toHaveText('Key +2')
  await expect(page.locator('.key-notice')).toHaveCount(0)
  const hz = await trackPitch(page, `/media/${song}/key/2`) // 440 × 2^(2/12) ≈ 494 Hz
  expect(Math.abs(hz - 493.9) / 493.9).toBeLessThan(0.02)

  // Volume and speed apply to the track.
  await control(room, cookie, 'volume', 20)
  await expect.poll(() => page.evaluate(() => document.querySelector('audio')!.volume)).toBeCloseTo(0.2)
  await control(room, cookie, 'volume', 100)

  // Back to the original key: the video's own audio again.
  await control(room, cookie, 'key', 0)
  await expect.poll(async () => (await media(page)).muted).toBe(false)
  await expect(page.locator('.key-badge')).toHaveCount(0)

  // Next song: the original key again.
  await control(room, cookie, 'key', 3)
  const cur = await fetch(`${BASE}/api/rooms/${room}/skip`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', Cookie: cookie },
    body: JSON.stringify({ itemId: (await roomState(room, user)).current.id }),
  })
  expect(cur.status).toBe(204)
  await expect.poll(async () => (await media(page)).vt, { timeout: 15_000 }).toBeGreaterThan(1)
  expect((await roomState(room, user)).player.key).toBe(0)
  expect(await audioSrc(page)).toBe('')
  expect((await media(page)).muted).toBe(false)
})

test('player: a key that is not rendered yet plays the original key and switches when ready', async ({ page }) => {
  const cookie = await adminCookie()
  const room = await createRoom(cookie)
  const user = await newUser(room, 'singer')
  const song = await songId('晴天')
  await enqueue(room, user, song)

  // Test media renders in a blink, so pretend nothing is ready by hiding the
  // rendered keys from the TV's room snapshots until `hide` is cleared.
  let hide = true
  await page.routeWebSocket(/\/player\/ws/, (ws) => {
    const server = ws.connectToServer()
    server.onMessage((msg) => {
      if (hide && typeof msg === 'string') {
        const s = JSON.parse(msg)
        if (s.type === 'state') {
          s.player.keysReady = []
          msg = JSON.stringify(s)
        }
      }
      ws.send(msg)
    })
  })
  await page.goto(`/r/${room}/player`)
  await expect.poll(async () => (await media(page)).vt, { timeout: 15_000 }).toBeGreaterThan(1)

  await control(room, cookie, 'key', -4)
  await expect(page.locator('.key-notice')).toHaveText('Key -4 還在製作中，先以原調播放')
  expect(await audioSrc(page)).toBe('')
  expect((await media(page)).muted).toBe(false)
  await expect(page.locator('.key-notice')).toHaveCount(0, { timeout: 5000 }) // brief

  // Rendered: the next snapshot lists the key and the TV switches by itself.
  hide = false
  await control(room, cookie, 'qr', 0) // any change sends a new snapshot
  await expect.poll(() => audioSrc(page)).toBe(`/media/${song}/key/-4`)
  await expect.poll(async () => (await media(page)).muted).toBe(true)
})
