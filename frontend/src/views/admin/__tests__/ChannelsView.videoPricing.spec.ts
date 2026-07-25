import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import type { Channel } from '@/api/admin/channels'
import ChannelsView from '../ChannelsView.vue'

const { getAllGroups, listChannels, updateChannel, showError } = vi.hoisted(() => ({
  getAllGroups: vi.fn(),
  listChannels: vi.fn(),
  updateChannel: vi.fn(),
  showError: vi.fn(),
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    channels: {
      list: listChannels,
      update: updateChannel,
      create: vi.fn(),
      delete: vi.fn(),
      syncPricingModels: vi.fn(),
    },
    groups: { getAll: getAllGroups },
    accounts: { list: vi.fn(), getById: vi.fn() },
    settings: { getWebSearchEmulationConfig: vi.fn().mockResolvedValue({ enabled: false }) },
  },
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({ showError, showSuccess: vi.fn() }),
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return { ...actual, useI18n: () => ({ t: (key: string) => key }) }
})

function makeChannel(): Channel {
  const videoPricing = {
    platform: 'openai',
    models: ['sora-2'],
    description: '',
    billing_mode: 'video' as const,
    input_price: null,
    output_price: null,
    cache_write_price: null,
    cache_read_price: null,
    image_input_price: null,
    image_output_price: null,
    per_request_price: null,
    video_price_per_second: 0,
    video_default_seconds: 10,
    video_allowed_seconds: [15, 5, 10],
    intervals: [{
      min_tokens: 0,
      max_tokens: null,
      tier_label: '1080p',
      input_price: null,
      output_price: null,
      cache_write_price: null,
      cache_read_price: null,
      per_request_price: null,
      video_price_per_second: 0.05,
      sort_order: 0,
    }],
  }
  const { description: _description, ...accountStatsVideoPricing } = videoPricing
  return {
    id: 7,
    name: 'Video',
    description: '',
    status: 'active',
    billing_model_source: 'channel_mapped',
    restrict_models: false,
    group_ids: [3],
    model_pricing: [videoPricing],
    model_mapping: {},
    apply_pricing_to_account_stats: true,
    account_stats_pricing_rules: [{
      name: 'Mirror',
      group_ids: [3],
      account_ids: [],
      pricing: [{ ...accountStatsVideoPricing, models: ['sora-rule'] }],
    }],
    created_at: '2026-07-01T00:00:00Z',
    updated_at: '2026-07-01T00:00:00Z',
  }
}

