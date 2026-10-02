import { expect, test } from '@playwright/test'
import { adminCookie, BASE, createRoom, enqueue, newUser, roomState, skipCurrent, songId, uniqueRoom } from './helpers'

async function adminPage(browser: import('@playwright/test').Browser, mobile = false) {
  const cookie = await adminCookie()
  const ctx = await browser.newContext(mobile ? { viewport: { width: 375, height: 760 }, isMobile: true, hasTouch: true } : {})
  const [name, value] = cookie.split('=')
  await ctx.addCookies([{ name, value, url: BASE }])
  return { page: await ctx.newPage(), cookie }
}

test('admin: room names follow the rules', async ({ browser }) => {
  const { page } = await adminPage(browser)
  await page.goto('/admin')
  const input = page.locator('form.create input')
  await input.fill('客廳')
  expect(await input.evaluate((el: HTMLInputElement) => el.checkValidity())).toBe(false)
  const name = uniqueRoom('Admin')
  await input.fill(name)
  await page.locator('form.create button').click()
  await expect(page.locator('.admin-room .name')).toContainText(name)
})

test('admin on a phone: fixed controls, tabs, history loads as you scroll', async ({ browser }) => {
  test.setTimeout(120_000)
  const { page, cookie } = await adminPage(browser, true)
  const room = await createRoom(cookie)
  const u = await newUser(room, 'U')
  const titles = ['倔強', '晴天', '遇見']
  for (let i = 0; i < 60; i++) {
    await enqueue(room, u, await songId(titles[i % 3]))
    await skipCurrent(room, cookie, u)
  }
  await enqueue(room, u, await songId('這是一首'))
  await enqueue(room, u, await songId('晴天'))

  await page.addInitScript((r) => localStorage.setItem('zkaraver.adminRoom', r), room)
  await page.goto('/admin')
  const top = page.locator('.admin-room .top')
  await expect(top.locator('.now-text')).toContainText('這是一首')
  await expect(top.locator('.transport button')).toHaveCount(4)
  expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(375)

  // Tabs.
  await expect(page.locator('.admin-room .body li')).toHaveCount(1) // queue: one waiting song
  await top.locator('.tabs button', { hasText: '設定' }).click()
  await expect(page.locator('.admin-room .danger-zone')).toBeVisible()
  await top.locator('.tabs button', { hasText: '成員' }).click()
  await expect(page.locator('.admin-room .body li')).toContainText('U')

  // History: 50 first, the rest when the end of the list scrolls into view.
  await top.locator('.tabs button', { hasText: '紀錄' }).click()
  const rows = page.locator('.admin-room .body li')
  await expect(rows).toHaveCount(50)
  await page.locator('.admin-room .end').scrollIntoViewIfNeeded()
  await expect(rows).toHaveCount(60)
  await expect(page.locator('.admin-room .end')).toHaveText('沒有更早的紀錄了')
  // The controls are still on screen after scrolling down.
  expect((await top.boundingBox())!.y).toBeGreaterThanOrEqual(0)
  expect((await top.boundingBox())!.y).toBeLessThan(5)
})

test('admin: seek and speed buttons drive the room', async ({ browser }) => {
  const { page, cookie } = await adminPage(browser)
  const room = await createRoom(cookie)
  const u = await newUser(room, 'U')
  await enqueue(room, u, await songId('晴天'))
  await page.addInitScript((r) => localStorage.setItem('zkaraver.adminRoom', r), room)
  await page.goto('/admin')
  const bar = page.locator('.admin-room .seekbar')
  await expect(bar.locator('.rate')).toHaveText('1.00×')
  await bar.locator('button', { hasText: '+' }).click()
  await bar.locator('button', { hasText: '+' }).click()
  await expect(bar.locator('.rate')).toHaveText('1.10×')
  await bar.locator('.rate').click()
  await expect(bar.locator('.rate')).toHaveText('1.00×')
  await bar.locator('button', { hasText: '3s ⏩' }).click()
  await expect.poll(async () => (await roomState(room, u)).player.seekNonce).toBe(1)
  expect((await roomState(room, u)).player.seekDelta).toBe(3)

  // Key change from the "⋯" sheet.
  await page.locator('.admin-room .transport .more').click()
  await page.locator('.panel .action', { hasText: '升 key' }).click()
  await expect(page.locator('.admin-room .now .badge', { hasText: 'Key +1' })).toBeVisible()
})
