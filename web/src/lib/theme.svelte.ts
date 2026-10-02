import { storage } from './storage'

// The theme is a per-device preference: stored in localStorage only.
export type Theme = 'dark' | 'light'

const KEY = 'zkaraver.theme'
const THEME_COLOR: Record<Theme, string> = { dark: '#14111c', light: '#f7f5fa' }

export const themeState = $state<{ current: Theme }>({
  current: storage.get(KEY) === 'light' ? 'light' : 'dark',
})

function apply(t: Theme) {
  document.documentElement.dataset.theme = t
  document.querySelector('meta[name="theme-color"]')?.setAttribute('content', THEME_COLOR[t])
}

export function setTheme(t: Theme) {
  themeState.current = t
  storage.set(KEY, t)
  apply(t)
}

/** Called once before the app mounts so the first paint uses the right colors. */
export function initTheme() {
  apply(themeState.current)
}
