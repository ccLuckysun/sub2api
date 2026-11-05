<template>
  <button
    type="button"
    @click="toggle"
    class="neo-toggle relative inline-flex h-6 w-11 flex-shrink-0 cursor-pointer rounded-full border-2"
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
.neo-toggle { border-color: var(--neo-border); background: var(--neo-divider); transition: background-color var(--neo-duration) ease; }
.neo-toggle-active { background: var(--neo-accent); }
.neo-toggle-thumb { background: var(--neo-surface); border: 1px solid var(--neo-border); transition: transform var(--neo-duration) var(--neo-ease); }
</style>
