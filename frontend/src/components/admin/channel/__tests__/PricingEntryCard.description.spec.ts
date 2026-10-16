import { mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'
import PricingEntryCard from '../PricingEntryCard.vue'
import type { PricingFormEntry } from '../types'

vi.mock('vue-i18n', async importOriginal => ({
  ...await importOriginal<typeof import('vue-i18n')>(),
  useI18n: () => ({
    t: (key: string) => ({
      'admin.channels.form.pricingDescription': 'Description',
      'admin.channels.form.pricingDescriptionPlaceholder': 'Optional model pricing description',
    }[key] || key),
  }),
}))

function makeEntry(over: Partial<PricingFormEntry> = {}): PricingFormEntry {
  return {
    models: [],
    description: 'Plain text',
    billing_mode: 'token',
    input_price: null,
    output_price: null,
    cache_write_price: null,
    cache_read_price: null,
    image_input_price: null,
    image_output_price: null,
    per_request_price: null,
    video_price_per_second: null,
    video_default_seconds: null,
    video_allowed_seconds: [],
    intervals: [],
    ...over,
  } as PricingFormEntry
}

function mountCard(entry = makeEntry(), showDescription = true) {
  return mount(PricingEntryCard, {
    props: { entry, inputIdPrefix: 'test-entry', showDescription },
    global: {
      stubs: {
        Icon: true,
        ModelTagInput: true,
        Select: true,
      },
    },
  })
}

describe('PricingEntryCard description editor', () => {
  it('edits model pricing descriptions with a fixed length counter', async () => {
    const wrapper = mountCard()
    const textarea = wrapper.get('[data-test="pricing-description"]')

    expect(textarea.attributes('maxlength')).toBe('500')
    expect(wrapper.get('[data-test="pricing-description-count"]').text()).toBe('10/500')

    await textarea.setValue('Updated\nCopy')

    expect(wrapper.emitted('update')?.[0]?.[0]).toMatchObject({ description: 'Updated\nCopy' })
  })

  it('hides model pricing descriptions for account-stat pricing cards', () => {
    const wrapper = mountCard(makeEntry(), false)

    expect(wrapper.find('[data-test="pricing-description"]').exists()).toBe(false)
  })
})
