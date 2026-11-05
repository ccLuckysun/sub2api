import { fragmentShader, vertexShader } from './shaders'

export interface RenderFrame {
  time: number
  deep: number
  pointer: { x: number; y: number }
  intro: number
  phone: boolean
  steps: number
}

export interface RelativisticRenderer {
  render(frame: RenderFrame): void
  dispose(): void
}

/** All partially allocated resources belong to this factory, including failed links. */
export function makeRelativisticRenderer(canvas: HTMLCanvasElement): RelativisticRenderer | null {
  let gl: WebGL2RenderingContext | null = null
  let program: WebGLProgram | null = null
  let vao: WebGLVertexArrayObject | null = null
  const shaders: WebGLShader[] = []
  let disposed = false
  function dispose() {
    if (disposed) return
    disposed = true
    if (!gl) return
    for (const shader of shaders) gl.deleteShader(shader)
    shaders.length = 0
    if (vao) gl.deleteVertexArray(vao)
    if (program) gl.deleteProgram(program)
    vao = program = null
  }
  try {
    gl = canvas.getContext('webgl2', {
      alpha: false, antialias: false, depth: false, stencil: false,
      powerPreference: 'low-power', preserveDrawingBuffer: false
    })
    if (!gl || gl.isContextLost()) return null
    const compile = (type: number, source: string) => {
      const shader = gl!.createShader(type)
      if (!shader) throw new Error('Shader allocation failed')
      shaders.push(shader)
      gl!.shaderSource(shader, source)
      gl!.compileShader(shader)
      if (!gl!.getShaderParameter(shader, gl!.COMPILE_STATUS)) throw new Error('Shader compilation failed')
      return shader
    }
    program = gl.createProgram()
    if (!program) throw new Error('Program allocation failed')
    gl.attachShader(program, compile(gl.VERTEX_SHADER, vertexShader))
    gl.attachShader(program, compile(gl.FRAGMENT_SHADER, fragmentShader))
    gl.linkProgram(program)
    if (!gl.getProgramParameter(program, gl.LINK_STATUS)) throw new Error('Program link failed')
    // Detach before deleting so the linked program does not retain shader objects.
    for (const shader of shaders) {
      gl.detachShader(program, shader)
      gl.deleteShader(shader)
    }
    shaders.length = 0
    vao = gl.createVertexArray()
    if (!vao) throw new Error('Vertex array allocation failed')
    const uniforms = Object.fromEntries(
      ['uResolution', 'uPointer', 'uCenter', 'uTime', 'uDeep', 'uIntro', 'uSteps']
        .map(name => [name, gl!.getUniformLocation(program!, name)])
    )
    return {
      render(frame) {
        if (disposed || gl!.isContextLost()) throw new Error('Renderer unavailable')
        gl!.viewport(0, 0, canvas.width, canvas.height)
        gl!.useProgram(program)
        gl!.bindVertexArray(vao)
        gl!.uniform2f(uniforms.uResolution, canvas.width, canvas.height)
        gl!.uniform2f(uniforms.uPointer, frame.pointer.x, frame.pointer.y)
        gl!.uniform2f(uniforms.uCenter, .5, frame.phone ? .665 : .595)
        gl!.uniform1f(uniforms.uTime, frame.time)
        gl!.uniform1f(uniforms.uDeep, frame.deep)
        gl!.uniform1f(uniforms.uIntro, frame.intro)
        gl!.uniform1i(uniforms.uSteps, Math.max(104, Math.min(180, Math.round(frame.steps))))
        gl!.drawArrays(gl!.TRIANGLES, 0, 3)
      },
      dispose
    }
  } catch {
    dispose()
    return null
  }
}
