import { makeRelativisticRenderer, type RelativisticRenderer } from './renderer'

export type SceneMotion = 'rich' | 'moderate' | 'off'
export interface SceneOptions { motion: SceneMotion; paused: boolean }
export interface SceneDiagnostics {
  readonly renderer: 'poster' | 'webgl'
  readonly motion: SceneMotion
  readonly running: boolean
  readonly visible: boolean
  readonly quality: number
  readonly reason: 'disabled' | 'reduced-motion' | 'unavailable' | 'context-lost' | ''
}

/** Scene-local lifecycle. Nothing here owns page content, controls, or keyboard input. */
export function createBlackHoleController(
  host: HTMLElement,
  canvas: HTMLCanvasElement,
  initial: SceneOptions,
  onState: (state: Readonly<SceneDiagnostics>) => void = () => {}
) {
  let options = { ...initial }
  const media = window.matchMedia?.('(prefers-reduced-motion: reduce)')
  let reduced = media?.matches ?? false
  let visible = false
  let disposed = false
  let renderer: RelativisticRenderer | null = null
  let failed: SceneDiagnostics['reason'] = ''
  let rendered = false
  let frame: number | null = null
  let width = 0
  let height = 0
  let last: number | null = null
  let time = 0
  let introTime = 0
  let deep = 0
  let holding = false
  let quality = 0
  let slowMs = 0
  let fastMs = 0
  let qualityChangedAt = -Infinity
  const pointer = { x: 0, y: 0 }
  const smooth = { x: 0, y: 0 }
  const cleanup: Array<() => void> = []
  const motion = (): SceneMotion => reduced ? 'off' : options.motion === 'rich' ? 'rich' : options.motion === 'moderate' ? 'moderate' : 'off'
  const active = () => !disposed && visible && !document.hidden && motion() !== 'off' && !failed && width > 0 && height > 0
  const interactive = () => active() && !options.paused && motion() === 'rich'
  const diagnostics = (): Readonly<SceneDiagnostics> => Object.freeze({
    renderer: rendered ? 'webgl' : 'poster', motion: motion(), running: frame !== null,
    visible, quality, reason: reduced ? 'reduced-motion' : motion() === 'off' ? 'disabled' : failed
  })
  const emit = () => { if (!disposed) onState(diagnostics()) }
  const listen = (target: EventTarget, name: string, handler: EventListener, passive = false) => {
    target.addEventListener(name, handler, { passive })
    cleanup.push(() => target.removeEventListener(name, handler))
  }
  function stop() {
    if (frame !== null) window.cancelAnimationFrame(frame)
    frame = null
    last = null
    slowMs = fastMs = 0
  }
  function resetInteraction() {
    holding = false
    deep = 0
    pointer.x = pointer.y = smooth.x = smooth.y = 0
  }
  function release() {
    renderer?.dispose()
    renderer = null
    rendered = false
    // Release the backing store as well as GPU objects in static mode/unmount.
    canvas.width = canvas.height = 1
  }
  function sizeCanvas() {
    const pixels = (motion() === 'rich' ? [480000, 320000, 180000] : [240000, 160000, 100000])[quality]
    const ratio = Math.min(window.devicePixelRatio || 1, motion() === 'rich' ? 1.3 : 1,
      Math.sqrt(pixels / (width * height)), 2048 / Math.max(width, height))
    const w = Math.max(1, Math.floor(width * ratio))
    const h = Math.max(1, Math.floor(height * ratio))
    if (canvas.width !== w) canvas.width = w
    if (canvas.height !== h) canvas.height = h
  }
  function fail(reason: SceneDiagnostics['reason']) {
    failed = reason
    stop()
    resetInteraction()
    release()
    emit()
  }
  function render() {
    if (!renderer) return
    try {
      renderer.render({
        time, deep: motion() === 'rich' ? deep : 0,
        pointer: motion() === 'rich' ? smooth : { x: 0, y: 0 },
        intro: Math.min(1, introTime / 2.3),
        phone: width < 810,
        steps: (motion() === 'rich' ? [180, 156, 132] : [140, 120, 104])[quality]
      })
      if (!rendered) { rendered = true; emit() }
    } catch { fail('unavailable') }
  }
  function adapt(delta: number, now: number) {
    // Ignore isolated stalls. Downgrade after 2s slow, recover only after 12s fast;
    // an 8s cooldown prevents oscillation between resolution/step tiers.
    slowMs = delta > 40 ? slowMs + delta : 0
    fastMs = delta < 25 ? fastMs + delta : 0
    if (now - qualityChangedAt < 8000) return
    const next = slowMs >= 2000 ? Math.min(2, quality + 1) : fastMs >= 12000 ? Math.max(0, quality - 1) : quality
    if (next !== quality) {
      quality = next
      qualityChangedAt = now
      slowMs = fastMs = 0
      sizeCanvas()
    }
  }
  function draw(now: number) {
    frame = null
    if (!active() || options.paused) { stop(); emit(); return }
    const delta = last === null ? 1000 / 60 : Math.max(0, now - last)
    last = now
    const dt = Math.min(delta / 1000, .05)
    time += dt
    introTime += dt
    deep += ((holding ? 1 : 0) - deep) * Math.min(1, dt * 3.4)
    const ease = 1 - Math.exp(-dt * 2.8)
    smooth.x += (pointer.x - smooth.x) * ease
    smooth.y += (pointer.y - smooth.y) * ease
    const previousQuality = quality
    adapt(Math.min(delta, 250), now)
    render()
    schedule()
    if (quality !== previousQuality) emit()
  }
  function schedule() {
    if (frame === null && active() && !options.paused && renderer) frame = window.requestAnimationFrame(draw)
  }
  function sync() {
    if (disposed) return
    stop()
    if (!interactive()) resetInteraction()
    if (motion() === 'off') { release(); emit(); return }
    if (!active()) { emit(); return }
    if (!renderer) renderer = makeRelativisticRenderer(canvas)
    if (!renderer) { fail('unavailable'); return }
    sizeCanvas()
    render()
    schedule()
    emit()
  }
  function measure() {
    if (disposed) return
    const rect = host.getBoundingClientRect()
    width = Number.isFinite(rect.width) ? Math.max(0, rect.width) : 0
    height = Number.isFinite(rect.height) ? Math.max(0, rect.height) : 0
    sync()
  }
  let intersection: IntersectionObserver | undefined
  if (typeof IntersectionObserver !== 'undefined') {
    intersection = new IntersectionObserver(entries => {
      if (disposed) return
      for (const entry of entries) {
        if (entry.target !== host) continue
        visible = entry.isIntersecting && entry.intersectionRatio > 0
        if (!visible) resetInteraction()
        measure()
      }
    }, { threshold: .01 })
    intersection.observe(host)
    cleanup.push(() => intersection?.disconnect())
  } else {
    // Conservative visibility detection when observers are unavailable.
    const check = () => {
      const r = host.getBoundingClientRect()
      visible = r.bottom > 0 && r.right > 0 && r.top < window.innerHeight && r.left < window.innerWidth
      measure()
    }
    listen(window, 'scroll', check, true)
    listen(window, 'resize', check, true)
    check()
  }
  if (typeof ResizeObserver !== 'undefined') {
    const resize = new ResizeObserver(measure)
    resize.observe(host)
    cleanup.push(() => resize.disconnect())
  } else listen(window, 'resize', measure, true)
  listen(document, 'visibilitychange', () => {
    if (document.hidden) resetInteraction()
    measure()
  })
  const mediaChange = () => { reduced = media?.matches ?? false; sync() }
  if (media?.addEventListener) {
    media.addEventListener('change', mediaChange)
    cleanup.push(() => media.removeEventListener('change', mediaChange))
  } else if (media?.addListener) {
    media.addListener(mediaChange)
    cleanup.push(() => media.removeListener(mediaChange))
  }
  // Observe only our local hero ancestor; never capture global keyboard/Space input.
  const pointerHost = host.parentElement ?? host
  listen(pointerHost, 'pointermove', event => {
    const e = event as PointerEvent
    if (!interactive() || e.pointerType === 'touch') return
    const b = host.getBoundingClientRect()
    if (!b.width || !b.height) return
    pointer.x = Math.max(-1, Math.min(1, (e.clientX - b.left) / b.width * 2 - 1))
    pointer.y = Math.max(-1, Math.min(1, (e.clientY - b.top) / b.height * 2 - 1))
  }, true)
  listen(pointerHost, 'pointerleave', () => { pointer.x = pointer.y = 0 }, true)
  listen(canvas, 'webglcontextlost', () => { if (!disposed) fail('context-lost') })
  emit()
  return {
    update(next: SceneOptions) { if (!disposed) { options = { ...next }; sync() } },
    setDeepening(value: boolean) { holding = value && interactive() },
    replay() {
      if (!active() || options.paused) return
      time = introTime = 0
      resetInteraction()
      sync()
    },
    get diagnostics() { return diagnostics() },
    dispose() {
      if (disposed) return
      disposed = true
      stop()
      for (const remove of cleanup.splice(0)) remove()
      resetInteraction()
      release()
    }
  }
}
