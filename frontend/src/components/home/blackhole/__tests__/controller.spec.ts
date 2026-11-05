import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import BlackHoleScene from '../../BlackHoleScene.vue'
import { createBlackHoleController, type SceneOptions } from '../controller'
import { makeWebGLMock } from './webglMock'

let intersections: IntersectionObserverCallback[]
let resizes: ResizeObserverCallback[]
let disconnectIntersection: ReturnType<typeof vi.fn>
let disconnectResize: ReturnType<typeof vi.fn>
let callbacks: Map<number, FrameRequestCallback>
let nextId: number
let now: number
let hidden: boolean
let rect: DOMRect
let media: MediaQueryList
let mediaEvents: EventTarget
let gl: ReturnType<typeof makeWebGLMock>
let getContext: ReturnType<typeof vi.spyOn>
const disposers: Array<() => void> = []

beforeEach(() => {
  intersections = []
  resizes = []
  disconnectIntersection = vi.fn()
  disconnectResize = vi.fn()
  callbacks = new Map()
  nextId = 0
  now = 0
  hidden = false
  rect = { x: 0, y: 0, top: 0, left: 0, right: 1440, bottom: 900, width: 1440, height: 900, toJSON: () => ({}) }
  vi.spyOn(HTMLElement.prototype, 'getBoundingClientRect').mockImplementation(() => rect)
  vi.spyOn(document, 'hidden', 'get').mockImplementation(() => hidden)
  vi.stubGlobal('devicePixelRatio', 3)
  vi.stubGlobal('requestAnimationFrame', vi.fn((cb: FrameRequestCallback) => {
    callbacks.set(++nextId, cb)
    return nextId
  }))
  vi.stubGlobal('cancelAnimationFrame', vi.fn((id: number) => callbacks.delete(id)))
  vi.stubGlobal('IntersectionObserver', class {
    constructor(cb: IntersectionObserverCallback) { intersections.push(cb) }
    observe = vi.fn()
    disconnect = disconnectIntersection
  })
  vi.stubGlobal('ResizeObserver', class {
    constructor(cb: ResizeObserverCallback) { resizes.push(cb) }
    observe = vi.fn()
    disconnect = disconnectResize
  })
  mediaEvents = new EventTarget()
  media = {
    matches: false,
    addEventListener: vi.fn((...args: Parameters<EventTarget['addEventListener']>) => mediaEvents.addEventListener(...args)),
    removeEventListener: vi.fn((...args: Parameters<EventTarget['removeEventListener']>) => mediaEvents.removeEventListener(...args))
  } as unknown as MediaQueryList
  vi.stubGlobal('matchMedia', vi.fn(() => media))
  gl = makeWebGLMock()
  getContext = vi.spyOn(HTMLCanvasElement.prototype, 'getContext').mockReturnValue(gl as unknown as WebGL2RenderingContext)
})

afterEach(() => {
  for (const dispose of disposers.splice(0)) dispose()
  document.body.replaceChildren()
  vi.restoreAllMocks()
  vi.unstubAllGlobals()
})

function setup(options: SceneOptions = { motion: 'rich', paused: false }) {
  const parent = document.createElement('section')
  const host = document.createElement('div')
  const canvas = document.createElement('canvas')
  parent.append(host)
  host.append(canvas)
  document.body.append(parent)
  const onState = vi.fn()
  const controller = createBlackHoleController(host, canvas, options, onState)
  disposers.push(() => controller.dispose())
  return { parent, host, canvas, controller, onState }
}
function visible(host: Element, value: boolean) {
  intersections.at(-1)!([{ target: host, isIntersecting: value, intersectionRatio: value ? 1 : 0 } as IntersectionObserverEntry], {} as IntersectionObserver)
}
function tick(ms = 16) {
  now += ms
  const pending = [...callbacks.values()]
  callbacks.clear()
  for (const callback of pending) callback(now)
}
function run(count: number, ms = 16) { for (let n = 0; n < count; n++) tick(ms) }
function reduce(value: boolean) {
  Object.defineProperty(media, 'matches', { configurable: true, value })
  mediaEvents.dispatchEvent(new Event('change'))
}
function uniform(name: string) {
  return gl.uniform1f.mock.calls.filter(([location]) => location.name === name).at(-1)?.[1]
}
function move(target: Element) {
  const event = new MouseEvent('pointermove', { clientX: 1440, clientY: 900, bubbles: true })
  Object.defineProperty(event, 'pointerType', { value: 'mouse' })
  target.dispatchEvent(event)
}

