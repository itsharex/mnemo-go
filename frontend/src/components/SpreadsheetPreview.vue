<script setup>
import { computed, onBeforeUnmount, onMounted, ref, shallowRef } from 'vue'
import UiSelect from './UiSelect.vue'

const props = defineProps({ bytes: Object, file: Object })
const loading = ref(true)
const error = ref('')
const workbook = shallowRef(null)
const activeSheet = ref('')
const page = ref(0)
const PAGE_ROWS = 100
const MAX_COLUMNS = 50
let library = null
let disposed = false

const sheetNames = computed(() => workbook.value?.SheetNames || [])
const sheet = computed(() => workbook.value?.Sheets[activeSheet.value])
const range = computed(() => {
  if (!library || !sheet.value?.['!ref']) return null
  try { return library.utils.decode_range(sheet.value['!ref']) } catch { return null }
})
const rowCount = computed(() => range.value ? range.value.e.r + 1 : 0)
const columnCount = computed(() => range.value ? Math.min(MAX_COLUMNS, range.value.e.c + 1) : 0)
const pageCount = computed(() => Math.max(1, Math.ceil(rowCount.value / PAGE_ROWS)))
const startRow = computed(() => page.value * PAGE_ROWS)
const columns = computed(() => library ? Array.from({ length: columnCount.value }, (_, index) => library.utils.encode_col(index)) : [])
const rows = computed(() => {
  if (!library || !sheet.value) return []
  const end = Math.min(rowCount.value, startRow.value + PAGE_ROWS)
  return Array.from({ length: Math.max(0, end - startRow.value) }, (_, offset) => {
    const number = startRow.value + offset
    const cells = columns.value.map((_, column) => {
      const cell = sheet.value[library.utils.encode_cell({ r: number, c: column })]
      if (!cell) return ''
      if (cell.v == null && cell.f) return `=${cell.f}`
      try { return library.utils.format_cell(cell) } catch { return String(cell.w ?? cell.v ?? '') }
    })
    return { number: number + 1, cells }
  })
})

async function openWorkbook() {
  try {
    library = await import('xlsx')
    if (disposed) return
    const parsed = library.read(props.bytes, { type: 'array', cellText: true, cellDates: true })
    if (!parsed.SheetNames.length) throw new Error('文件中没有可显示的工作表')
    workbook.value = parsed
    activeSheet.value = parsed.SheetNames[0]
  } catch (cause) {
    if (!disposed) error.value = String(cause?.message || cause)
  } finally {
    if (!disposed) loading.value = false
  }
}

function selectSheet(value) { activeSheet.value = value; page.value = 0 }
onMounted(openWorkbook)
onBeforeUnmount(() => { disposed = true; workbook.value = null })
</script>

<template>
  <div class="spreadsheet-viewer">
    <div class="spreadsheet-toolbar"><strong>表格预览</strong>
      <div v-if="sheetNames.length" class="spreadsheet-controls">
        <UiSelect :model-value="activeSheet" :options="sheetNames.map(name => ({ value: name, label: name }))" @change="selectSheet" />
        <button class="btn sm" :disabled="page === 0" @click="page--">上一页</button>
        <span>{{ page + 1 }} / {{ pageCount }}</span>
        <button class="btn sm" :disabled="page + 1 >= pageCount" @click="page++">下一页</button>
      </div>
    </div>
    <div v-if="loading" class="spreadsheet-state"><span class="spin"></span>正在读取表格…</div>
    <div v-else-if="error" class="spreadsheet-state" role="alert">{{ error }}</div>
    <div v-else class="spreadsheet-stage">
      <p v-if="range && range.e.c + 1 > MAX_COLUMNS" class="spreadsheet-note">为保持流畅，仅展示前 {{ MAX_COLUMNS }} 列</p>
      <table><thead><tr><th scope="col"></th><th v-for="column in columns" :key="column" scope="col">{{ column }}</th></tr></thead>
        <tbody><tr v-for="row in rows" :key="row.number"><th scope="row">{{ row.number }}</th><td v-for="(cell, index) in row.cells" :key="index" :title="cell">{{ cell }}</td></tr></tbody>
      </table>
      <div v-if="!rowCount" class="spreadsheet-state">此工作表没有内容</div>
    </div>
  </div>
</template>

<style scoped>
.spreadsheet-viewer { flex: 1; min-height: 0; display: flex; flex-direction: column; }
.spreadsheet-toolbar { min-height: 48px; padding: 7px 16px; display: flex; align-items: center; justify-content: space-between; gap: 12px; border-bottom: 1px solid var(--border-light); background: var(--bg-surface); }
.spreadsheet-toolbar strong { font-size: 12px; color: var(--text-secondary); white-space: nowrap; }
.spreadsheet-controls { display: flex; align-items: center; justify-content: flex-end; gap: 8px; font-size: 12px; color: var(--text-secondary); font-variant-numeric: tabular-nums; }
.spreadsheet-controls :deep(.uiselect) { min-width: 100px; max-width: 200px; }
.spreadsheet-state { flex: 1; display: flex; align-items: center; justify-content: center; gap: 10px; color: var(--text-secondary); font-size: 13px; }
.spreadsheet-stage { flex: 1; min-height: 0; overflow: auto; background: var(--bg-surface); }
.spreadsheet-note { padding: 8px 16px; color: var(--text-secondary); font-size: 12px; }
table { border-collapse: separate; border-spacing: 0; min-width: 100%; font-size: 12px; font-variant-numeric: tabular-nums; }
th, td { min-width: 96px; max-width: 280px; height: 28px; padding: 4px 8px; border-right: 1px solid var(--border-lighter); border-bottom: 1px solid var(--border-lighter); overflow: hidden; white-space: nowrap; text-overflow: ellipsis; text-align: left; }
thead th { position: sticky; top: 0; z-index: 2; background: var(--bg-elevated); color: var(--text-secondary); text-align: center; }
tbody th { position: sticky; left: 0; z-index: 1; min-width: 46px; width: 46px; max-width: 46px; background: var(--bg-elevated); color: var(--text-tertiary); text-align: center; }
thead th:first-child { left: 0; z-index: 3; min-width: 46px; width: 46px; max-width: 46px; }
tbody tr:nth-child(even) td { background: color-mix(in srgb, var(--bg-hover) 45%, transparent); }
@media (max-width: 600px) { .spreadsheet-toolbar, .spreadsheet-controls { flex-wrap: wrap; } }
</style>
