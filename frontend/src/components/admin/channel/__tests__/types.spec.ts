import { describe, expect, it } from 'vitest'
import { apiIntervalsToForm, apiVideoPricingToForm, formAccountStatsPricingToAPI, formIntervalsToAPI, formPricingToAPI, formVideoPricingToAPI, validateIntervals, validatePricingEntry, validateVideoPricing, type IntervalFormEntry, type PricingFormEntry } from '../types'
import type { BillingMode } from '@/api/admin/channels'

function makeInterval(over: Partial<IntervalFormEntry>): IntervalFormEntry {
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
  }
}

function makeVideoPricing(over: Partial<PricingFormEntry> = {}): PricingFormEntry {
  return {
    models: ['sora-2'],
    description: '',
    billing_mode: 'video' as BillingMode,
    input_price: null,
    output_price: null,
    cache_write_price: null,
    cache_read_price: null,
    image_output_price: null,
    per_request_price: null,
    video_price_per_second: 0.03,
    video_default_seconds: 10,
    video_allowed_seconds: [5, 10, 15],
    intervals: [],
    ...over,
  } as PricingFormEntry
}

function t(key: string, params?: Record<string, unknown>): string {
  return `${key}${params ? ` ${JSON.stringify(params)}` : ''}`
}

describe('validateIntervals', () => {
  describe('token mode', () => {
    it('rejects unbounded interval that is not last', () => {
      const intervals: IntervalFormEntry[] = [
        makeInterval({ min_tokens: 0, max_tokens: null, input_price: 1, output_price: 1 }),
        makeInterval({ min_tokens: 200000, max_tokens: 500000, input_price: 2, output_price: 2 }),
      ]
      expect(validateIntervals(intervals, 'token', t)).toContain('unboundedLast')
    })

    it('accepts unbounded interval at the end', () => {
      const intervals: IntervalFormEntry[] = [
        makeInterval({ min_tokens: 0, max_tokens: 200000, input_price: 1, output_price: 1 }),
        makeInterval({ min_tokens: 200000, max_tokens: null, input_price: 2, output_price: 2 }),
      ]
      expect(validateIntervals(intervals, 'token', t)).toBeNull()
    })

    it('rejects overlapping intervals', () => {
      const intervals: IntervalFormEntry[] = [
        makeInterval({ min_tokens: 0, max_tokens: 250000, input_price: 1, output_price: 1 }),
        makeInterval({ min_tokens: 200000, max_tokens: 500000, input_price: 2, output_price: 2 }),
      ]
      expect(validateIntervals(intervals, 'token', t)).toContain('overlap')
    })

    it('rejects unbounded interval in token mode', () => {
      const intervals: IntervalFormEntry[] = [
        makeInterval({ min_tokens: 0, max_tokens: null, input_price: 1, output_price: 1 }),
        makeInterval({ min_tokens: 100, max_tokens: 200, input_price: 2, output_price: 2 }),
      ]
      expect(validateIntervals(intervals, 'token', t)).toContain('unboundedLast')
    })
  })

  describe('image / per_request mode', () => {
    it('allows multiple unbounded tiers identified by label', () => {
      const intervals: IntervalFormEntry[] = [
        makeInterval({ tier_label: '1K', per_request_price: 0.04 }),
        makeInterval({ tier_label: '2K', per_request_price: 0.06 }),
        makeInterval({ tier_label: '4K', per_request_price: 0.08 }),
      ]
      expect(validateIntervals(intervals, 'image', t)).toBeNull()
      expect(validateIntervals(intervals, 'per_request', t)).toBeNull()
    })

    it('still rejects negative prices', () => {
      const intervals: IntervalFormEntry[] = [
        makeInterval({ tier_label: '1K', per_request_price: -1 }),
      ]
      expect(validateIntervals(intervals, 'image', t)).toContain('negativePrice')
    })

    it('still rejects max <= min on a single tier', () => {
      const intervals: IntervalFormEntry[] = [
        makeInterval({ tier_label: '1K', min_tokens: 100, max_tokens: 50, per_request_price: 0.04 }),
      ]
      expect(validateIntervals(intervals, 'image', t)).toContain('maxGreaterThanMin')
    })
  })

  describe('video mode', () => {
    it('serializes tier video prices unchanged', () => {
      const result = formIntervalsToAPI([
        makeInterval({
          tier_label: '1080p',
          video_price_per_second: 0.05,
        } as Partial<IntervalFormEntry>),
      ])

      expect(result[0]).toMatchObject({
        tier_label: '1080p',
        video_price_per_second: 0.05,
      })
    })

    it('does not apply token range checks to resolution tiers', () => {
      const intervals: IntervalFormEntry[] = [
        makeInterval({
          min_tokens: 100,
          max_tokens: 50,
          tier_label: '1080p',
          video_price_per_second: 0.05,
        } as Partial<IntervalFormEntry>),
      ]

      expect(validateIntervals(intervals, 'video' as BillingMode, t)).toBeNull()
    })
  })
})

