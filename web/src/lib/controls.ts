import type { SheetAction } from './dialog.svelte'
import { t } from './i18n'

const MAX_KEY = 6

/** "Key +2" / "原調" for a semitone offset. */
export function keyLabel(key: number): string {
  return key === 0 ? t('room.keyOriginal') : t('room.keyNow', { n: key > 0 ? `+${key}` : `${key}` })
}

/** Key up / down / reset, one semitone at a time, for the "⋯" sheets. */
export function keyActions(key: number, control: (action: string, value?: number) => unknown): SheetAction[] {
  return [
    { label: `♯ ${t('room.keyUp')}`, disabled: key >= MAX_KEY, run: () => control('key', key + 1) },
    { label: `♭ ${t('room.keyDown')}`, disabled: key <= -MAX_KEY, run: () => control('key', key - 1) },
    { label: `↺ ${t('room.keyReset')}`, disabled: key === 0, run: () => control('key', 0) },
  ]
}
