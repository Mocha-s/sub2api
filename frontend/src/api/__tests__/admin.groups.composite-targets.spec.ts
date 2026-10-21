import { beforeEach, describe, expect, it, vi } from 'vitest'

const { get, post, put, del } = vi.hoisted(() => ({
  get: vi.fn(),
  post: vi.fn(),
  put: vi.fn(),
  del: vi.fn()
}))

vi.mock('@/api/client', () => ({ apiClient: { get, post, put, delete: del } }))

import {
  createCompositeTarget,
  deleteCompositeTarget,
  listCompositeTargets,
  updateCompositeTarget
} from '@/api/admin/groups'

describe('admin composite target API', () => {
  beforeEach(() => {
    get.mockReset(); post.mockReset(); put.mockReset(); del.mockReset()
  })

  it('lists target references', async () => {
    get.mockResolvedValue({ data: [{ id: 1, target_group_id: 20 }] })
    await expect(listCompositeTargets(7)).resolves.toEqual([{ id: 1, target_group_id: 20 }])
    expect(get).toHaveBeenCalledWith('/admin/groups/7/composite-targets')
  })

  it('normalizes an empty response to an empty array', async () => {
    get.mockResolvedValue({ data: null })
    await expect(listCompositeTargets(7)).resolves.toEqual([])
  })

  it('writes target references and preserves the target payload', async () => {
    post.mockResolvedValue({ data: { id: 1, target_group_id: 20, priority: 10, enabled: true } })
    put.mockResolvedValue({ data: { id: 1, target_group_id: 20, priority: 2, enabled: false } })
    del.mockResolvedValue({ data: { message: 'ok' } })

    await createCompositeTarget(7, { target_group_id: 20, priority: 10, enabled: true })
    await updateCompositeTarget(7, 1, { priority: 2, enabled: false })
    await deleteCompositeTarget(7, 1)

    expect(post).toHaveBeenCalledWith('/admin/groups/7/composite-targets', { target_group_id: 20, priority: 10, enabled: true })
    expect(put).toHaveBeenCalledWith('/admin/groups/7/composite-targets/1', { priority: 2, enabled: false })
    expect(del).toHaveBeenCalledWith('/admin/groups/7/composite-targets/1')
  })
})