describe('video pricing helpers', () => {
  it('trims primary pricing descriptions', () => {
    const entry = makeVideoPricing({ description: '  First line\nSecond line  ' })

    expect(formPricingToAPI(entry, 'openai').description).toBe('First line\nSecond line')
  })

  it('does not serialize descriptions in account stats pricing rules', () => {
    const injected = makeVideoPricing({ description: 'must not leave the form' })

    expect(formAccountStatsPricingToAPI(injected, 'openai')).not.toHaveProperty('description')
  })

  it('preserves duplicate server durations through hydration so validation rejects them', () => {
    const hydrated = apiVideoPricingToForm({
      video_price_per_second: 0.03,
      video_default_seconds: 10,
      video_allowed_seconds: [15, '10', 10, '5'] as unknown as number[],
    })

    expect(hydrated.video_allowed_seconds).toEqual([15, 10, 10, 5])
    expect(validateVideoPricing(makeVideoPricing(hydrated), t)).toContain('allowedSecondsUnique')
  })

  it('round-trips a valid server duration list canonically', () => {
    const hydrated = apiVideoPricingToForm({
      video_price_per_second: 0.03,
      video_default_seconds: 10,
      video_allowed_seconds: [15, 5, 10],
    })

    expect(hydrated.video_allowed_seconds).toEqual([15, 5, 10])
    expect(formVideoPricingToAPI(makeVideoPricing(hydrated)).video_allowed_seconds).toEqual([5, 10, 15])
  })

  it('round-trips default and tier video prices without token conversion', () => {
    const entry = makeVideoPricing({
      video_price_per_second: '0.03',
      video_default_seconds: '10',
      video_allowed_seconds: [15, 5, 10, 5],
      intervals: [makeInterval({ tier_label: '1080p', video_price_per_second: '0.05' } as Partial<IntervalFormEntry>)],
    })
    const apiValue = formVideoPricingToAPI(entry)

    expect(apiValue).toMatchObject({
      video_price_per_second: 0.03,
      video_default_seconds: 10,
      video_allowed_seconds: [5, 10, 15],
    })
    expect(formIntervalsToAPI(entry.intervals)[0]).toMatchObject({
      video_price_per_second: 0.05,
    })
    expect(apiVideoPricingToForm(apiValue)).toMatchObject({
      video_price_per_second: 0.03,
      video_default_seconds: 10,
      video_allowed_seconds: [5, 10, 15],
    })
  })

  it('rejects a default duration missing from allowed durations', () => {
    expect(validateVideoPricing(makeVideoPricing({ video_allowed_seconds: [5, 15] }), t)).toContain('defaultNotAllowed')
  })

  it('rejects duplicate allowed durations', () => {
    expect(validateVideoPricing(makeVideoPricing({ video_allowed_seconds: [5, 10, 10] }), t)).toContain('allowedSecondsUnique')
  })

  it.each([null, 0, 3601, 1.5])('requires an integer default duration in range: %s', value => {
    expect(validateVideoPricing(makeVideoPricing({ video_default_seconds: value }), t)).toContain('defaultSeconds')
  })

  it('rejects allowed durations outside the supported bounds', () => {
    expect(validateVideoPricing(makeVideoPricing({ video_allowed_seconds: [10, 3601] }), t)).toContain('allowedSecondsBounds')
  })

  it('requires either a default video price or a tier video price', () => {
    expect(validateVideoPricing(makeVideoPricing({ video_price_per_second: null }), t)).toContain('missingPrice')
  })

  it('accepts a tier price when the default price is absent and trims its resolution', () => {
    const entry = makeVideoPricing({
      video_price_per_second: null,
      intervals: [makeInterval({ tier_label: ' 1080p ', video_price_per_second: 0 })],
    })

    expect(validateVideoPricing(entry, t)).toBeNull()
    expect(formIntervalsToAPI(entry.intervals)[0]).toMatchObject({ tier_label: '1080p', video_price_per_second: 0 })
  })

  it.each([Number.NaN, Number.POSITIVE_INFINITY, -0.01])('rejects invalid default video price: %s', value => {
    expect(validateVideoPricing(makeVideoPricing({ video_price_per_second: value }), t)).toContain('invalidPrice')
  })

  it.each([Number.NaN, Number.POSITIVE_INFINITY, -0.01])('rejects invalid tier video price: %s', value => {
    const entry = makeVideoPricing({
      video_price_per_second: null,
      intervals: [makeInterval({ tier_label: '1080p', video_price_per_second: value })],
    })
    expect(validateVideoPricing(entry, t)).toMatch(/nonFinitePrice|negativePrice/)
  })

  it('requires unique trimmed resolution labels', () => {
    const entry = makeVideoPricing({
      intervals: [
        makeInterval({ tier_label: '1080p', video_price_per_second: 0.05 }),
        makeInterval({ tier_label: ' 1080p ', video_price_per_second: 0.06 }),
      ],
    })
    expect(validateVideoPricing(entry, t)).toContain('tierLabelUnique')
  })

  it('rejects blank priced labels and case-insensitive quote-equivalent duplicates', () => {
    expect(validateVideoPricing(makeVideoPricing({
      intervals: [makeInterval({ tier_label: ' \t ', video_price_per_second: 0 })],
    }), t)).toContain('videoTierLabelRequired')

    for (const duplicate of ['720P', ' 720p ', '1280x720']) {
      expect(validateVideoPricing(makeVideoPricing({
        intervals: [
          makeInterval({ tier_label: '720p', video_price_per_second: 0 }),
          makeInterval({ tier_label: duplicate, video_price_per_second: 0.01 }),
        ],
      }), t)).toContain('tierLabelUnique')
    }
  })

  it('preserves invalid raw tier evidence during hydration', () => {
    const intervals = apiIntervalsToForm([
      makeInterval({ tier_label: '720p', video_price_per_second: 0 }),
      makeInterval({ tier_label: ' 720P ', video_price_per_second: 0.01 }),
    ])

    expect(intervals.map(interval => interval.tier_label)).toEqual(['720p', ' 720P '])
    expect(validateVideoPricing(makeVideoPricing({ intervals }), t)).toContain('tierLabelUnique')
  })

  it('accepts arbitrary distinct labels and canonicalizes a valid video payload', () => {
    const entry = makeVideoPricing({
      video_price_per_second: null,
      intervals: [
        makeInterval({ tier_label: ' Preview ', video_price_per_second: 0 }),
        makeInterval({ tier_label: ' CINEMA ', video_price_per_second: 0.05 }),
      ],
    })

    expect(validateVideoPricing(entry, t)).toBeNull()
    expect(formPricingToAPI(entry, 'openai').intervals).toEqual([
      expect.objectContaining({ tier_label: 'preview', video_price_per_second: 0 }),
      expect.objectContaining({ tier_label: 'cinema', video_price_per_second: 0.05 }),
    ])
  })
})

