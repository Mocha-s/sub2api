import { describe, expect, it } from 'vitest'
import {
  BILLING_MODE_IMAGE,
  BILLING_MODE_TOKEN,
  BILLING_MODE_VIDEO,
  getDisplayBillingMode,
  isImageUsage,
  isVideoUsage,
  netAccountCost,
  netActualCost,
  netTotalCost,
} from '../billingMode'

describe('billingMode helpers', () => {
  it('prefers explicit video mode over image_count', () => {
    expect(
      getDisplayBillingMode({ image_count: 1, billing_mode: BILLING_MODE_VIDEO })
    ).toBe(BILLING_MODE_VIDEO)
    expect(isImageUsage({ image_count: 1, billing_mode: BILLING_MODE_VIDEO })).toBe(false)
  })

  it('infers image when image_count set and mode missing', () => {
    expect(getDisplayBillingMode({ image_count: 2, billing_mode: null })).toBe(BILLING_MODE_IMAGE)
  })

  it('keeps token mode even with image_count', () => {
    expect(
      getDisplayBillingMode({ image_count: 1, billing_mode: BILLING_MODE_TOKEN })
    ).toBe(BILLING_MODE_TOKEN)
  })
})

describe('video and refund billing helpers', () => {
  it('recognizes video usage only from the explicit billing mode', () => {
    expect(isVideoUsage({ billing_mode: 'video' })).toBe(true)
    expect(isVideoUsage({ billing_mode: 'token', video_count: 1 })).toBe(false)
    expect(isVideoUsage({ billing_mode: null, video_count: 1 })).toBe(false)
  })

  it('prefers backend net values and otherwise clamps gross minus refund at zero', () => {
    expect(netActualCost({ actual_cost: 0.4, refunded_cost: 0.4 })).toBe(0)
    expect(netActualCost({ actual_cost: 0.3, refunded_cost: 0.4 })).toBe(0)
    expect(netActualCost({ actual_cost: 0.4, refunded_cost: null })).toBe(0.4)
    expect(netActualCost({ actual_cost: 0.4, refunded_cost: 0.1, net_actual_cost: 0.27 })).toBe(0.27)

    expect(netTotalCost({ total_cost: 0.4, refunded_total_cost: 0.15 })).toBe(0.25)
    expect(netAccountCost({ account_stats_cost: 0.3, refunded_account_cost: 0.3 })).toBe(0)
    expect(netAccountCost({ total_cost: 0.4, account_rate_multiplier: 0.5, refunded_account_cost: 0.1 })).toBe(0.1)
  })

  it('treats custom account stats cost as the final gross charge and preserves explicit backend net', () => {
    expect(netAccountCost({
      account_stats_cost: 0.3,
      account_rate_multiplier: 2.5,
      refunded_account_cost: 0.1,
    })).toBe(0.2)
    expect(netAccountCost({
      account_stats_cost: 0.3,
      account_rate_multiplier: 2.5,
      refunded_account_cost: 0.1,
      net_account_cost: 0.17,
    })).toBe(0.17)
  })
})
