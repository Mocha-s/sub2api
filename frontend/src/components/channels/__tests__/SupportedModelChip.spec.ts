import { afterEach, describe, expect, it, vi } from 'vitest'
import { mount, type VueWrapper } from '@vue/test-utils'
import type { UserSupportedModel } from '@/api/channels'

vi.mock('vue-i18n', () => ({
  useI18n: () => ({
    t: (key: string) => ({
      'availableChannels.pricing.billingMode': 'Billing Mode',
      'availableChannels.pricing.billingModeToken': 'Per Token',
      'availableChannels.pricing.billingModePerRequest': 'Per Request',
      'availableChannels.pricing.billingModeImage': 'Per Image',
      'availableChannels.pricing.billingModeVideo': 'Per-second video',
      'availableChannels.pricing.perRequestPrice': 'Per Request',
      'availableChannels.pricing.videoPricePerSecond': 'Price per second',
      'availableChannels.pricing.videoDefaultSeconds': 'Default duration',
      'availableChannels.pricing.videoAllowedSeconds': 'Duration policy',
      'availableChannels.pricing.anyDuration': 'Any duration',
      'availableChannels.pricing.unitPerRequest': '/ request',
      'availableChannels.pricing.unitPerSecond': '/ second',
      'availableChannels.pricing.unitSeconds': 'seconds',
      'availableChannels.pricing.intervals': 'Tiered Pricing',
    }[key] ?? key),
  }),
}))

import SupportedModelChip from '../SupportedModelChip.vue'

const wrappers: VueWrapper[] = []

function makeModel(overrides: Partial<UserSupportedModel> = {}): UserSupportedModel {
  return {
    name: 'sora-video',
    platform: 'openai',
    pricing: {
      billing_mode: 'video',
      input_price: null,
      output_price: null,
      cache_write_price: null,
      cache_read_price: null,
      image_output_price: null,
      per_request_price: null,
      video_price_per_second: 0.08,
      video_default_seconds: 10,
      video_allowed_seconds: [],
      intervals: [
        {
          min_tokens: 0,
          max_tokens: null,
          tier_label: '1080p',
          input_price: null,
          output_price: null,
          cache_write_price: null,
          cache_read_price: null,
          per_request_price: null,
          video_price_per_second: 0.12,
        },
      ],
    },
    ...overrides,
  } as UserSupportedModel
}

async function mountAndOpen(model: UserSupportedModel) {
  const wrapper = mount(SupportedModelChip, { props: { model }, attachTo: document.body })
  wrappers.push(wrapper)
  await wrapper.get('[tabindex="0"]').trigger('mouseenter')
  await new Promise(resolve => setTimeout(resolve, 0))
  return wrapper
}

afterEach(() => {
  for (const wrapper of wrappers.splice(0)) wrapper.unmount()
})

describe('SupportedModelChip', () => {
  it('shows video pricing, any-duration policy, and resolution tiers', async () => {
    await mountAndOpen(makeModel())

    expect(document.body.textContent).toContain('Per-second video')
    expect(document.body.textContent).toContain('$0.080000 / second')
    expect(document.body.textContent).toContain('Default duration')
    expect(document.body.textContent).toContain('10 seconds')
    expect(document.body.textContent).toContain('Any duration')
    expect(document.body.textContent).toContain('1080p')
    expect(document.body.textContent).toContain('$0.120000 / second')
    expect(document.body.textContent).not.toContain('$0.000000 / $0.000000')
  })

  it('sorts and de-duplicates allowed video durations', async () => {
    await mountAndOpen(makeModel({
      pricing: {
        ...makeModel().pricing!,
        video_allowed_seconds: [15, 5, 10, 5],
      },
    }))

    expect(document.body.textContent).toContain('5 seconds, 10 seconds, 15 seconds')
  })

  it('keeps per-request pricing unchanged', async () => {
    await mountAndOpen(makeModel({
      pricing: {
        ...makeModel().pricing!,
        billing_mode: 'per_request',
        per_request_price: 0.25,
      },
    }))

    expect(document.body.textContent).toContain('Per Request')
    expect(document.body.textContent).toContain('$0.25 / request')
  })
})