describe('billing mode isolation', () => {
  it('ignores hidden invalid video fields and video interval prices in token mode', () => {
    const entry = makeVideoPricing({
      billing_mode: 'token',
      input_price: 1,
      output_price: 2,
      video_price_per_second: -1,
      video_default_seconds: 0,
      video_allowed_seconds: [10, 10],
      intervals: [makeInterval({ input_price: 1, output_price: 2, video_price_per_second: -1 })],
    })

    expect(validatePricingEntry(entry, t)).toBeNull()
  })

  it.each(['image', 'per_request'] as const)('ignores stale token and video fields in %s mode', mode => {
    const entry = makeVideoPricing({
      billing_mode: mode,
      input_price: Number.NaN,
      video_price_per_second: -1,
      video_default_seconds: 0,
      video_allowed_seconds: [10, 10],
      per_request_price: 0,
      intervals: [],
    })

    expect(validatePricingEntry(entry, t)).toBeNull()
  })

  it('ignores stale token and per-request prices in video mode', () => {
    const entry = makeVideoPricing({
      input_price: -1,
      per_request_price: Number.NaN,
      intervals: [makeInterval({ tier_label: '1080p', video_price_per_second: 0, input_price: -1, per_request_price: -1 })],
    })

    expect(validatePricingEntry(entry, t)).toBeNull()
  })

  it.each(['token', 'image', 'per_request', 'video'] as const)('serializes only %s mode fields', mode => {
    const entry = makeVideoPricing({
      billing_mode: mode,
      input_price: 0,
      output_price: 2,
      cache_write_price: 3,
      cache_read_price: 4,
      image_output_price: 5,
      per_request_price: 0,
      video_price_per_second: 0,
      video_default_seconds: 10,
      video_allowed_seconds: [15, 5, 10],
      intervals: [makeInterval({
        min_tokens: 12,
        max_tokens: 24,
        tier_label: ' 1080p ',
        input_price: 0,
        output_price: 2,
        cache_write_price: 3,
        cache_read_price: 4,
        per_request_price: 0,
        video_price_per_second: 0,
      })],
    })

    const result = formPricingToAPI(entry, 'openai')

    expect(result.video_price_per_second).toBe(mode === 'video' ? 0 : null)
    expect(result.video_default_seconds).toBe(mode === 'video' ? 10 : null)
    expect(result.video_allowed_seconds).toEqual(mode === 'video' ? [5, 10, 15] : [])
    expect(result.per_request_price).toBe(mode === 'image' || mode === 'per_request' ? 0 : null)
    expect(result.input_price).toBe(mode === 'token' ? 0 : null)
    expect(result.intervals[0]).toMatchObject({
      min_tokens: mode === 'video' ? 0 : 12,
      max_tokens: mode === 'video' ? null : 24,
      input_price: mode === 'token' ? 0 : null,
      per_request_price: mode === 'image' || mode === 'per_request' ? 0 : null,
      video_price_per_second: mode === 'video' ? 0 : null,
    })
  })

  it('keeps video drafts local across mode transitions without leaking them into payloads', () => {
    const entry = makeVideoPricing({
      video_price_per_second: 0,
      video_default_seconds: 10,
      video_allowed_seconds: [10],
      per_request_price: 0,
      input_price: 0,
      intervals: [makeInterval({ tier_label: '1080p', video_price_per_second: 0, input_price: 0, per_request_price: 0 })],
    })

    for (const mode of ['token', 'image', 'per_request'] as const) {
      entry.billing_mode = mode
      const payload = formPricingToAPI(entry, 'openai')
      expect(payload.video_price_per_second).toBeNull()
      expect(payload.video_default_seconds).toBeNull()
      expect(payload.video_allowed_seconds).toEqual([])
      expect(payload.intervals[0].video_price_per_second).toBeNull()
    }

    entry.billing_mode = 'video'
    expect(formPricingToAPI(entry, 'openai')).toMatchObject({
      video_price_per_second: 0,
      video_default_seconds: 10,
      video_allowed_seconds: [10],
      intervals: [expect.objectContaining({ tier_label: '1080p', video_price_per_second: 0 })],
    })
  })
})
