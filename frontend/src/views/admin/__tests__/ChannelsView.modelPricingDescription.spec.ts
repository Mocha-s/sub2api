import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import type { Channel } from '@/api/admin/channels'
import type { PricingFormEntry } from '@/components/admin/channel/types'
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

function makePricing(description: string) {
  return {
    platform: 'openai',
    models: ['gpt-description'],
    description,
    billing_mode: 'token' as const,
    input_price: 0.000001,
    output_price: 0.000002,
    cache_write_price: null,
    cache_read_price: null,
    image_input_price: null,
    image_output_price: null,
    per_request_price: null,
    video_price_per_second: null,
    video_default_seconds: null,
    video_allowed_seconds: [],
    intervals: [],
  }
}

function makeChannel(): Channel {
  const primaryPricing = makePricing('Primary line\nVisible copy')
  const injectedAccountStatsPricing = {
    ...makePricing('must not leave the form'),
    models: ['gpt-account-stats'],
  }

  return {
    id: 21,
    name: 'Descriptions',
    description: '',
    status: 'active',
    billing_model_source: 'channel_mapped',
    restrict_models: false,
    group_ids: [3],
    model_pricing: [primaryPricing],
    model_mapping: {},
    apply_pricing_to_account_stats: true,
    account_stats_pricing_rules: [{
      name: 'Nested',
      group_ids: [3],
      account_ids: [],
      pricing: [injectedAccountStatsPricing],
    }],
    created_at: '2026-07-01T00:00:00Z',
    updated_at: '2026-07-01T00:00:00Z',
  } as Channel
}

function mountView() {
  return mount(ChannelsView, {
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
}

describe('ChannelsView model pricing descriptions', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    getAllGroups.mockResolvedValue([{ id: 3, name: 'OpenAI', platform: 'openai' }])
    listChannels.mockResolvedValue({ items: [], total: 0 })
    updateChannel.mockResolvedValue(makeChannel())
  })

  it('serializes descriptions only on primary model pricing', async () => {
    const wrapper = mountView()
    await flushPromises()

    const vm = wrapper.vm as unknown as {
      openEditDialog: (channel: Channel) => Promise<void>
      handleSubmit: () => Promise<void>
      form: {
        platforms: Array<{
          model_pricing: PricingFormEntry[]
          account_stats_pricing_rules: Array<{ pricing: PricingFormEntry[] }>
        }>
      }
    }
    await vm.openEditDialog(makeChannel())

    expect(vm.form.platforms[0].model_pricing[0].description).toBe('Primary line\nVisible copy')
    expect(vm.form.platforms[0].account_stats_pricing_rules[0].pricing[0].description).toBe('')

    await vm.handleSubmit()

    expect(showError).not.toHaveBeenCalled()
    const submitted = updateChannel.mock.calls.at(-1)?.[1]
    expect(submitted.model_pricing[0]).toMatchObject({
      models: ['gpt-description'],
      description: 'Primary line\nVisible copy',
    })
    expect(submitted.account_stats_pricing_rules[0].pricing[0]).toMatchObject({
      models: ['gpt-account-stats'],
    })
    expect(submitted.account_stats_pricing_rules[0].pricing[0]).not.toHaveProperty('description')
  })
})
