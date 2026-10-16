import { describe, expect, it } from 'vitest'

import en from '../locales/en'
import zh from '../locales/zh'

describe('model pricing description locale keys', () => {
  it('contains English copy for the pricing description editor', () => {
    expect(en.admin.channels.form.pricingDescription).toBe('Pricing Description')
    expect(en.admin.channels.form.pricingDescriptionPlaceholder).toBe('Optional notes shown with this model pricing')
  })

  it('contains Chinese copy for the pricing description editor', () => {
    expect(zh.admin.channels.form.pricingDescription).toBe('定价描述')
    expect(zh.admin.channels.form.pricingDescriptionPlaceholder).toBe('可选，展示在该模型定价中')
  })
})
