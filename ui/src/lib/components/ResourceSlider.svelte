<script lang="ts">
  let { label, value = $bindable(), min = 0, max = 16384, step = 256, display }:
    {
      label: string;
      value: number;
      min?: number;
      max?: number;
      step?: number;
      display: (v: number) => string;
    } = $props();

  let sliderEl: HTMLInputElement;

  // Update fill on any value change (including initial load)
  $effect(() => {
    if (!sliderEl) return;
    const pct = ((value - min) / (max - min)) * 100;
    sliderEl.style.background = `linear-gradient(to right, var(--accent) 0%, var(--accent) ${pct}%, var(--border-dim) ${pct}%, var(--border-dim) 100%)`;
  });
</script>

<div class="resource">
  <div class="resource-header">
    <span class="label">{label}</span>
    <span class="resource-value">{display(value)}</span>
  </div>
  <input
    type="range" class="slider"
    {min} {max} {step}
    bind:value={value}
    bind:this={sliderEl}
  >
</div>

<style>
  .resource-header {
    display: flex; align-items: center; justify-content: space-between;
    margin-bottom: 8px;
  }
  .resource-header .label { margin-bottom: 0; }
  .resource-value {
    font-size: 0.78rem; font-family: var(--font-mono);
    font-weight: 500; color: var(--text-primary);
  }

  .slider {
    -webkit-appearance: none; appearance: none;
    width: 100%; height: 4px; border-radius: 2px;
    background: var(--border-dim); outline: none; cursor: pointer;
  }
  .slider::-webkit-slider-thumb {
    -webkit-appearance: none; appearance: none;
    width: 16px; height: 16px; border-radius: 50%;
    background: var(--accent); cursor: pointer;
    box-shadow: 0 0 8px rgba(232,114,42,0.25);
  }
  .slider::-moz-range-thumb {
    width: 16px; height: 16px; border-radius: 50%; border: none;
    background: var(--accent); cursor: pointer;
    box-shadow: 0 0 8px rgba(232,114,42,0.25);
  }
</style>
