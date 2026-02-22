<script setup>
import {computed, onBeforeUnmount, onMounted, reactive} from 'vue'
import {StartUpdate, CancelUpdate, GetDefaults, IsUpdating, CheckPrerequisites} from '../../wailsjs/go/main/App'
import {EventsOn} from '../../wailsjs/runtime/runtime'
import PrereqGate from './PrereqGate.vue'

const stepOrder = ['fetch_manifest', 'check_local', 'download', 'integrity', 'symlink']
const stepLabels = {
  fetch_manifest: 'Fetch Manifest',
  check_local: 'Local Validation',
  download: 'Download',
  integrity: 'Integrity Check',
  symlink: 'Symlinks'
}

const state = reactive({
  defaults: {
    application_name: '',
    application_version: '',
    destination: '',
    manifest_url: '',
    pathmap_url: '',
    timeout_seconds: 0,
  },
  prereq: {
    ok: true,
    missing: [],
    target_dir: '',
    working_dir: '',
  },
  steps: buildStepState(),
  progress: [],
  latestProgress: null,
  eventCount: 0,
  summary: '',
  error: '',
  running: false
})

let unsubscribers = []

function buildStepState() {
  const base = {}
  stepOrder.forEach((step) => {
    base[step] = {
      status: 'pending',
      message: 'Waiting',
      error: '',
    }
  })
  return base
}

const orderedSteps = computed(() => stepOrder.map((key) => ({
  key,
  label: stepLabels[key] || key,
  status: state.steps[key]?.status || 'pending',
  message: state.steps[key]?.message || '',
  error: state.steps[key]?.error || '',
})))

onMounted(async () => {
  await hydrateDefaults()
  await checkPrereq()
  bindEvents()
  state.running = await IsUpdating()
})

onBeforeUnmount(() => {
  unsubscribers.forEach((off) => off && off())
  unsubscribers = []
})

async function hydrateDefaults() {
  try {
    const defaults = await GetDefaults()
    state.defaults = defaults
  } catch (err) {
    state.error = toMessage(err)
  }
}

async function checkPrereq() {
  try {
    const res = await CheckPrerequisites()
    state.prereq = res
  } catch (err) {
    state.prereq.ok = false
    state.prereq.missing = [toMessage(err)]
  }
}

function bindEvents() {
  unsubscribers.push(EventsOn('syncer:step', (evt) => {
    const status = normalizeStatus(evt.status)
    state.steps[evt.step] = {
      status,
      message: evt.message,
      error: evt.error || '',
    }
  }))

  unsubscribers.push(EventsOn('syncer:progress', (evt) => {
    const actionText = progressActionToText(evt.action)
    state.progress.unshift({
      path: evt.path,
      action: actionText,
      actionCode: evt.action,
      index: evt.index,
      total: evt.total,
      error: evt.error || '',
      at: new Date().toLocaleTimeString(),
    })
    state.latestProgress = evt
    state.eventCount += 1
    if (state.progress.length > 100) {
      state.progress.pop()
    }
  }))

  unsubscribers.push(EventsOn('syncer:done', (evt) => {
    state.running = false
    state.summary = evt.summary || ''
    state.error = evt.error || ''
    state.latestProgress = null
  }))
}

async function startUpdate() {
  await checkPrereq()
  if (!state.prereq.ok) {
    state.error = 'Prerequisites not met. See details above.'
    return
  }

  state.error = ''
  state.summary = ''
  state.progress = []
  state.latestProgress = null
  state.eventCount = 0
  state.steps = buildStepState()
  state.running = true

  try {
    await StartUpdate()
  } catch (err) {
    state.running = false
    state.error = toMessage(err)
  }
}

async function cancelUpdate() {
  try {
    await CancelUpdate()
  } catch (err) {
    state.error = toMessage(err)
  }
}

function statusClass(step) {
  if (step.error) return 'error'
  const status = normalizeStatus(step.status)
  if (status === 2) return 'done'      // StatusDone
  if (status === 1) return 'running'   // StatusRunning
  return 'pending'
}

const progressPercent = computed(() => {
  const lp = state.latestProgress
  if (!lp || !lp.total) return 0
  const ratio = lp.index / lp.total
  if (!isFinite(ratio)) return 0
  return Math.max(0, Math.min(100, Math.round(ratio * 100)))
})

function progressActionToText(code) {
  switch (code) {
    case 1:
      return 'Present'
    case 2:
      return 'Downloaded'
    case 3:
      return 'Verified'
    case 4:
      return 'Linked'
    case 5:
      return 'Error'
    default:
      return 'Unknown'
  }
}

function toMessage(err) {
  if (!err) return ''
  if (typeof err === 'string') return err
  if (err.message) return err.message
  return JSON.stringify(err)
}

function normalizeStatus(val) {
  const num = Number(val)
  if (Number.isFinite(num)) return num
  if (val === 'running') return 1
  if (val === 'done') return 2
  if (val === 'error') return 3
  return 0
}
</script>

