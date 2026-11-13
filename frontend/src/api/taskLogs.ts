/**
 * 任务日志（视频 + 批量图片）API，用户端与管理端共用。
 */

import { apiClient } from './client'
import type { PaginatedResponse } from '@/types'

export type TaskLogType = 'video' | 'batch_image' | 'image'
export type TaskLogStatus = 'queued' | 'running' | 'succeeded' | 'failed' | 'cancelled' | 'expired'

export interface TaskLog {
  type: TaskLogType
  task_id: string
  platform: string
  model: string
  status: TaskLogStatus
  raw_status: string
  progress: number
  submitted_at: string
  finished_at: string | null
  resolution?: string
  seconds?: number
  item_count?: number
  success_count?: number
  fail_count?: number
  cost_usd: number
  cost_known?: boolean
  prompt: string
  error_code?: string
  error_message?: string
  has_result: boolean
  request_mode?: 'sync' | 'async'
  // 以下仅管理端返回
  result_url?: string
  settlement_state?: string
  actual_cost_usd?: number
  refunded_cost_usd?: number
  user_id?: number
  user_email?: string
}

export interface TaskLogQuery {
  page?: number
  page_size?: number
  type?: string
  status?: string
  platform?: string
  task_id?: string
  start?: string
  end?: string
  user_id?: number
}

const base = (admin: boolean) => (admin ? '/admin/task-logs' : '/task-logs')

export async function listTaskLogs(admin: boolean, params: TaskLogQuery, signal?: AbortSignal): Promise<PaginatedResponse<TaskLog>> {
  const { data } = await apiClient.get(base(admin), { params, signal })
  return data
}

// 视频和压缩包可能很大，不套用默认的 30 秒超时。
export async function fetchVideoContent(admin: boolean, taskId: string, signal?: AbortSignal): Promise<Blob> {
  const { data } = await apiClient.get(`${base(admin)}/video/${encodeURIComponent(taskId)}/content`, {
    responseType: 'blob',
    timeout: 0,
    signal
  })
  return data
}

export async function fetchImageContent(admin: boolean, taskId: string, index: number, signal?: AbortSignal): Promise<Blob> {
  const { data } = await apiClient.get(`${base(admin)}/image/${encodeURIComponent(taskId)}/content`, {
    params: { index },
    responseType: 'blob',
    timeout: 0,
    signal
  })
  return data
}

export async function downloadBatchImages(admin: boolean, batchId: string): Promise<Blob> {
  const { data } = await apiClient.get(`${base(admin)}/batch-image/${encodeURIComponent(batchId)}/download`, {
    responseType: 'blob',
    timeout: 0
  })
  return data
}
