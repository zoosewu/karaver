import { expect, test } from '@playwright/test'
import { adminCookie, confirmDialog, createRoom, enqueue, newUser, openPhone, skipCurrent, songId } from './helpers'

const LONG = '這是一首'

test.describe('room page on a phone', () => {
  let cookie: string
  test.beforeAll(async () => {
    cookie = await adminCookie()
  })

  test('fits the screen, scrolls long titles, keeps the controls fixed', async ({ browser }) => {
    const room = await createRoom(cookie)
    const { page, user } = await openPhone(browser, room, '小明')
    expect(await enqueue(room, user, await songId(LONG))).toBe(204)

    const now = page.locator('.now .now-text')
    await expect(now).toContainText(LONG)
    await expect(now).toContainText('小明')
    expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(375)
    // Song · artist · requester is one line, and it scrolls because it does not fit.
    expect((await now.boundingBox())!.height).toBeLessThan(32)
    await expect(now.locator('.inner')).toHaveClass(/run/)

    // Only the list scrolls; the top stays put and is compact.
    const top = page.locator('.top')
    await page.locator('.scroller').evaluate((el) => (el.scrollTop = el.scrollHeight))
    expect((await top.boundingBox())!.y).toBe(0)
    expect(await page.evaluate(() => scrollY)).toBe(0)
    // Worst case: my-turn controls + TV pairing row + search box, still well under half the screen.
    expect((await top.boundingBox())!.height).toBeLessThan(760 * 0.42)

    // Request buttons are the quiet tonal style, not solid accent.
    const bg = await page.locator('.scroller button.tonal').first().evaluate((b) => getComputedStyle(b).backgroundColor)
    expect(bg).toMatch(/^rgba\(/)
  })

  test('pause, restart and skip are equal buttons that ask first', async ({ browser }) => {
    const room = await createRoom(cookie)
    const { page, user } = await openPhone(browser, room, '小明')
    await enqueue(room, user, await songId('晴天'))

    const buttons = page.locator('.transport button:not(.more)')
    await expect(buttons).toHaveCount(3)
    const widths = await buttons.evaluateAll((bs) => bs.map((b) => Math.round(b.getBoundingClientRect().width)))
    expect(new Set(widths).size).toBe(1)

    for (const [i, word] of [[0, '暫停'], [1, '重唱'], [2, '切掉']] as const) {
      await buttons.nth(i).click()
      await expect(page.locator('.panel .message')).toContainText(word)
      await confirmDialog(page, false)
    }
    // Volume, TV QR and vocals live behind "⋯".
    await page.locator('.transport .more').click()
    await expect(page.locator('.panel .action')).toContainText(['音量調大', '音量調小', 'QR'])
  })

  test('shows the room QR code', async ({ browser }) => {
    const room = await createRoom(cookie)
    const { page } = await openPhone(browser, room, '小明')
    await page.locator('header button', { hasText: 'QR' }).click()
    const img = page.locator('.qr-panel img')
    await expect(img).toBeVisible()
    await expect.poll(() => img.evaluate((i: HTMLImageElement) => i.complete && i.naturalWidth > 0)).toBe(true)
  })

  test('queue: turn counter, menus, moving and deleting', async ({ browser }) => {
    const room = await createRoom(cookie)
    const bob = await newUser(room, 'Bob')
    const { page, user } = await openPhone(browser, room, '小明')
    await enqueue(room, bob, await songId(LONG)) // playing
    await enqueue(room, bob, await songId('Bohemian'))
    await enqueue(room, user, await songId('遇見'))

    await expect(page.locator('.now .turn')).toHaveText('再 1 首輪到你')

    await page.locator('nav.tabs button').nth(2).click()
    const row = (who: string) => page.locator('li.upcoming', { has: page.locator('.who', { hasText: who }) })
    const firstWho = () => page.locator('li.upcoming .who').first().textContent()

    // Bob's song is first: no delete for me, and moves are disabled.
    await row('Bob').locator('button.icon').click()
    await expect(page.locator('.panel .action', { hasText: '刪除' })).toHaveCount(0)
    await expect(page.locator('.panel .action', { hasText: '向上一首' })).toBeDisabled()
    await page.locator('.panel .action.cancel').click()

    // FIFO: anyone may move any song.
    await row('小明').locator('button.icon').click()
    await page.locator('.panel .action', { hasText: '移到最上面' }).click()
    await expect.poll(firstWho).toBe('小明')
    await expect(page.locator('.now .turn')).toHaveText('下一首輪到你')
    await row('Bob').locator('button.icon').click()
    await page.locator('.panel .action', { hasText: '向上一首' }).click()
    await expect.poll(firstWho).toBe('Bob')

    // Deleting my own song asks twice.
    await row('小明').locator('button.icon').click()
    await page.locator('.panel .action', { hasText: '刪除歌曲' }).click()
    await expect(page.locator('.panel .message')).toContainText('刪除')
    await confirmDialog(page)
    await expect(page.locator('.panel .message')).toContainText('再確認')
    await confirmDialog(page)
    await expect(page.locator('li.upcoming')).toHaveCount(1)
  })

  test('queue tab opens on the current song with history above it', async ({ browser }) => {
    const room = await createRoom(cookie)
    const { page, user } = await openPhone(browser, room, '小明')
    await enqueue(room, user, await songId('倔強'))
    await skipCurrent(room, cookie, user)
    await enqueue(room, user, await songId('晴天'))
    await skipCurrent(room, cookie, user)
    await enqueue(room, user, await songId('遇見'))

    await page.locator('nav.tabs button').nth(2).click()
    await expect(page.locator('li.playing')).toBeVisible()
    const scroller = await page.locator('.scroller').boundingBox()
    await expect.poll(async () => Math.round((await page.locator('li.playing').boundingBox())!.y - scroller!.y)).toBe(0)
    await expect(page.locator('li.history')).toHaveCount(2)
    await expect(page.locator('.divider')).toHaveCount(0) // colours, not section titles

    // History rows can be sung again. Nothing is waiting, so "replay all" is available;
    // once a song is queued it is not.
    await page.locator('li.history').first().locator('button.icon').click()
    await expect(page.locator('.panel .action', { hasText: '重唱' })).toBeVisible()
    await page.locator('.panel .action.cancel').click()
    await page.locator('header button[aria-label="房間設定"]').click()
    await expect(page.locator('.panel .action', { hasText: '全部重唱' })).toBeEnabled()
    await page.locator('.panel .action.cancel').click()
    await enqueue(room, user, await songId('倔強'))
    await page.locator('header button[aria-label="房間設定"]').click()
    await expect(page.locator('.panel .action', { hasText: '全部重唱' })).toBeDisabled()
  })

  test('search loads more results while scrolling', async ({ browser }) => {
    const room = await createRoom(cookie)
    const { page } = await openPhone(browser, room, '小明')
    await page.locator('input[type=search]').fill('filler')
    const rows = page.locator('.scroller li')
    await expect(rows).toHaveCount(50) // first page
    await page.locator('.scroller').evaluate((el) => (el.scrollTop = el.scrollHeight))
    await expect(rows).toHaveCount(70) // the rest arrived without a button
    await expect(page.locator('button', { hasText: '載入更多' })).toHaveCount(0)
  })

  test('forgiving search: spaces, punctuation and full-width are ignored', async ({ browser }) => {
    const room = await createRoom(cookie)
    const { page } = await openPhone(browser, room, '小明')
    for (const q of ['五月天倔強', '倔強 五月天', 'ｑｕｅｅｎ']) {
      await page.locator('input[type=search]').fill(q)
      await expect(page.locator('.scroller li').first()).toBeVisible()
      await expect(page.locator('.scroller li')).toHaveCount(1)
    }
  })

  test('theme is chosen per device', async ({ browser }) => {
    const room = await createRoom(cookie)
    const { page } = await openPhone(browser, room, '小明')
    await page.locator('header button[aria-label="房間設定"]').click()
    await page.locator('.panel .action', { hasText: '淺色主題' }).click()
    await expect(page.locator('html')).toHaveAttribute('data-theme', 'light')
    expect(await page.evaluate(() => getComputedStyle(document.body).backgroundColor)).toBe('rgb(247, 245, 250)')
    await page.reload()
    await expect(page.locator('html')).toHaveAttribute('data-theme', 'light')

    const other = await (await browser.newContext()).newPage()
    await other.goto(`/r/${room}`)
    await expect(other.locator('html')).toHaveAttribute('data-theme', 'dark')
  })

  test('keeps phones logged in across the Karaver → zKaraver rename', async ({ browser }) => {
    const room = await createRoom(cookie)
    // A phone that only has the old "karaver." keys still joins with its nickname.
    const { page } = await openPhone(browser, room, '舊手機', 'karaver.nickname')
    await expect(page.locator('header .nick')).toContainText('舊手機')
    expect(await page.evaluate(() => [localStorage.getItem('karaver.nickname'), localStorage.getItem('zkaraver.nickname')])).toEqual([
      null,
      '舊手機',
    ])
  })
})

test('history can be deleted from the room settings once the queue is empty', async ({ browser }) => {
  const cookie = await adminCookie()
  const room = await createRoom(cookie)
  const { page, user } = await openPhone(browser, room, '小明')
  const other = await openPhone(browser, room, '阿華') // a second phone watching the queue tab
  await enqueue(room, user, await songId('倔強'))
  await skipCurrent(room, cookie, user)
  await enqueue(room, user, await songId('晴天'))
  await skipCurrent(room, cookie, user)
  await other.page.locator('nav.tabs button').nth(2).click()
  await expect(other.page.locator('li.history')).toHaveCount(2)

  // Waiting songs block it, like "replay all".
  await enqueue(room, user, await songId('遇見')) // plays
  await enqueue(room, user, await songId('這是一首')) // waits
  await page.locator('header button[aria-label="房間設定"]').click()
  await expect(page.locator('.panel .action', { hasText: '刪除演唱紀錄' })).toBeDisabled()
  await page.locator('.panel .action.cancel').click()
  await skipCurrent(room, cookie, user) // 遇見 -> history; 這是一首 plays; queue empty

  await page.locator('header button[aria-label="房間設定"]').click()
  await page.locator('.panel .action', { hasText: '刪除演唱紀錄' }).click()
  await confirmDialog(page)
  await expect(page.locator('.panel .message')).toContainText('再確認')
  await confirmDialog(page)

  // Cleared everywhere at once; the song that is playing stays.
  await expect(other.page.locator('li.history')).toHaveCount(0)
  await expect(other.page.locator('li.playing')).toContainText('這是一首')
})
