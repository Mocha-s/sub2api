<template>
  <AppLayout>
    <TablePageLayout>
      <template #filters>
        <div class="flex flex-wrap items-end gap-3">
          <DateRangePicker v-model:start-date="startDate" v-model:end-date="endDate" @change="reload" />
          <div class="w-36">
            <Select v-model="filters.type" :options="typeOptions" @change="reload" />
          </div>
          <div class="w-36">
            <Select v-model="filters.status" :options="statusOptions" @change="reload" />
          </div>
          <input
            v-model.trim="filters.platform"
            class="input w-36"
            :placeholder="t('taskLogs.columns.platform')"
            :aria-label="t('taskLogs.columns.platform')"
            @keyup.enter="reload"
          />
          <input
            v-model.trim="filters.task_id"
            class="input w-56"
            :placeholder="t('taskLogs.columns.taskId')"
            :aria-label="t('taskLogs.columns.taskId')"
            @keyup.enter="reload"
          />
          <input
            v-if="admin"
            v-model.number="filters.user_id"
            type="number"
            min="1"
            class="input w-28"
            :placeholder="t('taskLogs.userId')"
            :aria-label="t('taskLogs.userId')"
            @keyup.enter="reload"
          />
          <button type="button" class="btn btn-secondary" :disabled="loading" @click="load()">
            {{ t('common.refresh') }}
          </button>
        </div>
      </template>

      <template #table>
        <DataTable :columns="columns" :data="rows" :loading="loading" row-key="task_id">
          <template #cell-submitted_at="{ value }">
            <span class="whitespace-nowrap">{{ formatDateTime(value) }}</span>
          </template>
          <template #cell-finished_at="{ value }">
            <span class="whitespace-nowrap">{{ value ? formatDateTime(value) : '-' }}</span>
          </template>
          <template #cell-duration="{ row }">
            <span class="whitespace-nowrap">{{ formatTaskDuration(taskDurationMs(row, now)) }}</span>
          </template>
          <template #cell-type="{ row }">{{ t(`taskLogs.types.${row.type}`) }}</template>
          <template #cell-task_id="{ row }">
            <button
              type="button"
              class="font-mono text-xs text-gray-700 hover:text-primary-600 dark:text-gray-300"
              :title="t('taskLogs.copyTaskId')"
              @click="copyToClipboard(row.task_id)"
            >
              {{ row.task_id }}
            </button>
          </template>
          <template #cell-status="{ row }">
            <span :title="row.raw_status">
              <StatusBadge :status="statusVariant[row.status as TaskLogStatus]" :label="t(`taskLogs.status.${row.status}`)" />
            </span>
          </template>
          <template #cell-progress="{ row }">
            <div v-if="isTaskActive(row) || row.status === 'succeeded'" class="flex items-center gap-2">
              <div
                class="h-1.5 w-20 overflow-hidden rounded bg-gray-200 dark:bg-dark-600"
                role="progressbar"
                :aria-label="t('taskLogs.columns.progress')"
                aria-valuemin="0"
                aria-valuemax="100"
                :aria-valuenow="row.progress"
              >
                <div class="h-full bg-primary-500" :style="{ width: `${row.progress}%` }"></div>
              </div>
              <span class="whitespace-nowrap text-xs text-gray-500 dark:text-gray-400">
                {{ row.progress }}%<template v-if="row.type === 'batch_image'">
                  · {{ (row.success_count ?? 0) + (row.fail_count ?? 0) }}/{{ row.item_count ?? 0 }}</template
                >
              </span>
            </div>
            <span v-else class="text-gray-500 dark:text-gray-400">{{ t(`taskLogs.status.${row.status}`) }}</span>
          </template>
          <template #cell-user="{ row }">
            <span class="text-xs">{{ row.user_email || row.user_id }}</span>
          </template>
          <template #cell-summary="{ row }">
            <div class="flex items-center gap-1.5 whitespace-nowrap">
              <span>{{ taskSummary(row, t) }}</span>
              <span
                v-if="canSettle(row, admin)"
                class="rounded bg-blue-100 px-1 py-px text-[10px] font-medium text-blue-700 dark:bg-blue-500/20 dark:text-blue-300"
              >
                {{ settlementLabelKey[row.settlement_state] ? t(settlementLabelKey[row.settlement_state]) : row.settlement_state }}
              </span>
            </div>
          </template>
          <template #cell-detail="{ row }">
            <div class="flex items-center gap-2 whitespace-nowrap">
              <button
                v-if="hasResultLink(row)"
                type="button"
                class="font-medium text-primary-600 hover:text-primary-700 disabled:opacity-50 dark:text-primary-400"
                :disabled="busyId === row.task_id || previewLoading && detailRow?.task_id === row.task_id"
                @click="openResult(row)"
              >
                {{ t(row.type === 'batch_image' ? 'taskLogs.download' : 'taskLogs.preview') }}
              </button>
              <button
                v-if="row.type !== 'batch_image' && hasResultLink(row)"
                type="button"
                class="font-medium text-primary-600 hover:text-primary-700 disabled:opacity-50 dark:text-primary-400"
                :disabled="!!busyId"
                @click="downloadResult(row)"
              >{{ busyId === row.task_id ? `${t('taskLogs.downloading')}${downloadProgress}` : t('taskLogs.download') }}</button>
              <span
                v-else-if="row.error_message"
                class="max-w-[200px] truncate text-red-600 dark:text-red-400"
                :title="row.error_message"
              >
                {{ row.error_message }}
              </span>
              <button type="button" class="text-gray-500 hover:text-gray-700 dark:text-gray-400" @click="openDetail(row)">
                {{ t('taskLogs.view') }}
              </button>
              <button
                v-if="canSettle(row, admin)"
                type="button"
                class="text-blue-600 hover:text-blue-700 dark:text-blue-400"
                @click="settleRow = row"
              >
                {{ t('admin.usage.videoSettlement') }}
              </button>
            </div>
          </template>
          <template #empty>
            <p class="py-8 text-center text-sm text-gray-500 dark:text-gray-400">{{ t('taskLogs.empty') }}</p>
          </template>
        </DataTable>
      </template>

      <template #pagination>
        <Pagination
          v-if="total > 0"
          :total="total"
          :page="page"
          :page-size="pageSize"
          :page-size-options="[10, 20, 50, 100]"
          @update:page="onPageChange"
          @update:pageSize="onPageSizeChange"
        />
      </template>
    </TablePageLayout>

    <BaseDialog :show="!!detailRow" :title="t(previewMode ? detailRow?.type === 'video' ? 'taskLogs.videoPreview' : 'taskLogs.imagePreview' : 'taskLogs.detailTitle')" width="wide" @close="closeDetail">
      <div v-if="detailRow" class="space-y-4 text-sm">
        <p v-if="previewLoading" role="status" class="py-8 text-center">{{ t('taskLogs.loadingMedia') }}{{ previewProgress }}</p>
        <p v-if="previewError" role="alert" class="rounded bg-red-50 p-3 text-red-700 dark:bg-red-950 dark:text-red-300">{{ previewError }}</p>
        <video v-if="resultUrl && detailRow.type === 'video'" :src="resultUrl" controls playsinline class="max-h-[60vh] w-full rounded bg-black" @error="previewError = t('taskLogs.playbackFailed')"></video>
        <img v-if="resultUrl && detailRow.type === 'image'" :src="resultUrl" :alt="detailRow.prompt || t('taskLogs.types.image')" class="max-h-[60vh] w-full rounded object-contain" />
        <div v-if="previewMode && detailRow.type === 'image' && hasResultLink(detailRow) && (detailRow.success_count ?? 0) > 1" class="flex items-center justify-center gap-3">
          <button type="button" class="btn btn-secondary" :disabled="previewLoading || imageIndex === 0" @click="loadPreview(detailRow, imageIndex - 1)">{{ t('taskLogs.previousImage') }}</button>
          <span>{{ imageIndex + 1 }} / {{ detailRow.success_count }}</span>
          <button type="button" class="btn btn-secondary" :disabled="previewLoading || imageIndex + 1 >= (detailRow.success_count ?? 0)" @click="loadPreview(detailRow, imageIndex + 1)">{{ t('common.next') }}</button>
        </div>
        <template v-if="!previewMode">
        <div>
          <div class="mb-1 text-xs text-gray-500 dark:text-gray-400">{{ t('taskLogs.columns.taskId') }}</div>
          <div class="font-mono text-xs">{{ detailRow.task_id }}</div>
        </div>
        <div>
          <div class="mb-1 text-xs text-gray-500 dark:text-gray-400">
            {{ t(detailRow.type === 'batch_image' ? 'taskLogs.taskName' : 'taskLogs.prompt') }}
          </div>
          <div class="whitespace-pre-wrap break-words">{{ detailRow.prompt || '-' }}</div>
        </div>
        <div>
          <div class="mb-1 text-xs text-gray-500 dark:text-gray-400">{{ t('taskLogs.rawStatus') }}</div>
          <div class="font-mono text-xs">{{ detailRow.raw_status }}</div>
        </div>
        <div v-if="detailRow.error_code || detailRow.error_message">
          <div class="mb-1 text-xs text-gray-500 dark:text-gray-400">{{ t('taskLogs.error') }}</div>
          <div class="whitespace-pre-wrap break-words text-red-600 dark:text-red-400">
            <span v-if="detailRow.error_code" class="font-mono">[{{ detailRow.error_code }}] </span>{{ detailRow.error_message }}
          </div>
        </div>
        </template>
      </div>
      <template #footer>
        <button v-if="detailRow && hasResultLink(detailRow) && !resultUrl && !previewLoading && detailRow.type !== 'batch_image'" type="button" class="btn btn-secondary" @click="loadPreview(detailRow, imageIndex)">{{ t('taskLogs.preview') }}</button>
        <button v-if="detailRow && hasResultLink(detailRow)" type="button" class="btn btn-primary" :disabled="!!busyId || previewLoading" @click="downloadResult(detailRow, imageIndex)">{{ busyId === detailRow.task_id ? `${t('taskLogs.downloading')}${downloadProgress}` : t('taskLogs.download') }}</button>
        <button type="button" class="btn btn-ghost" @click="closeDetail">{{ t('common.close') }}</button>
      </template>
    </BaseDialog>

    <VideoSettlementModal v-if="admin" :row="settleRow" @close="settleRow = null" @done="load()" />
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { saveAs } from 'file-saver'
import type { AxiosProgressEvent } from 'axios'
import AppLayout from '@/components/layout/AppLayout.vue'
import TablePageLayout from '@/components/layout/TablePageLayout.vue'
import DataTable from '@/components/common/DataTable.vue'
import type { Column } from '@/components/common/types'
import Pagination from '@/components/common/Pagination.vue'
import Select from '@/components/common/Select.vue'
import DateRangePicker from '@/components/common/DateRangePicker.vue'
import BaseDialog from '@/components/common/BaseDialog.vue'
import StatusBadge from '@/components/common/StatusBadge.vue'
import VideoSettlementModal from '@/components/taskLogs/VideoSettlementModal.vue'
import { useAppStore } from '@/stores/app'
import { useClipboard } from '@/composables/useClipboard'
import { formatDateTime } from '@/utils/format'
import { downloadBatchImages, fetchImageContent, fetchVideoContent, listTaskLogs, type TaskLog, type TaskLogStatus } from '@/api/taskLogs'
import {
  canSettle,
  formatTaskDuration,
  hasResultLink,
  isTaskActive,
  settlementLabelKey,
  taskDurationMs,
  taskSummary
} from '@/utils/taskLog'

