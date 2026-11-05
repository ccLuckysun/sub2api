import { afterEach, describe, expect, it, vi } from 'vitest'
import { makeRelativisticRenderer, type RenderFrame } from '../renderer'
import { makeWebGLMock } from './webglMock'

afterEach(() => vi.restoreAllMocks())

function setup() {
  const canvas = document.createElement('canvas')
  canvas.width = 800
  canvas.height = 600
  const gl = makeWebGLMock()
  vi.spyOn(canvas, 'getContext').mockReturnValue(gl as unknown as WebGL2RenderingContext)
  return { canvas, gl }
}
const frame: RenderFrame = {
  time: 2, deep: .5, pointer: { x: .1, y: -.1 }, intro: 1, phone: false, steps: 180
}

describe('independent relativistic WebGL renderer', () => {
  it('draws a full-screen triangle with the approved camera and bounded ray steps', () => {
    const { canvas, gl } = setup()
    const renderer = makeRelativisticRenderer(canvas)!
    renderer.render(frame)
    expect(gl.drawArrays).toHaveBeenCalledWith(gl.TRIANGLES, 0, 3)
    expect(gl.viewport).toHaveBeenCalledWith(0, 0, 800, 600)
    expect(gl.uniform2f).toHaveBeenCalledWith({ name: 'uCenter' }, .5, .595)
    expect(gl.uniform1f).toHaveBeenCalledWith({ name: 'uDeep' }, .5)
    expect(gl.uniform1i).toHaveBeenCalledWith({ name: 'uSteps' }, 180)
    renderer.render({ ...frame, phone: true, steps: 900 })
    expect(gl.uniform2f).toHaveBeenLastCalledWith({ name: 'uCenter' }, .5, .665)
    expect(gl.uniform1i).toHaveBeenLastCalledWith({ name: 'uSteps' }, 180)
    renderer.render({ ...frame, steps: 1 })
    expect(gl.uniform1i).toHaveBeenLastCalledWith({ name: 'uSteps' }, 104)
    renderer.dispose()
    renderer.dispose()
    expect(gl.detachShader).toHaveBeenCalledTimes(2)
    expect(gl.deleteShader).toHaveBeenCalledTimes(2)
    expect(gl.deleteProgram).toHaveBeenCalledTimes(1)
    expect(gl.deleteVertexArray).toHaveBeenCalledTimes(1)
    expect(() => renderer.render(frame)).toThrow()
  })

  it('returns fallback when WebGL2 is absent or context creation throws', () => {
    const { canvas } = setup()
    vi.mocked(canvas.getContext).mockReturnValue(null)
    expect(makeRelativisticRenderer(canvas)).toBeNull()
    vi.mocked(canvas.getContext).mockImplementation(() => { throw new Error('blocked') })
    expect(makeRelativisticRenderer(canvas)).toBeNull()
  })

  it.each(['vertex', 'fragment', 'link', 'vao'] as const)('cleans partially allocated resources on %s failure', failure => {
    const { canvas, gl } = setup()
    if (failure === 'vertex') gl.getShaderParameter.mockReturnValue(false)
    if (failure === 'fragment') gl.getShaderParameter.mockReturnValueOnce(true).mockReturnValueOnce(false)
    if (failure === 'link') gl.getProgramParameter.mockReturnValue(false)
    if (failure === 'vao') gl.createVertexArray.mockReturnValue(null as unknown as object)
    expect(makeRelativisticRenderer(canvas)).toBeNull()
    expect(gl.deleteShader).toHaveBeenCalledTimes(failure === 'vertex' ? 1 : 2)
    expect(gl.deleteProgram).toHaveBeenCalledTimes(1)
    expect(gl.drawArrays).not.toHaveBeenCalled()
  })

  it('rejects a lost context before drawing', () => {
    const { canvas, gl } = setup()
    const renderer = makeRelativisticRenderer(canvas)!
    gl.isContextLost.mockReturnValue(true)
    expect(() => renderer.render(frame)).toThrow('Renderer unavailable')
    expect(gl.drawArrays).not.toHaveBeenCalled()
    renderer.dispose()
  })
})
