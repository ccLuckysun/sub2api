import { vi } from 'vitest'

export function makeWebGLMock() {
  return {
    VERTEX_SHADER: 0x8b31, FRAGMENT_SHADER: 0x8b30,
    COMPILE_STATUS: 0x8b81, LINK_STATUS: 0x8b82, TRIANGLES: 4,
    createShader: vi.fn(() => ({})), shaderSource: vi.fn(), compileShader: vi.fn(),
    getShaderParameter: vi.fn(() => true), deleteShader: vi.fn(),
    createProgram: vi.fn(() => ({})), attachShader: vi.fn(), detachShader: vi.fn(),
    linkProgram: vi.fn(), getProgramParameter: vi.fn(() => true), deleteProgram: vi.fn(),
    createVertexArray: vi.fn(() => ({})), deleteVertexArray: vi.fn(),
    getUniformLocation: vi.fn((_program: unknown, name: string) => ({ name })),
    viewport: vi.fn(), useProgram: vi.fn(), bindVertexArray: vi.fn(),
    uniform2f: vi.fn(), uniform1f: vi.fn(), uniform1i: vi.fn(),
    drawArrays: vi.fn(), isContextLost: vi.fn(() => false)
  }
}