const props = withDefaults(defineProps<{ admin?: boolean }>(), { admin: false })

const { t } = useI18n()
const appStore = useAppStore()
const { copyToClipboard } = useClipboard()

const localDate = (d: Date) =>
  `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}-${String(d.getDate()).padStart(2, '0')}`
const DAY_MS = 24 * 60 * 60 * 1000

const loading = ref(false)
const rows = ref<TaskLog[]>([])
const total = ref(0)
const page = ref(1)
const pageSize = ref(20)
const now = ref(Date.now())
// 默认近 24 小时，与 DateRangePicker 的默认预设标签一致。
const startDate = ref(localDate(new Date(Date.now() - DAY_MS)))
const endDate = ref(localDate(new Date()))
const filters = reactive({ type: '', status: '', platform: '', task_id: '', user_id: undefined as number | undefined })

const detailRow = ref<TaskLog | null>(null)
const settleRow = ref<TaskLog | null>(null)
const resultUrl = ref('')
const previewLoading = ref(false)
const previewMode = ref(false)
const previewError = ref('')
const previewProgress = ref('')
const downloadProgress = ref('')
const imageIndex = ref(0)
let previewRequest: AbortController | undefined
let listRequest: AbortController | undefined
let downloadRequest: AbortController | undefined
let previewBlob: Blob | undefined
const busyId = ref('')

