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
            <div class="flex items-center gap-2">
              <div
                class="h-1.5 w-20 overflow-hidden rounded bg-gray-200 dark:bg-dark-600"
                role="progressbar"
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
                v-if="hasResultLink(row, admin)"
                type="button"
                class="font-medium text-primary-600 hover:text-primary-700 disabled:opacity-50 dark:text-primary-400"
                :disabled="busyId === row.task_id"
                @click="openResult(row)"
              >
                {{ t(row.type === 'video' ? 'taskLogs.preview' : 'taskLogs.download') }}
              </button>
              <span
                v-else-if="row.error_message"
                class="max-w-[200px] truncate text-red-600 dark:text-red-400"
                :title="row.error_message"
              >
                {{ row.error_message }}
              </span>
              <button type="button" class="text-gray-500 hover:text-gray-700 dark:text-gray-400" @click="detailRow = row">
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

    <BaseDialog :show="!!detailRow" :title="t('taskLogs.detailTitle')" width="wide" @close="closeDetail">
      <div v-if="detailRow" class="space-y-4 text-sm">
        <video v-if="videoUrl" :src="videoUrl" controls class="max-h-[60vh] w-full rounded bg-black"></video>
        <div>
          <div class="mb-1 text-xs text-gray-500 dark:text-gray-400">{{ t('taskLogs.columns.taskId') }}</div>
          <div class="font-mono text-xs">{{ detailRow.task_id }}</div>
        </div>
        <div>
          <div class="mb-1 text-xs text-gray-500 dark:text-gray-400">
            {{ t(detailRow.type === 'video' ? 'taskLogs.prompt' : 'taskLogs.taskName') }}
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
      </div>
      <template #footer>
        <button type="button" class="btn btn-ghost" @click="closeDetail">{{ t('common.close') }}</button>
      </template>
    </BaseDialog>

    <VideoSettlementModal v-if="admin" :row="settleRow" @close="settleRow = null" @done="load()" />
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, reactive, ref } from 'vue'
import { useI18n } from 'vue-i18n'
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
import { downloadBatchImages, fetchVideoContent, listTaskLogs, type TaskLog, type TaskLogStatus } from '@/api/taskLogs'
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
const videoUrl = ref('')
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
    })
    rows.value = res.items
    total.value = res.total
    now.value = Date.now()
  } catch (e) {
    if (!silent) appStore.showError(errorText(e, t('taskLogs.loadFailed')))
  } finally {
    loading.value = false
  }
}

function reload() {
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

function revokeVideo() {
  if (videoUrl.value) URL.revokeObjectURL(videoUrl.value)
  videoUrl.value = ''
}
function closeDetail() {
  detailRow.value = null
  revokeVideo()
}

async function openResult(row: TaskLog) {
  if (row.type === 'video' && props.admin) {
    window.open(row.result_url, '_blank', 'noopener,noreferrer')
    return
  }
  busyId.value = row.task_id
  try {
    if (row.type === 'video') {
      revokeVideo()
      videoUrl.value = URL.createObjectURL(await fetchVideoContent(row.task_id))
      detailRow.value = row
    } else {
      const url = URL.createObjectURL(await downloadBatchImages(!!props.admin, row.task_id))
      const a = document.createElement('a')
      a.href = url
      a.download = `${row.task_id}.zip`
      a.click()
      URL.revokeObjectURL(url)
    }
  } catch (e) {
    appStore.showError(errorText(e, t('taskLogs.resultFailed')))
  } finally {
    busyId.value = ''
  }
}

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
  revokeVideo()
})
</script>
