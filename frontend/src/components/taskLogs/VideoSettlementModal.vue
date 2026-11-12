<template>
  <BaseDialog :show="!!row" :title="t('admin.usage.videoSettlement')" width="narrow" @close="close">
    <div v-if="row" class="space-y-3 text-sm">
      <div class="flex items-center justify-between">
        <span class="text-gray-500 dark:text-gray-400">Task ID</span>
        <span class="font-mono text-xs text-gray-700 dark:text-gray-300">{{ row.task_id }}</span>
      </div>
      <div class="flex items-center justify-between">
        <span class="text-gray-500 dark:text-gray-400">{{ t('admin.usage.videoSettlementState') }}</span>
        <span class="font-medium text-gray-700 dark:text-gray-300">{{ stateLabel }}</span>
      </div>
      <div class="flex items-center justify-between">
        <span class="text-gray-500 dark:text-gray-400">{{ t('admin.usage.tokenRanking.columns.cost') }}</span>
        <span class="font-medium text-gray-700 dark:text-gray-300">${{ (row.actual_cost_usd ?? 0).toFixed(6) }}</span>
      </div>
      <div v-if="(row.refunded_cost_usd ?? 0) > 0" class="flex items-center justify-between">
        <span class="text-gray-500 dark:text-gray-400">{{ t('admin.usage.videoSettlementRefunded') }}</span>
        <span class="font-medium text-amber-600 dark:text-amber-400">${{ (row.refunded_cost_usd ?? 0).toFixed(6) }}</span>
      </div>
      <div v-if="state === 'charged'" class="pt-1">
        <label class="mb-1 block text-xs text-gray-500 dark:text-gray-400" for="video-settlement-refund-reason">
          {{ t('admin.usage.videoSettlementRefundReason') }}
        </label>
        <input id="video-settlement-refund-reason" v-model="refundReason" type="text" class="input" />
      </div>
    </div>

    <template #footer>
      <button type="button" class="btn btn-ghost" :disabled="acting" @click="close">
        {{ t(state === 'charged' || state === 'reserved' ? 'common.cancel' : 'common.close') }}
      </button>
      <template v-if="state === 'charged'">
        <button type="button" class="btn btn-danger" :disabled="acting" @click="act('refund')">
          {{ t('admin.usage.videoSettlementRefund') }}
        </button>
        <button type="button" class="btn btn-primary" :disabled="acting" @click="act('confirm')">
          {{ t('admin.usage.videoSettlementConfirm') }}
        </button>
      </template>
      <button v-else-if="state === 'reserved'" type="button" class="btn btn-danger" :disabled="acting" @click="confirmingRelease = true">
        {{ t('admin.usage.videoSettlementRelease') }}
      </button>
    </template>
  </BaseDialog>

  <ConfirmDialog
    :show="confirmingRelease"
    :title="t('admin.usage.videoSettlementRelease')"
    :message="t('admin.usage.videoSettlementReleaseConfirm')"
    danger
    @confirm="act('release')"
    @cancel="confirmingRelease = false"
  />
</template>

<script setup lang="ts">
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAppStore } from '@/stores/app'
import BaseDialog from '@/components/common/BaseDialog.vue'
import ConfirmDialog from '@/components/common/ConfirmDialog.vue'
import type { TaskLog } from '@/api/taskLogs'
import { adminConfirmVideoTask, adminRefundVideoTask, adminReleaseVideoTask } from '@/api/admin/videoTaskSettlement'
import { settlementLabelKey } from '@/utils/taskLog'

type Action = 'refund' | 'confirm' | 'release'

const props = defineProps<{ row: TaskLog | null }>()
const emit = defineEmits<{ close: []; done: [] }>()

const { t } = useI18n()
const appStore = useAppStore()

const acting = ref(false)
const refundReason = ref('')
const confirmingRelease = ref(false)

const state = computed(() => props.row?.settlement_state ?? '')
const stateLabel = computed(() => (settlementLabelKey[state.value] ? t(settlementLabelKey[state.value]) : state.value || '-'))

const successKey: Record<Action, string> = {
  refund: 'admin.usage.videoSettlementRefundSuccess',
  confirm: 'admin.usage.videoSettlementConfirmSuccess',
  release: 'admin.usage.videoSettlementReleaseSuccess'
}

async function act(action: Action) {
  if (!props.row) return
  confirmingRelease.value = false
  acting.value = true
  try {
    const id = props.row.task_id
    const { applied } =
      action === 'refund'
        ? await adminRefundVideoTask(id, refundReason.value)
        : action === 'confirm'
          ? await adminConfirmVideoTask(id)
          : await adminReleaseVideoTask(id)
    if (applied) appStore.showSuccess(t(successKey[action]))
    else appStore.showError(t('admin.usage.videoSettlementNotApplied'))
    emit('done')
    close()
  } catch (e: unknown) {
    appStore.showError((e as { message?: string })?.message || String(e))
  } finally {
    acting.value = false
  }
}

function close() {
  refundReason.value = ''
  confirmingRelease.value = false
  emit('close')
}
</script>