const statusVariant: Record<TaskLogStatus, string> = {
  queued: 'pending',
  running: 'warning',
  succeeded: 'success',
  failed: 'error',
  cancelled: 'cancelled',
  expired: 'error'
}

const typeOptions = computed(() => [
  { value: '', label: t('taskLogs.allTypes') },
  { value: 'video', label: t('taskLogs.types.video') },
  { value: 'image', label: t('taskLogs.types.image') },
  { value: 'batch_image', label: t('taskLogs.types.batch_image') }
])
const statusOptions = computed(() => [
  { value: '', label: t('taskLogs.allStatuses') },
  ...(Object.keys(statusVariant) as TaskLogStatus[]).map((s) => ({ value: s, label: t(`taskLogs.status.${s}`) }))
])

const columns = computed<Column[]>(() => [
  { key: 'submitted_at', label: t('taskLogs.columns.submittedAt') },
  { key: 'finished_at', label: t('taskLogs.columns.finishedAt') },
  { key: 'duration', label: t('taskLogs.columns.duration') },
  { key: 'platform', label: t('taskLogs.columns.platform') },
  { key: 'type', label: t('taskLogs.columns.type') },
  { key: 'task_id', label: t('taskLogs.columns.taskId') },
  { key: 'status', label: t('taskLogs.columns.status') },
  { key: 'progress', label: t('taskLogs.columns.progress') },
  ...(props.admin ? [{ key: 'user', label: t('taskLogs.columns.user') }] : []),
  { key: 'summary', label: t('taskLogs.columns.summary') },
  { key: 'detail', label: t('taskLogs.columns.detail') }
])

