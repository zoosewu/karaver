import { expect, test } from '@playwright/test'
import jsQR from 'jsqr'
import { PNG } from 'pngjs'
import { adminCookie, BASE, createRoom } from './helpers'

test('room QR codes carry the logo and still scan', async () => {
  const room = await createRoom(await adminCookie())
  const res = await fetch(`${BASE}/api/rooms/${room}/qr.png`)
  expect(res.headers.get('content-type')).toBe('image/png')
  const png = PNG.sync.read(Buffer.from(await res.arrayBuffer()))

  // Full size, and half size (a phone camera further away).
  for (const scale of [1, 0.5]) {
    const w = Math.round(png.width * scale)
    const h = Math.round(png.height * scale)
    const px = new Uint8ClampedArray(w * h * 4)
    for (let y = 0; y < h; y++)
      for (let x = 0; x < w; x++) {
        const s = (Math.floor(y / scale) * png.width + Math.floor(x / scale)) * 4
        px.set(png.data.subarray(s, s + 4), (y * w + x) * 4)
      }
    const code = jsQR(px, w, h)
    expect(code?.data, `decode at ${w}px`).toMatch(new RegExp(`/r/${room}$`))
  }

  // The centre is the coloured logo, not a black or white module.
  const c = (png.height / 2) * png.width * 4 + (png.width / 2) * 4
  const [r, g, b] = png.data.subarray(c, c + 3)
  expect(r === g && g === b).toBe(false)
})
