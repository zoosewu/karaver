// localStorage can throw (private mode, blocked storage); never let that break the app.
export const storage = {
  get(key: string): string | null {
    try {
      return localStorage.getItem(key)
    } catch {
      return null
    }
  },
  set(key: string, value: string) {
    try {
      localStorage.setItem(key, value)
    } catch {
      /* ignore */
    }
  },
}

/**
 * Keys were prefixed "karaver." before the rename to zKaraver. Move them once so
 * phones keep their identity, nickname and theme.
 */
// Runs when this module first loads, i.e. before any other module reads a key.
migrateLegacyStorage()

function migrateLegacyStorage() {
  try {
    for (let i = localStorage.length - 1; i >= 0; i--) {
      const key = localStorage.key(i)
      if (!key?.startsWith('karaver.')) continue
      const next = 'z' + key
      if (localStorage.getItem(next) === null) localStorage.setItem(next, localStorage.getItem(key) ?? '')
      localStorage.removeItem(key)
    }
  } catch {
    /* storage unavailable: nothing to migrate */
  }
}
