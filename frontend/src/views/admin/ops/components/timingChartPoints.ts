/**
 * 时序折线的点可见性。
 *
 * 背景：三张趋势图与分组图都设了 `spanGaps: false`（不伪造跨空桶连线）。当一个窗口里
 * 只有一个有效桶、前后都是 null 时，Chart.js 不画线；如果 `pointRadius` 还是 0，
 * 「有样本」就会被画成完全空白的图——用户与验收都无法区分「没数据」和「数据被隐藏」。
 *
 * 因此有效点给一个最小可点半径（2），null 点保持 0：
 * - 断线语义不变（不连线、不插值）；
 * - 孤立样本仍然可见；
 * - hover 半径 4，便于 tooltip 命中，但不放大视觉噪声。
 */
export const TIMING_POINT_RADIUS = 2
export const TIMING_POINT_HOVER_RADIUS = 4

/** Chart.js scriptable 选项：仅对有效数值绘制点，null 保持不可见。 */
export const timingPointRadius = (context: { raw?: unknown }): number =>
  typeof context.raw === 'number' && Number.isFinite(context.raw) ? TIMING_POINT_RADIUS : 0
