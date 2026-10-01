import zhTW from './locales/zh-TW'
import { storage } from './storage'

export type MessageKey = keyof typeof zhTW
type Dictionary = Partial<Record<MessageKey, string>>

const FALLBACK = 'zh-TW'
const dictionaries: Record<string, Dictionary> = {
  'zh-TW': zhTW,
}

function pickLocale(): string {
  const candidates = [storage.get('karaver.locale'), ...navigator.languages]
  for (const c of candidates) {
    if (c && dictionaries[c]) return c
  }
  return FALLBACK
}

export const locale = pickLocale()
document.documentElement.lang = locale === 'zh-TW' ? 'zh-Hant' : locale

export function t(key: MessageKey, params?: Record<string, string | number>): string {
  let s: string = dictionaries[locale][key] ?? zhTW[key] ?? key
  if (params) {
    for (const [k, v] of Object.entries(params)) s = s.replaceAll(`{${k}}`, String(v))
  }
  return s
}

/** Translates an API error code, falling back to a generic message. */
export function errorText(code: string): string {
  const key = `error.${code}` as MessageKey
  return key in zhTW ? t(key) : t('error.internal')
}
