import { expect, test } from '@playwright/test'
import { adminCookie, allSongs, createRoom, enqueue, newUser, openPhone, skipCurrent, songId } from './helpers'

const ROW_H = 60

test('queue tab: virtual list with history loaded in pages', async ({ browser }) => {
  test.setTimeout(150_000)
  const cookie = await adminCookie()
  const room = await createRoom(cookie)
  const bob = await newUser(room, 'Bob')
  const songs = await allSongs()

  // 120 history entries.
  const HIST = 120
  for (let i = 0; i < HIST; i++) {
    expect(await enqueue(room, bob, songs[i % 10].id)).toBe(204)
    await skipCurrent(room, cookie, bob)
  }
  const { page, user } = await openPhone(browser, room, '小明')
  await enqueue(room, bob, await songId('這是一首'))
  await enqueue(room, user, await songId('遇見'))

  await page.locator('nav.tabs button').nth(2).click()
  const scroller = page.locator('.scroller')
  const list = page.locator('.vlist')
  await expect(page.locator('li.playing')).toBeVisible()

  // Opens on the current song after the newest 50 history rows; only visible rows exist in the DOM.
  await expect.poll(() => scroller.evaluate((el) => el.scrollTop)).toBe(50 * ROW_H)
  const rendered = await page.locator('.rows > li').count()
  expect(rendered).toBeGreaterThan(0)
  expect(rendered).toBeLessThan(30)

  // Scrolling to the top loads older pages, keeping the view where it was.
  const heights: number[] = []
  for (let i = 0; i < 4; i++) {
    await scroller.evaluate((el) => {
      el.scrollTop = 0
      el.dispatchEvent(new Event('scroll'))
    })
    await page.waitForTimeout(600)
    heights.push(Math.round((await list.boundingBox())!.height))
  }
  expect(heights[0]).toBeGreaterThan(54 * ROW_H)
  expect(Math.floor(heights.at(-1)! / ROW_H)).toBeGreaterThanOrEqual(HIST + 2)
  await expect(page.locator('.rows > li').first()).toHaveClass(/history/)
})