describe('black-hole scene lifecycle', () => {
  it('initializes only after intersection, caps pixels, and pauses offscreen/hidden', () => {
    const { host, canvas, controller } = setup()
    expect(getContext).not.toHaveBeenCalled()
    expect(callbacks.size).toBe(0)
    visible(host, true)
    expect(getContext).toHaveBeenCalledTimes(1)
    expect(canvas.width * canvas.height).toBeLessThanOrEqual(480000)
    expect(controller.diagnostics).toMatchObject({ renderer: 'webgl', running: true })
    tick()
    const before = gl.drawArrays.mock.calls.length
    visible(host, false)
    expect(callbacks.size).toBe(0)
    tick()
    expect(gl.drawArrays).toHaveBeenCalledTimes(before)
    visible(host, true)
    hidden = true
    document.dispatchEvent(new Event('visibilitychange'))
    expect(callbacks.size).toBe(0)
    const hiddenDraws = gl.drawArrays.mock.calls.length
    resizes[0]([], {} as ResizeObserver)
    expect(gl.drawArrays).toHaveBeenCalledTimes(hiddenDraws)
    hidden = false
    document.dispatchEvent(new Event('visibilitychange'))
    expect(callbacks.size).toBe(1)
    expect(getContext).toHaveBeenCalledTimes(1)
  })

  it('does not initialize an intersecting scene while the document is hidden or zero-size', () => {
    const { host } = setup()
    hidden = true
    visible(host, true)
    expect(getContext).not.toHaveBeenCalled()
    hidden = false
    rect = { ...rect, width: 0, height: 0 }
    document.dispatchEvent(new Event('visibilitychange'))
    expect(getContext).not.toHaveBeenCalled()
    expect(callbacks.size).toBe(0)
  })

  it.each(['off', 'reduced'] as const)('keeps %s static despite replay/deepen/bad parent updates', kind => {
    if (kind === 'reduced') reduce(true)
    const { host, controller } = setup({ motion: kind === 'off' ? 'off' : 'rich', paused: false })
    visible(host, true)
    controller.setDeepening(true)
    controller.replay()
    if (kind === 'reduced') controller.update({ motion: 'rich', paused: false })
    expect(controller.diagnostics.renderer).toBe('poster')
    expect(controller.diagnostics.motion).toBe('off')
    expect(getContext).not.toHaveBeenCalled()
    expect(callbacks.size).toBe(0)
    expect(gl.drawArrays).not.toHaveBeenCalled()
  })

  it('enforces live system reduction and releases GPU resources, then can resume', () => {
    const { host, controller, canvas } = setup()
    visible(host, true)
    reduce(true)
    expect(controller.diagnostics).toMatchObject({ renderer: 'poster', motion: 'off', reason: 'reduced-motion' })
    expect(gl.deleteProgram).toHaveBeenCalledTimes(1)
    expect(gl.deleteVertexArray).toHaveBeenCalledTimes(1)
    expect(canvas.width).toBe(1)
    expect(callbacks.size).toBe(0)
    controller.replay()
    controller.setDeepening(true)
    expect(callbacks.size).toBe(0)
    reduce(false)
    expect(getContext).toHaveBeenCalledTimes(2)
    expect(controller.diagnostics.running).toBe(true)
  })

  it('renders a complete paused frame with no continuous RAF and ignores interactions', () => {
    const { host, controller } = setup({ motion: 'rich', paused: true })
    visible(host, true)
    expect(gl.drawArrays).toHaveBeenCalledTimes(1)
    expect(uniform('uIntro')).toBe(0)
    controller.setDeepening(true)
    controller.replay()
    run(5)
    expect(gl.drawArrays).toHaveBeenCalledTimes(1)
    expect(callbacks.size).toBe(0)
    controller.update({ motion: 'rich', paused: false })
    expect(callbacks.size).toBe(1)
  })

  it('preserves early entry progress across manual pause and resumes without jumping', () => {
    const { host, controller } = setup()
    visible(host, true)
    run(30)
    const introBeforePause = uniform('uIntro')!
    const timeBeforePause = uniform('uTime')!
    expect(introBeforePause).toBeGreaterThan(0)
    expect(introBeforePause).toBeLessThan(1)

    controller.update({ motion: 'rich', paused: true })
    expect(uniform('uIntro')).toBe(introBeforePause)
    expect(uniform('uTime')).toBe(timeBeforePause)
    expect(callbacks.size).toBe(0)
    const drawsWhilePaused = gl.drawArrays.mock.calls.length
    run(120)
    controller.replay()
    expect(gl.drawArrays).toHaveBeenCalledTimes(drawsWhilePaused)
    expect(uniform('uIntro')).toBe(introBeforePause)

    controller.update({ motion: 'rich', paused: false })
    expect(uniform('uIntro')).toBe(introBeforePause)
    expect(uniform('uTime')).toBe(timeBeforePause)
    tick()
    expect(uniform('uIntro')).toBeGreaterThan(introBeforePause)
    expect(uniform('uIntro')).toBeLessThan(introBeforePause + .02)
    expect(uniform('uTime')).toBeGreaterThan(timeBeforePause)
  })

  it('moderate mode has lower pixel/step budgets and ignores deepening and mouse input', () => {
    const { host, parent, controller, canvas } = setup({ motion: 'moderate', paused: false })
    visible(host, true)
    move(parent)
    controller.setDeepening(true)
    run(60)
    expect(canvas.width * canvas.height).toBeLessThanOrEqual(240000)
    expect(uniform('uDeep')).toBe(0)
    expect(gl.uniform2f).toHaveBeenCalledWith({ name: 'uPointer' }, 0, 0)
    expect(gl.uniform1i).toHaveBeenLastCalledWith({ name: 'uSteps' }, 140)
  })

  it('supports rich parallax/deepening and replay without affecting page events', () => {
    const { host, parent, controller } = setup()
    visible(host, true)
    move(parent)
    controller.setDeepening(true)
    run(90)
    expect(uniform('uDeep')).toBeGreaterThan(.9)
    const pointer = gl.uniform2f.mock.calls.filter(([location]) => location.name === 'uPointer').at(-1)!
    expect(pointer[1]).toBeGreaterThan(.9)
    const key = new KeyboardEvent('keydown', { code: 'Space', cancelable: true })
    window.dispatchEvent(key)
    expect(key.defaultPrevented).toBe(false)
    controller.replay()
    expect(uniform('uTime')).toBe(0)
    expect(uniform('uIntro')).toBe(0)
    expect(uniform('uDeep')).toBe(0)
    controller.update({ motion: 'moderate', paused: false })
    expect(uniform('uDeep')).toBe(0)
    expect(gl.uniform2f).toHaveBeenLastCalledWith({ name: 'uCenter' }, .5, .595)
  })

  it('does not retain held deepening through offscreen suspension', () => {
    const { host, controller } = setup()
    visible(host, true)
    controller.setDeepening(true)
    run(60)
    visible(host, false)
    visible(host, true)
    run(60)
    expect(uniform('uDeep')).toBe(0)
  })

  it.each(['unsupported', 'compile', 'link', 'context-lost', 'draw'] as const)('leaves a static poster and no RAF after %s', failure => {
    if (failure === 'unsupported') getContext.mockReturnValue(null)
    if (failure === 'compile') gl.getShaderParameter.mockReturnValue(false)
    if (failure === 'link') gl.getProgramParameter.mockReturnValue(false)
    const { host, canvas, controller } = setup()
    visible(host, true)
    if (failure === 'context-lost') canvas.dispatchEvent(new Event('webglcontextlost'))
    if (failure === 'draw') {
      gl.drawArrays.mockImplementation(() => { throw new Error('driver failure') })
      tick()
    }
    expect(controller.diagnostics.renderer).toBe('poster')
    expect(callbacks.size).toBe(0)
    expect(canvas.width).toBe(1)
    const attempts = getContext.mock.calls.length
    controller.replay()
    controller.update({ motion: 'rich', paused: false })
    visible(host, false)
    visible(host, true)
    expect(getContext).toHaveBeenCalledTimes(attempts)
    if (failure !== 'unsupported') expect(gl.deleteProgram).toHaveBeenCalledTimes(1)
  })

  it('adapts only after sustained slow frames and recovers with hysteresis', () => {
    const { host, controller, canvas } = setup()
    visible(host, true)
    tick()
    run(10, 50)
    expect(controller.diagnostics.quality).toBe(0)
    run(32, 50)
    expect(controller.diagnostics.quality).toBe(1)
    expect(controller.diagnostics.running).toBe(true)
    expect(canvas.width * canvas.height).toBeLessThanOrEqual(320000)
    run(100, 16)
    expect(controller.diagnostics.quality).toBe(1)
    run(660, 16)
    expect(controller.diagnostics.quality).toBe(0)
    expect(gl.uniform1i).toHaveBeenLastCalledWith({ name: 'uSteps' }, 180)
    // Isolated stalls do not count as sustained load.
    for (let n = 0; n < 40; n++) { tick(70); tick(16) }
    expect(controller.diagnostics.quality).toBe(0)
  })

  it('bounds sustained degradation at the lowest pixel and step tier', () => {
    const { host, controller, canvas } = setup()
    visible(host, true)
    tick()
    run(600, 50)
    expect(controller.diagnostics.quality).toBe(2)
    expect(controller.diagnostics.running).toBe(true)
    expect(canvas.width * canvas.height).toBeLessThanOrEqual(180000)
    expect(gl.uniform1i).toHaveBeenLastCalledWith({ name: 'uSteps' }, 132)
  })

  it('fully disconnects lifecycle resources and cannot restart after unmount', () => {
    const { host, canvas, controller, parent, onState } = setup()
    const removePointer = vi.spyOn(parent, 'removeEventListener')
    const removeDocument = vi.spyOn(document, 'removeEventListener')
    const removeCanvas = vi.spyOn(canvas, 'removeEventListener')
    visible(host, true)
    controller.dispose()
    controller.dispose()
    const count = gl.drawArrays.mock.calls.length
    const states = onState.mock.calls.length
    expect(callbacks.size).toBe(0)
    expect(disconnectIntersection).toHaveBeenCalledTimes(1)
    expect(disconnectResize).toHaveBeenCalledTimes(1)
    expect(media.removeEventListener).toHaveBeenCalledWith('change', expect.any(Function))
    expect(removeDocument).toHaveBeenCalledWith('visibilitychange', expect.any(Function))
    expect(removePointer).toHaveBeenCalledWith('pointermove', expect.any(Function))
    expect(removeCanvas).toHaveBeenCalledWith('webglcontextlost', expect.any(Function))
    expect(gl.deleteShader).toHaveBeenCalledTimes(2)
    expect(gl.deleteProgram).toHaveBeenCalledTimes(1)
    expect(gl.deleteVertexArray).toHaveBeenCalledTimes(1)
    visible(host, true)
    resizes[0]([], {} as ResizeObserver)
    document.dispatchEvent(new Event('visibilitychange'))
    reduce(true)
    controller.replay()
    controller.update({ motion: 'rich', paused: false })
    run(3)
    expect(gl.drawArrays).toHaveBeenCalledTimes(count)
    expect(onState).toHaveBeenCalledTimes(states)
  })

  it('conservatively checks visibility without IntersectionObserver', () => {
    vi.stubGlobal('IntersectionObserver', undefined)
    rect = { ...rect, top: 2000, bottom: 2900 }
    const { controller } = setup()
    expect(getContext).not.toHaveBeenCalled()
    rect = { ...rect, top: 0, bottom: 900 }
    window.dispatchEvent(new Event('scroll'))
    expect(controller.diagnostics.renderer).toBe('webgl')
  })

  it('removes the rendered canvas on context loss while keeping the complete local poster', async () => {
    const wrapper = mount(BlackHoleScene, { props: { motion: 'rich' } })
    visible(wrapper.element, true)
    await wrapper.vm.$nextTick()
    expect(wrapper.find('canvas').classes()).toContain('blackhole-scene__canvas--ready')
    wrapper.find('canvas').element.dispatchEvent(new Event('webglcontextlost'))
    await wrapper.vm.$nextTick()
    expect(wrapper.find('canvas').classes()).not.toContain('blackhole-scene__canvas--ready')
    expect(wrapper.find('img').attributes('src')).toContain('poster.svg')
    expect(wrapper.attributes('data-fallback')).toBe('context-lost')
    expect(callbacks.size).toBe(0)
    wrapper.unmount()
  })

  it('wires Vue props/exposed API, retains a local poster, and disposes on actual unmount', async () => {
    const wrapper = mount(BlackHoleScene, { props: { motion: 'rich' } })
    const host = wrapper.element
    expect(wrapper.find('img').attributes('src')).toContain('poster.svg')
    expect(wrapper.attributes('aria-hidden')).toBe('true')
    visible(host, true)
    await wrapper.vm.$nextTick()
    expect(wrapper.attributes('data-renderer')).toBe('webgl')
    wrapper.vm.setDeepening(true)
    run(60)
    expect(uniform('uDeep')).toBeGreaterThan(.9)
    await wrapper.setProps({ paused: true })
    expect(callbacks.size).toBe(0)
    wrapper.vm.replay()
    await wrapper.setProps({ motion: 'off', paused: false })
    expect(wrapper.attributes('data-renderer')).toBe('poster')
    expect(wrapper.find('img').exists()).toBe(true)
    expect(gl.deleteProgram).toHaveBeenCalledTimes(1)
    wrapper.unmount()
    expect(disconnectIntersection).toHaveBeenCalledTimes(1)
    expect(disconnectResize).toHaveBeenCalledTimes(1)
  })
})