<template>
  <section class="panel">
    <PrereqGate v-if="!state.prereq.ok" :missing="state.prereq.missing" :target-dir="state.prereq.target_dir" :working-dir="state.prereq.working_dir" @retry="checkPrereq" />
    <template v-else>
    <div class="panel__header">
      <p class="eyebrow">{{ state.defaults.application_name }} v{{ state.defaults.application_version }}</p>
      <h1>Update clothes textures automatically</h1>
      <p class="lede">Downloads missing files, verifies integrity, then refreshes symlinks so the game sees the latest files.</p>
    </div>

    <div class="actions">
      <button class="btn primary" :disabled="state.running" @click="startUpdate">{{ state.running ? 'Running…' : 'Start Update' }}</button>
      <button class="btn ghost" :disabled="!state.running" @click="cancelUpdate">Cancel</button>
    </div>

    <div class="progress-row" v-if="state.running || progressPercent > 0">
      <div class="progress-meta">
        <span>{{ progressPercent }}%</span>
        <span v-if="state.latestProgress">{{ state.latestProgress.index }} / {{ state.latestProgress.total }}</span>
        <span class="muted" v-else>Awaiting first event…</span>
      </div>
      <div class="progress-bar">
        <div class="progress-bar__fill" :style="{ width: progressPercent + '%' }"></div>
      </div>
    </div>

    <div class="steps">
      <div v-for="step in orderedSteps" :key="step.key" class="step" :class="statusClass(step)">
        <div class="step__label">{{ step.label }}</div>
        <div class="step__message">{{ step.error || step.message }}</div>
      </div>
    </div>

    <div class="panels">
      <div class="panel-card">
        <div class="panel-card__header">
          <div>
            <p class="eyebrow">Live progress</p>
            <h3>{{ state.eventCount }} events</h3>
          </div>
          <span class="chip" v-if="state.running">Working</span>
          <span class="chip done" v-else>Ready</span>
        </div>
        <div class="log">
          <div v-if="state.progress.length === 0" class="log__empty">No events yet. Start an update to see file activity.</div>
          <div v-for="item in state.progress" :key="item.at + item.path + item.index" class="log__row">
            <div class="log__meta">
              <span class="chip" :class="item.error ? 'error' : 'done'">{{ item.error ? 'error' : item.action }}</span>
              <span class="log__path">{{ item.path }}</span>
            </div>
            <div class="log__details">
              <span>{{ item.index }} / {{ item.total }}</span>
              <span class="log__time">{{ item.at }}</span>
            </div>
            <div v-if="item.error" class="log__error">{{ item.error }}</div>
          </div>
        </div>
      </div>

      <div class="panel-card">
        <div class="panel-card__header">
          <p class="eyebrow">Result</p>
          <h3>{{ state.error ? 'Failed' : (state.summary ? 'Finished' : 'Standing by') }}</h3>
        </div>
        <div class="summary" v-if="state.summary || state.error">
          <p class="summary__title">{{ state.error ? 'Something went wrong' : 'All steps completed' }}</p>
          <p class="summary__body">{{ state.error || state.summary }}</p>
        </div>
        <div class="summary muted" v-else>
          <p class="summary__title">No run yet</p>
          <p class="summary__body">Start an update to see a summary here.</p>
        </div>
      </div>
    </div>
    </template>
  </section>
</template>

<style scoped>
.panel {
  padding: 28px;
  display: flex;
  flex-direction: column;
  gap: 12px;
  height: 100%;
  min-height: 100%;
  overflow: hidden;
}

.progress-row {
  margin: 10px 0 18px;
  display: flex;
  flex-direction: column;
  gap: 6px;
}

.progress-meta {
  display: flex;
  gap: 10px;
  align-items: center;
  color: var(--text);
  font-weight: 700;
}

.progress-meta .muted {
  color: var(--muted);
  font-weight: 400;
}

.progress-bar {
  width: 100%;
  height: 10px;
  border-radius: 999px;
  background: rgba(255, 255, 255, 0.08);
  overflow: hidden;
}

.progress-bar__fill {
  height: 100%;
  background: linear-gradient(120deg, var(--accent), var(--accent-strong));
  transition: width 0.2s ease;
}

.panel__header h1 {
  margin: 6px 0 8px;
  font-size: 28px;
  letter-spacing: -0.4px;
}

.lede {
  color: var(--muted);
  margin: 0;
  line-height: 1.5;
}

.actions {
  display: flex;
  align-items: center;
  gap: 12px;
  margin: 10px 0 24px;
}

.btn {
  border: none;
  border-radius: var(--radius);
  padding: 12px 18px;
  font-weight: 700;
  cursor: pointer;
  transition: transform 0.1s ease, box-shadow 0.2s ease, background 0.2s ease, filter 0.15s ease;
}

