import type { TaskLog } from '@/api/taskLogs'

type Translate = (key: string, params?: Record<string, unknown>) => string

export function taskDurationMs(row: TaskLog, now: number = Date.now()): number {
  const end = row.finished_at ? Date.parse(row.finished_at) : now
  return end - Date.parse(row.submitted_at)
}

export function formatTaskDuration(ms: number): string {
  const s = Math.max(0, Math.floor(ms / 1000))
  if (s < 60) return `${s}s`
  if (s < 3600) return `${Math.floor(s / 60)}m ${s % 60}s`
  return `${Math.floor(s / 3600)}h ${Math.floor((s % 3600) / 60)}m`
}

export function taskSummary(row: TaskLog, t: Translate): string {
  const parts = [row.model]
  if (row.type === 'video') {
    if (row.resolution) parts.push(row.resolution)
    if (row.seconds) parts.push(`${row.seconds}s`)
  } else {
    parts.push(t('taskLogs.batchItems', { total: row.item_count ?? 0, success: row.success_count ?? 0 }))
  }
  parts.push(`$${row.cost_usd.toFixed(4)}`)
  return parts.join(' · ')
}

export const isTaskActive = (row: TaskLog): boolean => row.status === 'queued' || row.status === 'running'

export const canSettle = (row: TaskLog, admin: boolean): boolean =>
  admin && row.type === 'video' && !!row.settlement_state

// 管理端视频直接打开上游地址，只接受 http(s)，避免上游返回 javascript: 之类的地址。
export function hasResultLink(row: TaskLog, admin: boolean): boolean {
  if (!row.has_result) return false
  if (admin && row.type === 'video') return /^https?:\/\//i.test(row.result_url ?? '')
  return true
}

export const settlementLabelKey: Record<string, string> = {
  reserved: 'admin.usage.videoSettlementReserved',
  charged: 'admin.usage.videoSettlementCharged',
  refunded: 'admin.usage.videoSettlementRefunded',
  released: 'admin.usage.videoSettlementReleased'
}