describe('ChannelsView video pricing', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    getAllGroups.mockResolvedValue([{ id: 3, name: 'OpenAI', platform: 'openai' }])
    listChannels.mockResolvedValue({ items: [], total: 0 })
    updateChannel.mockResolvedValue(makeChannel())
  })

  it('hydrates channel and account-stat video pricing and serializes it unchanged', async () => {
    const wrapper = mount(ChannelsView, {
      global: {
        stubs: {
          AppLayout: { template: '<div><slot /></div>' },
          TablePageLayout: { template: '<div><slot name="table" /></div>' },
          DataTable: true,
          Pagination: true,
          BaseDialog: true,
          ConfirmDialog: true,
          EmptyState: true,
          Select: true,
          Icon: true,
          PlatformIcon: true,
          Toggle: true,
          PricingEntryCard: true,
        },
      },
    })
    await flushPromises()

    const vm = wrapper.vm as unknown as {
      openEditDialog: (channel: Channel) => Promise<void>
      handleSubmit: () => Promise<void>
      form: { platforms: Array<{ model_pricing: Array<Record<string, unknown>>, account_stats_pricing_rules: Array<{ pricing: Array<Record<string, unknown>> }> }> }
    }
    const channel = makeChannel()
    await vm.openEditDialog(channel)

    expect(vm.form.platforms[0].model_pricing[0]).toMatchObject({
      video_price_per_second: 0,
      video_default_seconds: 10,
      video_allowed_seconds: [15, 5, 10],
    })
    expect(vm.form.platforms[0].account_stats_pricing_rules[0].pricing[0]).toMatchObject({
      video_price_per_second: 0,
      video_default_seconds: 10,
      video_allowed_seconds: [15, 5, 10],
    })

    await vm.handleSubmit()
    expect(showError).not.toHaveBeenCalled()
    expect(updateChannel).toHaveBeenCalledWith(7, expect.objectContaining({
      model_pricing: [expect.objectContaining({
        video_price_per_second: 0,
        video_default_seconds: 10,
        video_allowed_seconds: [5, 10, 15],
        intervals: [expect.objectContaining({ video_price_per_second: 0.05 })],
      })],
      account_stats_pricing_rules: [expect.objectContaining({
        pricing: [expect.objectContaining({ video_price_per_second: 0 })],
      })],
    }))
  })

  it('rejects duplicate edited durations before payload normalization and submits after correction', async () => {
    const wrapper = mount(ChannelsView, {
      global: {
        stubs: {
          AppLayout: { template: '<div><slot /></div>' },
          TablePageLayout: { template: '<div><slot name="table" /></div>' },
          DataTable: true,
          Pagination: true,
          BaseDialog: true,
          ConfirmDialog: true,
          EmptyState: true,
          Select: true,
          Icon: true,
          PlatformIcon: true,
          Toggle: true,
          PricingEntryCard: true,
        },
      },
    })
    await flushPromises()

    const vm = wrapper.vm as unknown as {
      openEditDialog: (channel: Channel) => Promise<void>
      handleSubmit: () => Promise<void>
      form: { platforms: Array<{ model_pricing: Array<{ video_allowed_seconds: number[] }> }> }
    }
    await vm.openEditDialog(makeChannel())

    vm.form.platforms[0].model_pricing[0].video_allowed_seconds = [5, 10, 10]
    await vm.handleSubmit()

    expect(showError).toHaveBeenCalledWith(expect.stringContaining('admin.channels.videoValidation.allowedSecondsUnique'))
    expect(updateChannel).not.toHaveBeenCalled()

    showError.mockClear()
    vm.form.platforms[0].model_pricing[0].video_allowed_seconds = [15, 5, 10]
    await vm.handleSubmit()

    expect(showError).not.toHaveBeenCalled()
    expect(updateChannel).toHaveBeenCalledWith(7, expect.objectContaining({
      model_pricing: [expect.objectContaining({ video_allowed_seconds: [5, 10, 15] })],
    }))
  })

  it('preserves invalid tier labels, rejects equivalents, then submits canonical distinct zero-price tiers', async () => {
    const wrapper = mount(ChannelsView, {
      global: {
        stubs: {
          AppLayout: { template: '<div><slot /></div>' },
          TablePageLayout: { template: '<div><slot name="table" /></div>' },
          DataTable: true,
          Pagination: true,
          BaseDialog: true,
          ConfirmDialog: true,
          EmptyState: true,
          Select: true,
          Icon: true,
          PlatformIcon: true,
          Toggle: true,
          PricingEntryCard: true,
        },
      },
    })
    await flushPromises()

    const channel = makeChannel()
    channel.model_pricing[0].video_price_per_second = null
    channel.model_pricing[0].intervals = [
      { ...channel.model_pricing[0].intervals[0], tier_label: '720p', video_price_per_second: 0 },
      { ...channel.model_pricing[0].intervals[0], tier_label: ' 720P ', video_price_per_second: 0.01 },
    ]
    const vm = wrapper.vm as unknown as {
      openEditDialog: (value: Channel) => Promise<void>
      handleSubmit: () => Promise<void>
      form: { platforms: Array<{ model_pricing: Array<{ intervals: Array<{ tier_label: string, video_price_per_second: number }> }> }> }
    }

    await vm.openEditDialog(channel)
    const intervals = vm.form.platforms[0].model_pricing[0].intervals
    expect(intervals.map(interval => interval.tier_label)).toEqual(['720p', ' 720P '])

    await vm.handleSubmit()
    expect(showError).toHaveBeenCalledWith(expect.stringContaining('tierLabelUnique'))
    expect(updateChannel).not.toHaveBeenCalled()

    showError.mockClear()
    intervals[0].tier_label = ' Preview '
    intervals[1].tier_label = ' CINEMA '
    intervals[1].video_price_per_second = 0
    await vm.handleSubmit()

    expect(showError).not.toHaveBeenCalled()
    expect(updateChannel).toHaveBeenCalledWith(7, expect.objectContaining({
      model_pricing: [expect.objectContaining({
        intervals: [
          expect.objectContaining({ tier_label: 'preview', video_price_per_second: 0 }),
          expect.objectContaining({ tier_label: 'cinema', video_price_per_second: 0 }),
        ],
      })],
    }))
  })
})