const errorText = (e: unknown, fallback: string) => (e as { message?: string })?.message || fallback

async function load(silent = false) {
  if (silent && listRequest) return
  listRequest?.abort()
  const request = new AbortController()
  listRequest = request
  if (!silent) loading.value = true
  try {
    const res = await listTaskLogs(!!props.admin, {
      page: page.value,
      page_size: pageSize.value,
      type: filters.type || undefined,
      status: filters.status || undefined,
      platform: filters.platform || undefined,
      task_id: filters.task_id || undefined,
      user_id: props.admin && filters.user_id ? filters.user_id : undefined,
      start: new Date(`${startDate.value}T00:00:00`).toISOString(),
      end: new Date(new Date(`${endDate.value}T00:00:00`).getTime() + DAY_MS).toISOString()
    }, request.signal)
    if (request.signal.aborted) return
    rows.value = res.items
    total.value = res.total
    now.value = Date.now()
    if (detailRow.value) {
      detailRow.value = rows.value.find(row => row.type === detailRow.value?.type && row.task_id === detailRow.value?.task_id) ?? detailRow.value
    }
  } catch (e) {
    if (!silent && !request.signal.aborted) appStore.showError(errorText(e, t('taskLogs.loadFailed')))
  } finally {
    if (listRequest === request) {
      listRequest = undefined
      loading.value = false
    }
  }
}

function reload() {
  closeDetail()
  page.value = 1
  void load()
}
function onPageChange(p: number) {
  page.value = p
  void load()
}
function onPageSizeChange(size: number) {
  pageSize.value = size
  reload()
}

