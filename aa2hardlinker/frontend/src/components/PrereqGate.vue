<script setup>
const props = defineProps({
  missing: { type: Array, default: () => [] },
  targetDir: { type: String, default: '' },
  workingDir: { type: String, default: '' },
})
</script>

<template>
  <section class="prereq">
    <div class="prereq__header">
      <p class="eyebrow">Prerequisites required</p>
      <h2>Before you can update</h2>
      <p class="lede">We need to locate your game folder and the textures directory.</p>
    </div>

    <div class="prereq__card">
      <p class="prereq__hint">Check that you are running this updater from your AA2 install directory.</p>
      <ul class="prereq__list">
        <li>Current folder: <strong>{{ workingDir || 'unknown' }}</strong></li>
        <li>Expected target: <strong>{{ targetDir || 'data/texture/harderlinker' }}</strong></li>
      </ul>

      <div v-if="missing.length" class="prereq__missing">
        <p class="prereq__title">Missing items</p>
        <ul>
          <li v-for="item in missing" :key="item">{{ item }}</li>
        </ul>
      </div>

      <div class="prereq__actions">
        <div class="prereq__tips">
          <p>Tips:</p>
          <ul>
            <li>Launch the updater inside the same folder as <strong>AA2Play.exe</strong>.</li>
            <li>Ensure the <strong>data/texture</strong> path exists.</li>
          </ul>
        </div>
      </div>
    </div>
  </section>
</template>

<style scoped>
.prereq {
  padding: 24px;
  border: 1px solid var(--border);
  border-radius: var(--radius);
  background: var(--surface);
  color: var(--text);
  display: flex;
  flex-direction: column;
  gap: 12px;
}

.prereq__header h2 {
  margin: 6px 0 4px;
}

.prereq__card {
  border: 1px solid var(--border);
  border-radius: var(--radius);
  padding: 14px 16px;
  background: var(--surface-soft);
  display: flex;
  flex-direction: column;
  gap: 12px;
}

.prereq__hint {
  margin: 0;
  color: var(--muted);
}

.prereq__list {
  margin: 0;
  padding-left: 18px;
  color: var(--text);
}

.prereq__missing {
  border: 1px solid var(--danger);
  border-radius: var(--radius);
  padding: 10px 12px;
  background: rgba(255, 138, 122, 0.08);
}

.prereq__title {
  margin: 0 0 6px;
  font-weight: 700;
  color: var(--danger);
}

.prereq__actions {
  display: flex;
  gap: 12px;
  align-items: flex-start;
}

.prereq__tips {
  color: var(--muted);
  font-size: 13px;
}

.prereq__tips ul {
  margin: 4px 0 0;
  padding-left: 18px;
}
</style>