.btn.primary {
  background: linear-gradient(120deg, var(--accent), var(--accent-strong));
  color: var(--text);
  text-shadow: 0 1px 2px rgba(0, 0, 0, 0.2);
  box-shadow: var(--shadow-soft);
}

.btn.ghost {
  background: var(--surface-soft);
  color: var(--text);
  border: 1px solid var(--border);
}

.btn:disabled {
  opacity: 0.6;
  cursor: not-allowed;
  transform: none;
  box-shadow: none;
}

.btn:not(:disabled):hover {
  box-shadow: 0 14px 30px rgba(124, 231, 255, 0.2);
  filter: brightness(1.05);
}

.btn:active {
  transform: translateY(1px) scale(0.99);
}

.btn:focus-visible {
  outline: 2px solid var(--accent);
  outline-offset: 2px;
}

.status-pill {
  padding: 8px 12px;
  border-radius: 999px;
  background: var(--surface-soft);
  color: var(--muted-strong);
  font-size: 13px;
}

.status-pill.muted {
  color: #90a0b6;
}

.steps {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(180px, 1fr));
  gap: 12px;
  margin-bottom: 20px;
}

.step {
  border: 1px solid var(--border);
  border-radius: var(--radius);
  padding: 10px;
  background: var(--surface-soft);
  transition: border-color 0.15s ease, box-shadow 0.2s ease, transform 0.1s ease;
}

.step__label {
  font-weight: 700;
  margin-bottom: 6px;
}

.step__message {
  color: var(--muted);
  font-size: 13px;
}

.step.running {
  border-color: rgba(124, 231, 255, 0.6);
  box-shadow: 0 0 0 2px rgba(124, 231, 255, 0.15);
  transform: translateY(-1px);
}

.step.done {
  border-color: rgba(95, 248, 161, 0.5);
  background: rgba(95, 248, 161, 0.05);
}

.step.error {
  border-color: rgba(255, 114, 94, 0.7);
  background: rgba(255, 114, 94, 0.08);
  color: #ffd7d1;
}

.panels {
  display: grid;
  grid-template-columns: 2fr 1fr;
  gap: 16px;
  flex: 1;
  min-height: 0;
}

@media (max-width: 1024px) {
  .panels {
    grid-template-columns: 1fr;
  }
}

.panel-card {
  border: 1px solid var(--border);
  border-radius: var(--radius);
  padding: 14px 16px;
  background: var(--surface);
  display: flex;
  flex-direction: column;
  min-height: 0;
  transition: transform 0.12s ease, box-shadow 0.2s ease;
}

.panel-card__header {
  display: flex;
  align-items: center;
  justify-content: space-between;
}

.chip {
  padding: 6px 10px;
  border-radius: 999px;
  background: var(--surface-soft);
  font-size: 12px;
  color: #dfe7f5;
}

.chip.done {
  background: rgba(95, 248, 161, 0.2);
  color: #adffd4;
}

.chip.action {
  background: rgba(124, 231, 255, 0.2);
  color: #a7f0ff;
}

.chip.error {
  background: rgba(255, 114, 94, 0.18);
  color: #ffc6be;
}

.log {
  margin-top: 12px;
  flex: 1;
  min-height: 0;
  overflow: auto;
  display: flex;
  flex-direction: column;
  gap: 10px;
}

.log__row {
  padding: 10px 12px;
  border-radius: 10px;
  background: var(--surface-soft);
  border: 1px solid var(--border);
  transition: border-color 0.15s ease, background 0.15s ease;
  display: flex;
  flex: 1;
  justify-content: space-between;
}

.log__row:hover {
  border-color: rgba(124, 231, 255, 0.3);
  background: rgba(255, 255, 255, 0.05);
}

.log__meta {
  display: flex;
  align-items: center;
  gap: 10px;
}

.log__path {
  color: var(--text);
  font-weight: 600;
  word-break: break-all;
}

.log__details {
  display: flex;
  align-items: center;
  gap: 10px;
  color: var(--muted);
  font-size: 12px;
  margin-top: 4px;
}

.log__time {
  color: var(--muted);
}

.log__error {
  color: #ffc6be;
  font-size: 13px;
  margin-top: 6px;
}

.log__empty {
  text-align: center;
  color: #8fa3bb;
  padding: 20px 0;
}

.summary {
  padding: 12px;
  border-radius: 10px;
  background: linear-gradient(120deg, rgba(94, 224, 194, 0.12), rgba(255, 93, 177, 0.12));
  border: 1px solid var(--border);
}

.summary.muted {
  background: rgba(255, 255, 255, 0.04);
}

.summary__title {
  margin: 0 0 4px;
  font-weight: 700;
}

.summary__body {
  margin: 0;
  color: #cfd8e8;
}

.eyebrow {
  text-transform: uppercase;
  font-size: 12px;
  letter-spacing: 2px;
  color: var(--muted);
  margin: 0;
}
</style>
