<template>
  <BaseDialog
    :show="visible"
    :title="t('admin.usage.videoSettlement')"
    width="narrow"
    @close="close"
  >
    <div v-if="loading" class="py-6 text-center text-sm text-gray-500 dark:text-gray-400">
      {{ t('common.loading') }}...
    </div>

    <div v-else-if="error" class="rounded-lg border border-red-200 bg-red-50 px-4 py-3 text-sm text-red-700 dark:border-red-500/30 dark:bg-red-500/10 dark:text-red-300">
      {{ error }}
    </div>

    <div v-else-if="settlement" class="space-y-3 text-sm">
      <div class="flex items-center justify-between">
        <span class="text-gray-500 dark:text-gray-400">Task ID</span>
        <span class="font-mono text-xs text-gray-700 dark:text-gray-300">{{ settlement.public_task_id }}</span>
      </div>
      <div class="flex items-center justify-between">
        <span class="text-gray-500 dark:text-gray-400">{{ t('admin.usage.videoSettlementState') }}</span>
        <span :class="stateClass">{{ stateLabel }}</span>
      </div>
      <div class="flex items-center justify-between">
        <span class="text-gray-500 dark:text-gray-400">{{ t('admin.usage.tokenRanking.columns.cost') }}</span>
        <span class="font-medium text-gray-700 dark:text-gray-300">${{ settlement.actual_cost_usd.toFixed(6) }}</span>
      </div>
      <div v-if="settlement.refunded_cost_usd > 0" class="flex items-center justify-between">
        <span class="text-gray-500 dark:text-gray-400">{{ t('admin.usage.videoSettlementRefunded') }}</span>
        <span class="font-medium text-amber-600 dark:text-amber-400">${{ settlement.refunded_cost_usd.toFixed(6) }}</span>
      </div>

      <div v-if="settlement.state === 'charged'" class="pt-1">
        <label class="mb-1 block text-xs text-gray-500 dark:text-gray-400">{{ t('admin.usage.videoSettlementRefundReason') }}</label>
        <input
          v-model="refundReason"
          type="text"
          class="w-full rounded-lg border border-gray-300 bg-white px-3 py-1.5 text-sm dark:border-dark-600 dark:bg-dark-800 dark:text-white"
          :placeholder="t('admin.usage.videoSettlementRefundReason')"
        />
      </div>
    </div>

    <div v-else class="py-6 text-center text-sm text-gray-500 dark:text-gray-400">
      {{ t('admin.usage.videoSettlementNotFound') }}
    </div>

    <template v-if="settlement?.state === 'charged'" #footer>
      <button type="button" class="btn btn-ghost" :disabled="acting" @click="close">
        {{ t('common.cancel') }}
      </button>
      <button type="button" class="btn btn-danger" :disabled="acting" @click="doRefund">
        {{ t('admin.usage.videoSettlementRefund') }}
      </button>
      <button type="button" class="btn btn-primary" :disabled="acting" @click="doConfirm">
        {{ t('admin.usage.videoSettlementConfirm') }}
      </button>
    </template>
    <template v-else #footer>
      <button type="button" class="btn btn-ghost" @click="close">
        {{ t('common.close') }}
      </button>
    </template>
  </BaseDialog>
</template>

<script setup lang="ts">
import { ref, computed, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAppStore } from '@/stores/app'
import BaseDialog from '@/components/common/BaseDialog.vue'
import type { VideoTaskSettlementInfo } from '@/api/admin/videoTaskSettlement'
import { getSettlementByUsageLog, adminRefundVideoTask, adminConfirmVideoTask } from '@/api/admin/videoTaskSettlement'

const props = defineProps<{
  usageLogId: number | null
  visible: boolean
}>()

const emit = defineEmits<{
  close: []
  success: [action: 'refund' | 'confirm']
}>()

const { t } = useI18n()
const appStore = useAppStore()

const loading = ref(false)
const error = ref<string | null>(null)
const settlement = ref<VideoTaskSettlementInfo | null>(null)
const acting = ref(false)
const refundReason = ref('')

watch(
  () => [props.visible, props.usageLogId] as const,
  async ([vis, id]) => {
    if (!vis || id == null) return
    loading.value = true
    error.value = null
    settlement.value = null
    try {
      settlement.value = await getSettlementByUsageLog(id)
    } catch (e: unknown) {
      error.value = e instanceof Error ? e.message : String(e)
    } finally {
      loading.value = false
    }
  },
  { immediate: false }
)

const stateLabel = computed(() => {
  const map: Record<string, string> = {
    charged: t('admin.usage.videoSettlementCharged'),
    refunded: t('admin.usage.videoSettlementRefunded'),
    released: t('admin.usage.videoSettlementReleased'),
    reserved: t('admin.usage.videoSettlementReserved'),
  }
  return map[settlement.value?.state ?? ''] ?? (settlement.value?.state ?? '-')
})

const stateClass = computed(() => {
  const s = settlement.value?.state
  if (s === 'charged') return 'font-medium text-blue-600 dark:text-blue-400'
  if (s === 'refunded') return 'font-medium text-amber-600 dark:text-amber-400'
  if (s === 'released') return 'font-medium text-gray-500'
  return 'font-medium text-gray-700 dark:text-gray-300'
})

async function doRefund() {
  if (!settlement.value) return
  acting.value = true
  try {
    await adminRefundVideoTask(settlement.value.public_task_id, refundReason.value)
    appStore.showSuccess(t('admin.usage.videoSettlementRefundSuccess'))
    emit('success', 'refund')
    close()
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    acting.value = false
  }
}

async function doConfirm() {
  if (!settlement.value) return
  acting.value = true
  try {
    await adminConfirmVideoTask(settlement.value.public_task_id)
    appStore.showSuccess(t('admin.usage.videoSettlementConfirmSuccess'))
    emit('success', 'confirm')
    close()
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    acting.value = false
  }
}

function close() {
  refundReason.value = ''
  error.value = null
  emit('close')
}
</script>
