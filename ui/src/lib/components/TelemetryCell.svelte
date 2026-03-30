<script lang="ts">
  let { label, value, unit = '', detail = '', percent = 0, color = 'accent' }:
    { label: string; value: string; unit?: string; detail?: string; percent?: number; color?: string } = $props();
</script>

<div class="cell">
  <span class="label">{label}</span>
  <div class="val">{value}<span class="unit">{unit}</span></div>
  <div class="detail">{detail || '\u00A0'}</div>
  <div class="bar">
    <div class="fill {color}" style="width:{percent}%"></div>
  </div>
</div>

<style>
  .cell { display: flex; flex-direction: column; gap: 8px; }
  .label {
    font-size: 0.62rem; font-family: var(--font-mono);
    text-transform: uppercase; letter-spacing: 0.1em;
    color: var(--text-tertiary);
  }
  .val {
    font-size: 1.5rem; font-weight: 600; letter-spacing: -0.03em;
    line-height: 1; font-family: var(--font-mono);
  }
  .unit { font-size: 0.65rem; font-weight: 400; color: var(--text-tertiary); margin-left: 2px; }
  .detail {
    font-size: 0.68rem; color: var(--text-tertiary); font-family: var(--font-mono);
    margin-top: -2px;
  }
  .bar { height: 3px; border-radius: 2px; background: rgba(42,37,32,0.6); overflow: hidden; }
  .fill { height: 100%; border-radius: 2px; transition: width 0.8s cubic-bezier(0.16,1,0.3,1); }
  .fill.accent { background: var(--accent); }
  .fill.mem { background: #8b5cf6; }
  .fill.live { background: var(--live); }
  .fill.muted { background: var(--accent); opacity: 0.5; }
</style>