function clearPreview() {
  previewRequest?.abort()
  previewRequest = undefined
  previewLoading.value = false
  previewError.value = ''
  previewProgress.value = ''
  if (resultUrl.value) URL.revokeObjectURL(resultUrl.value)
  resultUrl.value = ''
  previewBlob = undefined
}
function closeDetail() {
  detailRow.value = null
  clearPreview()
}
function openDetail(row: TaskLog) {
  clearPreview()
  previewMode.value = false
  imageIndex.value = 0
  detailRow.value = row
}
function openResult(row: TaskLog) {
  if (row.type === 'batch_image') {
    void downloadResult(row)
  } else {
    openDetail(row)
    void loadPreview(row, 0)
  }
}
async function loadPreview(row: TaskLog, index: number) {
  clearPreview()
  previewMode.value = true
  imageIndex.value = index
  const request = new AbortController()
  previewRequest = request
  previewLoading.value = true
  try {
    const blob = row.type === 'video'
      ? await fetchVideoContent(props.admin, row.task_id, request.signal, event => { if (!request.signal.aborted) previewProgress.value = transferProgress(event) })
      : await fetchImageContent(props.admin, row.task_id, index, request.signal, event => { if (!request.signal.aborted) previewProgress.value = transferProgress(event) })
    if (request.signal.aborted) return
    previewBlob = blob
    resultUrl.value = URL.createObjectURL(blob)
  } catch (e) {
    if (!request.signal.aborted) {
      previewError.value = errorText(e, t('taskLogs.resultFailed'))
      appStore.showError(previewError.value)
    }
  } finally {
    if (previewRequest === request) previewLoading.value = false
  }
}
function transferProgress(event: AxiosProgressEvent) {
  return event.total
    ? ` ${Math.min(100, Math.round(event.loaded * 100 / event.total))}%`
    : ` ${(event.loaded / 1024 / 1024).toFixed(1)} MB`
}
async function downloadResult(row: TaskLog, index = 0) {
  if (busyId.value) return
  busyId.value = row.task_id
  downloadProgress.value = ''
  const request = new AbortController()
  downloadRequest = request
  try {
    if (row.type === 'batch_image') {
      saveAs(await downloadBatchImages(props.admin, row.task_id), `${row.task_id}.zip`)
    } else {
      const blob = detailRow.value?.task_id === row.task_id && detailRow.value.type === row.type && imageIndex.value === index && previewBlob
        ? previewBlob
        : row.type === 'video'
          ? await fetchVideoContent(props.admin, row.task_id, request.signal, event => { if (!request.signal.aborted) downloadProgress.value = transferProgress(event) })
          : await fetchImageContent(props.admin, row.task_id, index, request.signal, event => { if (!request.signal.aborted) downloadProgress.value = transferProgress(event) })
      if (request.signal.aborted) return
      const extensions: Record<string, string> = { 'image/png': 'png', 'image/jpeg': 'jpg', 'image/webp': 'webp', 'image/gif': 'gif', 'image/avif': 'avif', 'video/mp4': 'mp4', 'video/webm': 'webm' }
      const extension = extensions[blob.type.split(';')[0]] ?? (row.type === 'video' ? 'mp4' : 'bin')
      saveAs(blob, `${row.task_id}${row.type === 'image' ? `-${index + 1}` : ''}.${extension}`)
    }
    appStore.showSuccess(t('taskLogs.downloadStarted'))
  } catch (e) {
    if (!request.signal.aborted) appStore.showError(errorText(e, t('taskLogs.resultFailed')))
  } finally {
    if (downloadRequest === request) {
      busyId.value = ''
      downloadRequest = undefined
    }
  }
}

watch(() => props.admin, () => {
  downloadRequest?.abort()
  closeDetail()
  settleRow.value = null
  rows.value = []
  reload()
})

// 有未完成任务且页面可见时，每 10 秒静默刷新当前页（与后端轮询间隔一致）。
let timer: number | undefined
onMounted(() => {
  void load()
  timer = window.setInterval(() => {
    now.value = Date.now()
    if (!document.hidden && rows.value.some(isTaskActive)) void load(true)
  }, 10_000)
})
onBeforeUnmount(() => {
  window.clearInterval(timer)
  listRequest?.abort()
  downloadRequest?.abort()
  clearPreview()
})
</script>
