import { afterEach, describe, expect, it, vi } from 'vitest'
import { mount, type VueWrapper } from '@vue/test-utils'
import { nextTick } from 'vue'
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
      description: '',
      billing_mode: 'video',
      input_price: null,
      output_price: null,
      cache_write_price: null,
      cache_read_price: null,
      image_input_price: null,
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

function mountModel(model: UserSupportedModel) {
  const wrapper = mount(SupportedModelChip, { props: { model }, attachTo: document.body })
  wrappers.push(wrapper)
  return wrapper
}

function tooltip(): HTMLElement {
  const element = document.body.querySelector<HTMLElement>('[role="tooltip"]')
  if (!element) throw new Error('tooltip was not rendered')
  return element
}

function tooltipText(): string {
  return tooltip().textContent ?? ''
}

function expectTooltipHidden() {
  expect(tooltip().style.display).toBe('none')
}

function expectTooltipVisible() {
  expect(tooltip().style.display).not.toBe('none')
}

async function openOnMouseenter(wrapper: VueWrapper) {
  await wrapper.get('[tabindex="0"]').trigger('mouseenter')
  await nextTick()
}

afterEach(() => {
  for (const wrapper of wrappers.splice(0)) wrapper.unmount()
})

describe('SupportedModelChip', () => {
  it('shows a visible tooltip with video pricing, any-duration policy, and resolution tiers on mouseenter', async () => {
    const wrapper = mountModel(makeModel())

    expectTooltipHidden()
    await openOnMouseenter(wrapper)
    expectTooltipVisible()

    expect(tooltipText()).toContain('Per-second video')
    expect(tooltipText()).toContain('$0.08 / second')
    expect(tooltipText()).toContain('Default duration')
    expect(tooltipText()).toContain('10 seconds')
    expect(tooltipText()).toContain('Any duration')
    expect(tooltipText()).toContain('1080p')
    expect(tooltipText()).toContain('$0.12 / second')
  })

  it('sorts and de-duplicates allowed video durations', async () => {
    const wrapper = mountModel(makeModel({
      pricing: {
        ...makeModel().pricing!,
        video_allowed_seconds: [15, 5, 10, 5],
      },
    }))
    await openOnMouseenter(wrapper)

    expectTooltipVisible()
    expect(tooltipText()).toContain('5 seconds, 10 seconds, 15 seconds')
  })

  it('treats null allowed video durations as any duration', async () => {
    const wrapper = mountModel(makeModel({
      pricing: {
        ...makeModel().pricing!,
        video_allowed_seconds: null,
      },
    }))
    await openOnMouseenter(wrapper)

    expectTooltipVisible()
    expect(tooltipText()).toContain('Any duration')
  })

  it('treats a missing runtime allowed-duration field as any duration', async () => {
    const wrapper = mountModel(makeModel({
      pricing: {
        ...makeModel().pricing!,
        video_allowed_seconds: undefined,
      } as unknown as NonNullable<UserSupportedModel['pricing']>,
    }))
    await openOnMouseenter(wrapper)

    expectTooltipVisible()
    expect(tooltipText()).toContain('Any duration')
  })

  it('keeps tiny nonzero video prices visible with the shared formatter', async () => {
    const wrapper = mountModel(makeModel({
      pricing: {
        ...makeModel().pricing!,
        video_price_per_second: 0.0000001,
        intervals: [{ ...makeModel().pricing!.intervals[0], video_price_per_second: 0.0000002 }],
      },
    }))
    await openOnMouseenter(wrapper)

    expectTooltipVisible()
    expect(tooltipText()).toContain('$1.000000000e-7 / second')
    expect(tooltipText()).toContain('$2.000000000e-7 / second')
  })

  it('shows a priced video tier without a missing default price', async () => {
    const wrapper = mountModel(makeModel({
      pricing: {
        ...makeModel().pricing!,
        video_price_per_second: null,
      },
    }))
    await openOnMouseenter(wrapper)

    expectTooltipVisible()
    expect(tooltipText()).toContain('1080p')
    expect(tooltipText()).toContain('$0.12 / second')
    expect(tooltipText()).not.toContain('Price per second')
    expect(tooltipText()).not.toContain('- / second')
  })

  it('keeps per-request pricing unchanged', async () => {
    const wrapper = mountModel(makeModel({
      pricing: {
        ...makeModel().pricing!,
        billing_mode: 'per_request',
        per_request_price: 0.25,
      },
    }))
    await openOnMouseenter(wrapper)

    expectTooltipVisible()
    expect(tooltipText()).toContain('Per Request')
    expect(tooltipText()).toContain('$0.25 / request')
  })

  it('opens on keyboard focus and hides on blur', async () => {
    const wrapper = mountModel(makeModel())
    const trigger = wrapper.get('[tabindex="0"]')

    expectTooltipHidden()
    trigger.element.focus()
    await nextTick()
    expectTooltipVisible()

    trigger.element.blur()
    await nextTick()
    expectTooltipHidden()
  })
})
