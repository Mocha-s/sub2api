import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { nextTick } from 'vue'

import type { UserAvailableChannel } from '@/api/channels'
import AvailableChannelsView from '../AvailableChannelsView.vue'

const { getAvailable, getUserGroupRates, showError } = vi.hoisted(() => ({
  getAvailable: vi.fn(),
  getUserGroupRates: vi.fn(),
  showError: vi.fn(),
}))

vi.mock('@/api/channels', () => ({
  default: { getAvailable },
}))

vi.mock('@/api/groups', () => ({
  default: { getUserGroupRates },
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({ showError, cachedPublicSettings: {} }),
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string, fallback?: string) => ({
        'availableChannels.searchPlaceholder': 'Search channels or models...',
        'availableChannels.columns.name': 'Channel',
        'availableChannels.columns.description': 'Description',
        'availableChannels.columns.platform': 'Platform',
        'availableChannels.columns.groups': 'Groups',
        'availableChannels.columns.supportedModels': 'Supported Models',
        'availableChannels.noPricing': 'Pricing not configured',
        'availableChannels.noModels': 'No model mapping configured',
        'availableChannels.empty': 'No data',
        'availableChannels.public': 'Public',
        'availableChannels.exclusive': 'Exclusive',
        'availableChannels.pricing.billingMode': 'Billing Mode',
        'availableChannels.pricing.billingModeToken': 'Per Token',
        'availableChannels.pricing.unitPerMillion': '/ 1M tokens',
      }[key] ?? fallback ?? key),
    }),
  }
})

let wrapper: VueWrapper | null = null

function makeChannels(): UserAvailableChannel[] {
  return [{
    name: 'Visible channel',
    description: 'Public channel copy',
    platforms: [{
      platform: 'openai',
      groups: [{
        id: 3,
        name: 'Public OpenAI',
        platform: 'openai',
        subscription_type: 'standard',
        rate_multiplier: 1,
        peak_rate_enabled: false,
        peak_start: '',
        peak_end: '',
        peak_rate_multiplier: 1,
        is_exclusive: false,
      }],
      supported_models: [{
        name: 'gpt-visible',
        platform: 'openai',
        pricing: {
          description: '<strong>Internal only</strong>\nSecond line',
          billing_mode: 'token',
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
        },
      }],
    }],
  }]
}

function mountView() {
  wrapper = mount(AvailableChannelsView, {
    attachTo: document.body,
    global: {
      stubs: {
        AppLayout: { template: '<div><slot /></div>' },
        TablePageLayout: { template: '<div><slot name="filters" /><slot name="table" /></div>' },
        Icon: true,
        PlatformIcon: true,
        GroupBadge: true,
      },
    },
  })
  return wrapper
}

afterEach(() => {
  wrapper?.unmount()
  wrapper = null
  document.body.innerHTML = ''
})

describe('AvailableChannelsView model pricing descriptions', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    getAvailable.mockResolvedValue(makeChannels())
    getUserGroupRates.mockResolvedValue({})
  })

  it('renders escaped multiline pricing descriptions without adding them to search', async () => {
    const mounted = mountView()
    await flushPromises()

    const vm = mounted.vm as unknown as { searchQuery: string }
    vm.searchQuery = 'internal only'
    await nextTick()

    expect(mounted.text()).toContain('No data')
    expect(mounted.text()).not.toContain('gpt-visible')

    vm.searchQuery = 'gpt-visible'
    await nextTick()

    expect(mounted.text()).toContain('gpt-visible')

    await mounted.get('[tabindex="0"]').trigger('mouseenter')
    await nextTick()
    await nextTick()

    const description = document.body.querySelector<HTMLElement>('[data-test="model-pricing-description"]')
    expect(description?.textContent).toBe('<strong>Internal only</strong>\nSecond line')
    expect(description?.className).toContain('whitespace-pre-wrap')
    expect(description?.innerHTML).toContain('&lt;strong&gt;Internal only&lt;/strong&gt;')
  })
})
