import { mount } from '@vue/test-utils'
import { defineComponent, nextTick, ref } from 'vue'
import { describe, expect, it, vi } from 'vitest'
import PricingEntryCard from '../PricingEntryCard.vue'
import IntervalRow from '../IntervalRow.vue'
import type { IntervalFormEntry, PricingFormEntry } from '../types'

vi.mock('vue-i18n', async importOriginal => ({
  ...await importOriginal<typeof import('vue-i18n')>(),
  useI18n: () => ({
    t: (key: string) => ({
      'admin.channels.form.resolution': 'Resolution',
      'admin.channels.form.videoPricePerSecond': 'Price per second',
    }[key] || key),
  }),
}))

function makeEntry(over: Partial<PricingFormEntry> = {}): PricingFormEntry {
  return {
    models: [],
    description: '',
    billing_mode: 'video',
    input_price: null,
    output_price: null,
    cache_write_price: null,
    cache_read_price: null,
    image_output_price: null,
    image_input_price: null,
    per_request_price: null,
    video_price_per_second: null,
    video_default_seconds: null,
    video_allowed_seconds: [],
    intervals: [],
    ...over,
  } as PricingFormEntry
}

function makeInterval(over: Partial<IntervalFormEntry> = {}): IntervalFormEntry {
  return {
    min_tokens: 0,
    max_tokens: null,
    tier_label: '',
    input_price: null,
    output_price: null,
    cache_write_price: null,
    cache_read_price: null,
    per_request_price: null,
    video_price_per_second: null,
    sort_order: 0,
    ...over,
  } as IntervalFormEntry
}

function mountCard(entry = makeEntry()) {
  return mount(PricingEntryCard, {
    props: { entry, inputIdPrefix: 'test-entry', showDescription: false },
    global: {
      stubs: {
        Icon: true,
        ModelTagInput: true,
        Select: true,
      },
    },
  })
}

describe('PricingEntryCard video billing', () => {
  it('preserves active raw text through prop feedback and accepts external replacement', async () => {
    const Harness = defineComponent({
      components: { PricingEntryCard },
      setup() {
        const entry = ref(makeEntry())
        return { entry }
      },
      template: '<PricingEntryCard :entry="entry" input-id-prefix="harness" :show-description="false" @update="entry = $event" />',
    })
    const wrapper = mount(Harness, {
      global: { stubs: { Icon: true, ModelTagInput: true, Select: true } },
    })
    const allowed = wrapper.findAll('input')[2]

    await allowed.trigger('focus')
    for (const value of ['5', '5,', '5,1', '5,10']) {
      await allowed.setValue(value)
      expect(allowed.element.value).toBe(value)
    }
    await allowed.setValue('5,')
    await nextTick()
    expect(allowed.element.value).toBe('5,')

    const vm = wrapper.vm as unknown as { entry: PricingFormEntry }
    vm.entry = makeEntry({ video_allowed_seconds: [20, 30] })
    await nextTick()
    expect(allowed.element.value).toBe('20, 30')

    await allowed.setValue('20,30')
    await allowed.trigger('blur')
    expect(allowed.element.value).toBe('20, 30')
  })

  it('retains duplicate duration evidence through blur and clears it after correction', async () => {
    const wrapper = mountCard()
    const inputs = wrapper.findAll('input')

    expect(inputs).toHaveLength(3)
    await inputs[0].setValue('0.03')
    await inputs[1].setValue('10')
    await inputs[2].setValue('20, 5, 10, 5')

    const updates = wrapper.emitted('update') || []
    expect(updates[0][0]).toMatchObject({ video_price_per_second: '0.03' })
    expect(updates[1][0]).toMatchObject({ video_default_seconds: '10' })
    expect(updates[2][0]).toMatchObject({ video_allowed_seconds: [20, 5, 10, 5] })

    await inputs[2].trigger('blur')
    expect(wrapper.emitted('update')?.at(-1)?.[0]).toMatchObject({ video_allowed_seconds: [20, 5, 10, 5] })

    await inputs[2].setValue('20, 5, 10')
    await inputs[2].trigger('blur')
    expect(wrapper.emitted('update')?.at(-1)?.[0]).toMatchObject({ video_allowed_seconds: [20, 5, 10] })
  })

  it('adds resolution tiers with a video price field', async () => {
    const wrapper = mountCard()

    await wrapper.get('button.text-xs.text-primary-600').trigger('click')

    expect(wrapper.emitted('update')?.[0][0]).toMatchObject({
      intervals: [expect.objectContaining({ tier_label: '480p', video_price_per_second: null })],
    })
  })

  it('associates unique stable labels with all video controls', () => {
    const wrapper = mountCard(makeEntry({
      intervals: [
        makeInterval({ tier_label: '720p', video_price_per_second: 0.03 }),
        makeInterval({ tier_label: '1080p', video_price_per_second: 0.05 }),
      ],
    }))
    const ids = wrapper.findAll('input').map(input => input.attributes('id')).filter(Boolean)

    expect(new Set(ids).size).toBe(ids.length)
    for (const id of ids) {
      expect(wrapper.get(`label[for="${id}"]`).exists()).toBe(true)
    }
    expect(wrapper.get('#test-entry-video-default-seconds').attributes('required')).toBeDefined()
    expect(wrapper.get('#test-entry-video-allowed-seconds').attributes('aria-label')).toBeTruthy()
  })
})

describe('IntervalRow video billing', () => {
  it('shows resolution and USD-per-second inputs without token bounds', () => {
    const wrapper = mount(IntervalRow, {
      props: { interval: makeInterval({ tier_label: '1080p', video_price_per_second: 0.05 }), mode: 'video' as never, inputIdPrefix: 'test-tier' },
      global: {
        stubs: { Icon: true },
      },
    })

    expect(wrapper.text()).toContain('Resolution')
    expect(wrapper.text()).toContain('Price per second')
    expect(wrapper.text()).not.toContain('Min')
    expect(wrapper.findAll('input')).toHaveLength(2)
    expect(wrapper.get('.flex.items-start').classes()).toContain('flex-wrap')
  })

  it('emits raw resolution text so invalid evidence remains editable', async () => {
    const wrapper = mount(IntervalRow, {
      props: { interval: makeInterval({ tier_label: '720p', video_price_per_second: 0 }), mode: 'video' as never, inputIdPrefix: 'test-tier' },
      global: { stubs: { Icon: true } },
    })

    await wrapper.findAll('input')[0].setValue(' 720P ')

    expect(wrapper.emitted('update')?.at(-1)?.[0]).toMatchObject({ tier_label: ' 720P ' })
  })
})
