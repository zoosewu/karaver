import { sheet, type SheetAction } from './dialog.svelte'
import { t } from './i18n'
import type { RoomState } from './types'

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

type Control = (action: string, value?: number) => unknown

/**
 * The "⋯" playback sheet (phone requester and admin). It stays open and rebuilds
 * from the live room state, so the volume and key can be adjusted repeatedly.
 * Volume belongs to the TV that is playing; vocals and key to the current song.
 */
export function openPlayerControls(getRoom: () => RoomState | null, control: Control) {
  sheet(
    t('room.moreControls'),
    () => {
      const room = getRoom()
      if (!room) return []
      const p = room.player
      const cur = room.current
      const actions: SheetAction[] = []
      if (cur?.hasOriginal)
        actions.push({ label: `🎤 ${p.vocal ? t('room.vocalSwitchOff') : t('room.vocalSwitchOn')}`, run: () => control('vocal', p.vocal ? 0 : 1) })
      if (p.online)
        actions.push(
          { label: `🔊 ${t('room.volumeUp')}`, disabled: p.volume >= 100, run: () => control('volume', Math.min(100, p.volume + 10)) },
          { label: `🔉 ${t('room.volumeDown')}`, disabled: p.volume <= 0, run: () => control('volume', Math.max(0, p.volume - 10)) },
        )
      actions.push({ label: `▦ ${p.showQR ? t('room.qrHide') : t('room.qrShow')}`, run: () => control('qr', p.showQR ? 0 : 1) })
      if (cur && p.keyAvailable) actions.push(...keyActions(p.key, control))
      return actions
    },
    () => {
      const room = getRoom()
      if (!room) return ''
      const volume = room.player.online ? t('room.volumeNow', { n: room.player.volume }) : t('room.noPlayer')
      if (!room.current || !room.player.keyAvailable) return volume
      const key = keyLabel(room.player.key)
      return `${volume} · ${room.player.vocal && room.player.key !== 0 ? `${key} ${t('room.keyVocal')}` : key}`
    },
    { keepOpen: true },
  )
}
