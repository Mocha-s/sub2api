/**
 * Admin Video Task Settlement API
 * Manual refund / confirm-charge operations for admin operators.
 */

import { apiClient } from '../client'

export interface VideoTaskSettlementInfo {
  public_task_id: string
  state: 'reserved' | 'charged' | 'released' | 'refunded' | string
  actual_cost_usd: number
  refunded_cost_usd: number
  charged_at?: string | null
  refunded_at?: string | null
}

export async function getSettlementByUsageLog(usageLogId: number): Promise<VideoTaskSettlementInfo | null> {
  const res = await apiClient.get<{ data: VideoTaskSettlementInfo }>(
    `/admin/video-task-settlements/by-usage/${usageLogId}`
  )
  return res.data.data ?? null
}

export async function adminRefundVideoTask(taskId: string, reason: string): Promise<{ applied: boolean }> {
  const res = await apiClient.post<{ data: { applied: boolean } }>(
    `/admin/video-task-settlements/${taskId}/refund`,
    { reason }
  )
  return res.data.data
}

export async function adminConfirmVideoTask(taskId: string): Promise<{ applied: boolean }> {
  const res = await apiClient.post<{ data: { applied: boolean } }>(
    `/admin/video-task-settlements/${taskId}/confirm`
  )
  return res.data.data
}
