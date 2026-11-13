import { describe, expect, it } from 'vitest'
import type { TaskLog } from '@/api/taskLogs'
import {
  canSettle,
  formatTaskDuration,
  hasResultLink,
  isTaskActive,
  taskDurationMs,
  taskSummary
} from '../taskLog'

const row = (over: Partial<TaskLog> = {}): TaskLog => ({
  type: 'video',
  task_id: 'task_1',
  platform: 'openai',
  model: 'sora-2',
  status: 'succeeded',
  raw_status: 'completed',
  progress: 100,
  submitted_at: '2026-11-13T00:00:00Z',
  finished_at: '2026-11-13T00:01:23Z',
  cost_usd: 0.8,
  prompt: 'p',
  has_result: true,
  ...over
})
const t = (key: string, params?: Record<string, unknown>) => `${key}:${JSON.stringify(params ?? {})}`

describe('taskLog utils', () => {
  it('花费时间：已结束用结束时间，未结束用当前时间', () => {
    expect(taskDurationMs(row())).toBe(83_000)
    const now = Date.parse('2026-11-13T00:00:45Z')
    expect(taskDurationMs(row({ finished_at: null }), now)).toBe(45_000)
  })

  it('格式化耗时', () => {
    expect(formatTaskDuration(45_000)).toBe('45s')
    expect(formatTaskDuration(83_000)).toBe('1m 23s')
    expect(formatTaskDuration(3_723_000)).toBe('1h 2m')
    expect(formatTaskDuration(-5)).toBe('0s')
  })

  it('明细：视频带分辨率和时长，缺失时只剩模型和费用', () => {
    expect(taskSummary(row({ resolution: '720p', seconds: 8 }), t)).toBe('sora-2 · 720p · 8s · $0.8000')
    expect(taskSummary(row({ cost_usd: 0 }), t)).toBe('sora-2 · $0.0000')
  })

  it('明细：批量图片带张数', () => {
    const batch = row({ type: 'batch_image', model: 'imagen', item_count: 20, success_count: 18, cost_usd: 1.5 })
    expect(taskSummary(batch, t)).toBe('imagen · taskLogs.batchItems:{"total":20,"success":18} · $1.5000')
  })

  it('图片费用尚未关联账单时不显示为免费', () => {
    const image = row({ type: 'image', model: 'imagen', request_mode: 'async', item_count: 2, success_count: 2, cost_usd: 0, cost_known: false })
    expect(taskSummary(image, t)).toBe('imagen · taskLogs.requestModes.async:{} · taskLogs.imageItems:{"total":2,"success":2} · —')
    expect(taskSummary({ ...image, cost_known: true }, t)).toContain('$0.0000')
  })

  it('只有排队中和进行中的任务触发自动刷新', () => {
    expect(isTaskActive(row({ status: 'queued' }))).toBe(true)
    expect(isTaskActive(row({ status: 'running' }))).toBe(true)
    for (const status of ['succeeded', 'failed', 'cancelled', 'expired'] as const) {
      expect(isTaskActive(row({ status }))).toBe(false)
    }
  })

  it('结算入口只在管理端、有结算记录的视频行出现', () => {
    expect(canSettle(row({ settlement_state: 'charged' }), true)).toBe(true)
    expect(canSettle(row({ settlement_state: 'charged' }), false)).toBe(false)
    expect(canSettle(row(), true)).toBe(false)
    expect(canSettle(row({ type: 'batch_image', settlement_state: 'charged' }), true)).toBe(false)
  })

  it('结果入口使用内容代理，不依赖上游地址', () => {
    expect(hasResultLink(row())).toBe(true)
    expect(hasResultLink(row({ has_result: false }))).toBe(false)
    expect(hasResultLink(row({ type: 'batch_image' }))).toBe(true)
    expect(hasResultLink(row({ type: 'image' }))).toBe(true)
  })
})
