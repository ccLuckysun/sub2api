<template>
  <button
    type="button"
    @click="toggle"
    class="neo-toggle relative inline-flex h-6 w-11 flex-shrink-0 cursor-pointer rounded-full border"
    :class="{ 'neo-toggle-active': modelValue }"
    role="switch"
    :aria-checked="modelValue"
  >
    <span
      class="neo-toggle-thumb pointer-events-none inline-block h-5 w-5 rounded-full"
      :class="[modelValue ? 'translate-x-5' : 'translate-x-0']"
    />
  </button>
</template>

<script setup lang="ts">
const props = defineProps<{
  modelValue: boolean
}>()

const emit = defineEmits<{
  (e: 'update:modelValue', value: boolean): void
}>()

function toggle() {
  emit('update:modelValue', !props.modelValue)
}
</script>

<style scoped>
.neo-toggle { align-items: center; padding: 1px; border-color: var(--ui-control-border); background: var(--ui-surface-raised); transition: background-color var(--ui-duration) ease; }
.neo-toggle-active { background: var(--ui-accent); }
.neo-toggle-thumb { background: var(--ui-ink); border: 1px solid var(--ui-control-border); transition: transform var(--ui-duration) var(--ui-ease); }
.neo-toggle-active .neo-toggle-thumb { background: var(--ui-accent-ink); }
</style>
