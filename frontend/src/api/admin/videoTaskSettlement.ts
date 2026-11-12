/**
 * Admin Video Task Settlement API
 * Manual refund / confirm-charge / release-hold operations for admin operators.
 */

import { apiClient } from '../client'

async function settle(taskId: string, action: 'refund' | 'confirm' | 'release', body?: object): Promise<{ applied: boolean }> {
  const { data } = await apiClient.post(`/admin/video-task-settlements/${encodeURIComponent(taskId)}/${action}`, body)
  return data
}

export const adminRefundVideoTask = (taskId: string, reason: string) => settle(taskId, 'refund', { reason })
export const adminConfirmVideoTask = (taskId: string) => settle(taskId, 'confirm')
export const adminReleaseVideoTask = (taskId: string) => settle(taskId, 'release')
