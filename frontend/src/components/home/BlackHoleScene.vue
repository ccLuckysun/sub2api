<script setup lang="ts">
import { onBeforeUnmount, onMounted, shallowRef, useTemplateRef, watch } from 'vue'
import { createBlackHoleController, type SceneDiagnostics, type SceneMotion } from './blackhole/controller'
import poster from './blackhole/poster.svg'

const props = withDefaults(defineProps<{ motion: SceneMotion; paused?: boolean }>(), { paused: false })
const host = useTemplateRef<HTMLDivElement>('host')
const canvas = useTemplateRef<HTMLCanvasElement>('canvas')
const state = shallowRef<Readonly<SceneDiagnostics>>()
let controller: ReturnType<typeof createBlackHoleController> | undefined

onMounted(() => {
  if (!host.value || !canvas.value) return
  controller = createBlackHoleController(host.value, canvas.value,
    { motion: props.motion, paused: props.paused }, value => { state.value = value })
})
watch(() => [props.motion, props.paused] as const, ([motion, paused]) => controller?.update({ motion, paused }))
onBeforeUnmount(() => { controller?.dispose(); controller = undefined })

defineExpose({
  setDeepening: (active: boolean) => controller?.setDeepening(active),
  replay: () => controller?.replay()
})
</script>

<template>
  <div
    ref="host"
    class="blackhole-scene"
    aria-hidden="true"
    :data-renderer="state?.renderer ?? 'poster'"
    :data-motion="state?.motion ?? 'off'"
    :data-running="state?.running ?? false"
    :data-quality="state?.quality ?? 0"
    :data-fallback="state?.reason || undefined"
  >
    <img class="blackhole-scene__poster" :src="poster" alt="" draggable="false">
    <canvas
      ref="canvas"
      class="blackhole-scene__canvas"
      :class="{ 'blackhole-scene__canvas--ready': state?.renderer === 'webgl' }"
      aria-hidden="true"
    />
  </div>
</template>

<style scoped>
.blackhole-scene {
  position: absolute;
  inset: 0;
  overflow: hidden;
  pointer-events: none;
  background: #030304;
  isolation: isolate;
  contain: strict;
}
.blackhole-scene__poster {
  position: absolute;
  left: 50%;
  top: 40.5%;
  height: 100%;
  width: auto;
  max-width: none;
  transform: translate(-50%, -50%);
  user-select: none;
}
.blackhole-scene__canvas {
  position: absolute;
  inset: 0;
  display: block;
  height: 100%;
  width: 100%;
  visibility: hidden;
}
.blackhole-scene__canvas--ready { visibility: visible; }
@media (max-width: 809px) {
  .blackhole-scene__poster { top: 33.5%; }
}
/* A CSS safety net also covers the short interval before the media listener runs. */
@media (prefers-reduced-motion: reduce) {
  .blackhole-scene__canvas { visibility: hidden; }
}
</style>
