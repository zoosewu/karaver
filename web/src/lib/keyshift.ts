// Key change on the TV, without changing the tempo.
//
// A delay-line pitch shifter: the signal is written into a ring buffer and read
// back by two taps that drift relative to the write head (faster = higher,
// slower = lower). The taps are half a window apart and cross-faded with
// complementary Hann gains, so each one fades out just before it wraps around.
// Cheap enough for TV boxes, and fine for the ±6 semitones the room allows.
//
// It runs in a ScriptProcessorNode rather than an AudioWorklet on purpose:
// AudioWorklet only exists in secure contexts, and TVs usually open the player
// over plain http on the LAN. The extra buffering adds ~50 ms, which is not
// noticeable when singing along.

const WINDOW_SECONDS = 0.05
const BUFFER_SIZE = 1024

export class PitchShifter {
  private buf: Float32Array
  private mask: number
  private write = 0
  private delay = 0
  private ratio = 1
  private readonly window: number

  constructor(sampleRate: number) {
    this.window = Math.round(sampleRate * WINDOW_SECONDS)
    let size = 1
    while (size < this.window * 2 + 4) size *= 2
    this.buf = new Float32Array(size)
    this.mask = size - 1
  }

  setSemitones(n: number) {
    this.ratio = Math.pow(2, n / 12)
  }

  process(input: Float32Array, output: Float32Array) {
    const W = this.window
    const half = W / 2
    for (let i = 0; i < input.length; i++) {
      this.buf[this.write] = input[i]
      const d1 = this.delay
      const d2 = d1 + half >= W ? d1 + half - W : d1 + half
      const g1 = Math.sin((Math.PI * d1) / W) ** 2
      const g2 = 1 - g1 // = sin²(π·d2/W), since d2 is half a window away
      output[i] = this.read(this.write - d1) * g1 + this.read(this.write - d2) * g2
      // The taps move at `ratio` samples per sample; the write head moves at 1.
      let d = d1 + 1 - this.ratio
      if (d < 0) d += W
      else if (d >= W) d -= W
      this.delay = d
      this.write = (this.write + 1) & this.mask
    }
  }

  private read(pos: number): number {
    const i = Math.floor(pos)
    const frac = pos - i
    const a = this.buf[i & this.mask]
    const b = this.buf[(i + 1) & this.mask]
    return a + (b - a) * frac
  }
}

/**
 * Routes the player's media elements through the shifter. At 0 semitones the
 * shifter is bypassed entirely (no added latency).
 */
export class KeyShift {
  readonly ctx: AudioContext
  /** Last node before the speakers; tests read the pitch from it. */
  readonly analyser: AnalyserNode
  private processor: ScriptProcessorNode
  private shifters: PitchShifter[]
  private sources: { el: HTMLMediaElement; node: MediaElementAudioSourceNode }[] = []
  private semitones = 0

  constructor() {
    this.ctx = new AudioContext()
    this.analyser = this.ctx.createAnalyser()
    this.analyser.fftSize = 8192
    this.analyser.connect(this.ctx.destination)
    this.processor = this.ctx.createScriptProcessor(BUFFER_SIZE, 2, 2)
    this.shifters = [new PitchShifter(this.ctx.sampleRate), new PitchShifter(this.ctx.sampleRate)]
    this.processor.onaudioprocess = (e) => {
      for (let ch = 0; ch < 2; ch++) {
        this.shifters[ch].process(e.inputBuffer.getChannelData(ch), e.outputBuffer.getChannelData(ch))
      }
    }
    this.processor.connect(this.analyser)
  }

  /** True once audio can actually play (needs an earlier tap, or kiosk mode). */
  get running(): boolean {
    return this.ctx.state === 'running'
  }

  async resume() {
    if (this.ctx.state !== 'running') await this.ctx.resume().catch(() => {})
  }

  /** Takes over an element's audio. Each element can only be attached once. */
  attach(el: HTMLMediaElement) {
    this.sources = this.sources.filter((s) => {
      if (s.el.isConnected) return true
      s.node.disconnect() // element left the page (next song): drop its node
      return false
    })
    if (this.sources.some((s) => s.el === el)) return
    const node = this.ctx.createMediaElementSource(el)
    this.sources.push({ el, node })
    this.route(node)
  }

  setSemitones(n: number) {
    this.semitones = n
    for (const s of this.shifters) s.setSemitones(n)
    for (const s of this.sources) this.route(s.node)
  }

  private route(node: MediaElementAudioSourceNode) {
    node.disconnect()
    node.connect(this.semitones === 0 ? this.analyser : this.processor)
  }

  close() {
    this.ctx.close().catch(() => {})
  }
}
