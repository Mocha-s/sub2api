import type { AccountStatsModelPricing, BillingMode, ChannelModelPricing, PricingInterval } from '@/api/admin/channels'

type TranslateFn = (key: string, params?: Record<string, unknown>) => string

export interface IntervalFormEntry {
  min_tokens: number
  max_tokens: number | null
  tier_label: string
  input_price: number | string | null
  output_price: number | string | null
  cache_write_price: number | string | null
  cache_read_price: number | string | null
  per_request_price: number | string | null
  video_price_per_second: number | string | null
  sort_order: number
}

export interface PricingFormEntry {
  models: string[]
  description: string
  billing_mode: BillingMode
  input_price: number | string | null
  output_price: number | string | null
  cache_write_price: number | string | null
  cache_read_price: number | string | null
  image_input_price: number | string | null
  image_output_price: number | string | null
  per_request_price: number | string | null
  video_price_per_second: number | string | null
  video_default_seconds: number | string | null
  video_allowed_seconds: number[]
  intervals: IntervalFormEntry[]
}

// 价格转换：后端存 per-token，前端显示 per-MTok ($/1M tokens)
const MTOK = 1_000_000

export function toNullableNumber(val: number | string | null | undefined): number | null {
  if (val === null || val === undefined || val === '') return null
  const num = Number(val)
  return isNaN(num) ? null : num
}

/** 前端显示值($/MTok) → 后端存储值(per-token) */
export function mTokToPerToken(val: number | string | null | undefined): number | null {
  const num = toNullableNumber(val)
  return num === null ? null : parseFloat((num / MTOK).toPrecision(10))
}

/** 后端存储值(per-token) → 前端显示值($/MTok) */
export function perTokenToMTok(val: number | null | undefined): number | null {
  if (val === null || val === undefined) return null
  // toPrecision(10) 消除 IEEE 754 浮点乘法精度误差，如 5e-8 * 1e6 = 0.04999...96 → 0.05
  return parseFloat((val * MTOK).toPrecision(10))
}

/** Normalizes video durations while retaining invalid values for submit-time feedback. */
export function normalizeVideoAllowedSeconds(seconds: number[]): number[] {
  return [...new Set(seconds)].sort((a, b) => a - b)
}

/** Returns the canonical label used by backend video quote tier matching. */
export function normalizeVideoTierLabel(value: string): string {
  const label = value.trim().toLowerCase()
  switch (label) {
    case '854x480':
    case '480p':
      return '480p'
    case '1280x720':
    case '720p':
      return '720p'
    case '1920x1080':
    case '1080p':
      return '1080p'
    case '3840x2160':
    case '2160p':
    case '4k':
      return '4k'
    default:
      return label
  }
}

/** Video prices are already USD/s and must not use token-price conversion. */
export function apiVideoPricingToForm(
  pricing: Pick<ChannelModelPricing, 'video_price_per_second' | 'video_default_seconds' | 'video_allowed_seconds'>,
): Pick<PricingFormEntry, 'video_price_per_second' | 'video_default_seconds' | 'video_allowed_seconds'> {
  return {
    video_price_per_second: pricing.video_price_per_second,
    video_default_seconds: pricing.video_default_seconds,
    video_allowed_seconds: (pricing.video_allowed_seconds || []).map(seconds => Number(seconds)),
  }
}

/** Converts video pricing fields without applying the token-price display conversion. */
export function formVideoPricingToAPI(
  pricing: Pick<PricingFormEntry, 'video_price_per_second' | 'video_default_seconds' | 'video_allowed_seconds'>,
): Pick<ChannelModelPricing, 'video_price_per_second' | 'video_default_seconds' | 'video_allowed_seconds'> {
  return {
    video_price_per_second: toNullableNumber(pricing.video_price_per_second),
    video_default_seconds: toNullableNumber(pricing.video_default_seconds),
    video_allowed_seconds: normalizeVideoAllowedSeconds(pricing.video_allowed_seconds || []),
  }
}

export function apiIntervalsToForm(intervals: PricingInterval[]): IntervalFormEntry[] {
  return (intervals || []).map(iv => ({
    min_tokens: iv.min_tokens,
    max_tokens: iv.max_tokens,
    tier_label: iv.tier_label || '',
    input_price: perTokenToMTok(iv.input_price),
    output_price: perTokenToMTok(iv.output_price),
    cache_write_price: perTokenToMTok(iv.cache_write_price),
    cache_read_price: perTokenToMTok(iv.cache_read_price),
    per_request_price: iv.per_request_price,
    video_price_per_second: iv.video_price_per_second,
    sort_order: iv.sort_order
  }))
}

export function formIntervalsToAPI(intervals: IntervalFormEntry[]): PricingInterval[] {
  return (intervals || []).map(iv => ({
    min_tokens: iv.min_tokens,
    max_tokens: iv.max_tokens,
    tier_label: iv.tier_label.trim(),
    input_price: mTokToPerToken(iv.input_price),
    output_price: mTokToPerToken(iv.output_price),
    cache_write_price: mTokToPerToken(iv.cache_write_price),
    cache_read_price: mTokToPerToken(iv.cache_read_price),
    per_request_price: toNullableNumber(iv.per_request_price),
    video_price_per_second: toNullableNumber(iv.video_price_per_second),
    sort_order: iv.sort_order
  }))
}

function formIntervalsForMode(intervals: IntervalFormEntry[], mode: BillingMode): PricingInterval[] {
  return (intervals || []).map(iv => ({
    min_tokens: mode === 'video' ? 0 : iv.min_tokens,
    max_tokens: mode === 'video' ? null : iv.max_tokens,
    tier_label: mode === 'video' ? normalizeVideoTierLabel(iv.tier_label) : iv.tier_label.trim(),
    input_price: mode === 'token' ? mTokToPerToken(iv.input_price) : null,
    output_price: mode === 'token' ? mTokToPerToken(iv.output_price) : null,
    cache_write_price: mode === 'token' ? mTokToPerToken(iv.cache_write_price) : null,
    cache_read_price: mode === 'token' ? mTokToPerToken(iv.cache_read_price) : null,
    per_request_price: mode === 'image' || mode === 'per_request' ? toNullableNumber(iv.per_request_price) : null,
    video_price_per_second: mode === 'video' ? toNullableNumber(iv.video_price_per_second) : null,
    sort_order: iv.sort_order,
  }))
}

function formPricingFieldsToAPI(entry: PricingFormEntry, platform: string): AccountStatsModelPricing {
  const tokenMode = entry.billing_mode === 'token'
  const requestMode = entry.billing_mode === 'image' || entry.billing_mode === 'per_request'
  const videoMode = entry.billing_mode === 'video'
  const video = videoMode
    ? formVideoPricingToAPI(entry)
    : { video_price_per_second: null, video_default_seconds: null, video_allowed_seconds: [] }

  return {
    platform,
    models: [...entry.models],
    billing_mode: entry.billing_mode,
    input_price: tokenMode ? mTokToPerToken(entry.input_price) : null,
    output_price: tokenMode ? mTokToPerToken(entry.output_price) : null,
    cache_write_price: tokenMode ? mTokToPerToken(entry.cache_write_price) : null,
    cache_read_price: tokenMode ? mTokToPerToken(entry.cache_read_price) : null,
    image_input_price: tokenMode ? mTokToPerToken(entry.image_input_price) : null,
    image_output_price: tokenMode ? mTokToPerToken(entry.image_output_price) : null,
    per_request_price: requestMode ? toNullableNumber(entry.per_request_price) : null,
    ...video,
    intervals: formIntervalsForMode(entry.intervals || [], entry.billing_mode),
  }
}

export function formPricingToAPI(entry: PricingFormEntry, platform: string): ChannelModelPricing {
  return {
    ...formPricingFieldsToAPI(entry, platform),
    description: entry.description.trim(),
  }
}

export function formAccountStatsPricingToAPI(entry: PricingFormEntry, platform: string): AccountStatsModelPricing {
  return formPricingFieldsToAPI(entry, platform)
}

// ── 模型模式冲突检测 ──────────────────────────────────────

interface ModelPattern {
  pattern: string
  prefix: string  // lowercase, 通配符去掉尾部 *
  wildcard: boolean
}

function toModelPattern(model: string): ModelPattern {
  const lower = model.toLowerCase()
  const wildcard = lower.endsWith('*')
  return {
    pattern: model,
    prefix: wildcard ? lower.slice(0, -1) : lower,
    wildcard,
  }
}

function patternsConflict(a: ModelPattern, b: ModelPattern): boolean {
  if (!a.wildcard && !b.wildcard) return a.prefix === b.prefix
  if (a.wildcard && !b.wildcard) return b.prefix.startsWith(a.prefix)
  if (!a.wildcard && b.wildcard) return a.prefix.startsWith(b.prefix)
  // 双通配符：任一前缀是另一前缀的前缀即冲突
  return a.prefix.startsWith(b.prefix) || b.prefix.startsWith(a.prefix)
}

/** 检测模型模式列表中的冲突，返回冲突的两个模式名；无冲突返回 null */
export function findModelConflict(models: string[]): [string, string] | null {
  const patterns = models.map(toModelPattern)
  for (let i = 0; i < patterns.length; i++) {
    for (let j = i + 1; j < patterns.length; j++) {
      if (patternsConflict(patterns[i], patterns[j])) {
        return [patterns[i].pattern, patterns[j].pattern]
      }
    }
  }
  return null
}

// ── 区间校验 ──────────────────────────────────────────────

/** 校验区间列表的合法性，返回错误消息；通过则返回 null
 *
 * mode 决定区间语义：
 * - token：区间是上下文 token 数分段 (min, max]，不能重叠，无上限段必须放最后
 * - per_request / image / video：区间是按 tier_label 分层（1K/2K/4K 等），后端按 label
 *   匹配，不依赖 min/max，因此跳过重叠 / last-unlimited 校验
 */
export function validateIntervals(
  intervals: IntervalFormEntry[],
  mode: BillingMode,
  t: TranslateFn,
): string | null {
  if (!intervals || intervals.length === 0) return null

  // 视频层级按分辨率标签匹配，不使用 token 范围。
  const sorted = mode === 'video'
    ? [...intervals]
    : [...intervals].sort((a, b) => a.min_tokens - b.min_tokens)

  for (let i = 0; i < sorted.length; i++) {
    const err = validateSingleInterval(sorted[i], i, mode, t)
    if (err) return err
  }

  if (mode === 'video') {
    const labels = sorted.map(interval => normalizeVideoTierLabel(interval.tier_label))
    if (new Set(labels).size !== labels.length) {
      return intervalValidationMessage(t, 'tierLabelUnique', {})
    }
  }

  // per_request / image / video 模式按 tier_label 匹配，不做 token 区间重叠校验
  if (mode !== 'token') return null
  return checkIntervalOverlap(sorted, t)
}

function intervalValidationMessage(
  t: TranslateFn,
  key: string,
  params: Record<string, unknown>,
): string {
  return t(`admin.channels.intervalValidation.${key}`, params)
}

function intervalPriceLabel(t: TranslateFn, key: string): string {
  return t(`admin.channels.intervalValidation.price.${key}`)
}

function validateSingleInterval(iv: IntervalFormEntry, idx: number, mode: BillingMode, t: TranslateFn): string | null {
  const index = idx + 1
  if (mode === 'video') {
    if (!iv.tier_label.trim()) {
      return intervalValidationMessage(
        t,
        'videoTierLabelRequired',
        { index },
      )
    }
    if (iv.video_price_per_second == null || iv.video_price_per_second === '') {
      return intervalValidationMessage(
        t,
        'videoTierPriceRequired',
        { index },
      )
    }
  } else {
    if (iv.min_tokens < 0) {
      return intervalValidationMessage(
        t,
        'negativeMin',
        { index, value: iv.min_tokens },
      )
    }
    if (iv.max_tokens != null) {
      if (iv.max_tokens <= 0) {
        return intervalValidationMessage(
          t,
          'maxPositive',
          { index, value: iv.max_tokens },
        )
      }
      if (iv.max_tokens <= iv.min_tokens) {
        return intervalValidationMessage(
          t,
          'maxGreaterThanMin',
          { index, max: iv.max_tokens, min: iv.min_tokens },
        )
      }
    }
  }
  return validateIntervalPrices(iv, idx, mode, t)
}

function validateIntervalPrices(iv: IntervalFormEntry, idx: number, mode: BillingMode, t: TranslateFn): string | null {
  const index = idx + 1
  const prices: [string, number | string | null][] = mode === 'token'
    ? [
        ['inputPrice', iv.input_price],
        ['outputPrice', iv.output_price],
        ['cacheWritePrice', iv.cache_write_price],
        ['cacheReadPrice', iv.cache_read_price],
      ]
    : mode === 'video'
      ? [['videoPricePerSecond', iv.video_price_per_second]]
      : [['perRequestPrice', iv.per_request_price]]
  for (const [key, val] of prices) {
    if (val != null && val !== '' && !Number.isFinite(Number(val))) {
      const field = intervalPriceLabel(t, key)
      return intervalValidationMessage(
        t,
        'nonFinitePrice',
        { index, field },
      )
    }
    if (val != null && val !== '' && Number(val) < 0) {
      const field = intervalPriceLabel(t, key)
      return intervalValidationMessage(
        t,
        'negativePrice',
        { index, field },
      )
    }
  }
  return null
}

function hasPrice(value: number | string | null): boolean {
  return value != null && value !== ''
}

function validatePricingPrices(entry: PricingFormEntry, t: TranslateFn): string | null {
  const prices: [string, number | string | null][] = entry.billing_mode === 'token'
    ? [
        ['inputPrice', entry.input_price],
        ['outputPrice', entry.output_price],
        ['cacheWritePrice', entry.cache_write_price],
        ['cacheReadPrice', entry.cache_read_price],
        ['imageTokenPrice', entry.image_output_price],
      ]
    : entry.billing_mode === 'video'
      ? [['videoPricePerSecond', entry.video_price_per_second]]
      : [['perRequestPrice', entry.per_request_price]]
  for (const [key, value] of prices) {
    if (!hasPrice(value)) continue
    const numberValue = Number(value)
    if (!Number.isFinite(numberValue) || numberValue < 0) {
      return t('admin.channels.pricingValidation.invalidPrice', {
        field: t(`admin.channels.form.${key}`),
      })
    }
  }
  return null
}

export function validateVideoPricing(entry: PricingFormEntry, t: TranslateFn): string | null {
  const priceError = validatePricingPrices(entry, t)
  if (priceError) return priceError

  const defaultSeconds = toNullableNumber(entry.video_default_seconds)
  if (
    defaultSeconds == null ||
    !Number.isInteger(defaultSeconds) ||
    defaultSeconds < 1 ||
    defaultSeconds > 3600
  ) {
    return t('admin.channels.videoValidation.defaultSeconds')
  }

  const allowedSeconds = entry.video_allowed_seconds || []
  if (allowedSeconds.some(seconds => !Number.isInteger(seconds) || seconds < 1 || seconds > 3600)) {
    return t('admin.channels.videoValidation.allowedSecondsBounds')
  }
  if (new Set(allowedSeconds).size !== allowedSeconds.length) {
    return t('admin.channels.videoValidation.allowedSecondsUnique')
  }
  if (allowedSeconds.length > 0 && !allowedSeconds.includes(defaultSeconds)) {
    return t('admin.channels.videoValidation.defaultNotAllowed')
  }

  if (!hasPrice(entry.video_price_per_second) && !entry.intervals.some(interval => hasPrice(interval.video_price_per_second))) {
    return t('admin.channels.videoValidation.missingPrice')
  }

  return validateIntervals(entry.intervals || [], entry.billing_mode, t)
}

export function validatePricingEntry(entry: PricingFormEntry, t: TranslateFn): string | null {
  if (entry.billing_mode === 'video') {
    return validateVideoPricing(entry, t)
  }

  const priceError = validatePricingPrices(entry, t)
  if (priceError) return priceError
  if (
    (entry.billing_mode === 'per_request' || entry.billing_mode === 'image') &&
    !hasPrice(entry.per_request_price) &&
    entry.intervals.length === 0
  ) {
    return t('admin.channels.form.perRequestPriceRequired')
  }
  return validateIntervals(entry.intervals || [], entry.billing_mode, t)
}

function checkIntervalOverlap(sorted: IntervalFormEntry[], t: TranslateFn): string | null {
  for (let i = 0; i < sorted.length; i++) {
    // 无上限区间必须是最后一个
    if (sorted[i].max_tokens == null && i < sorted.length - 1) {
      return intervalValidationMessage(
        t,
        'unboundedLast',
        { index: i + 1 },
      )
    }
    if (i === 0) continue
    const prev = sorted[i - 1]
    // (min, max] 语义：前一个区间上界 > 当前区间下界则重叠
    if (prev.max_tokens == null || prev.max_tokens > sorted[i].min_tokens) {
      const prevMax = prev.max_tokens == null ? '∞' : String(prev.max_tokens)
      return intervalValidationMessage(
        t,
        'overlap',
        { previousIndex: i, currentIndex: i + 1, previousMax: prevMax, currentMin: sorted[i].min_tokens },
      )
    }
  }
  return null
}

/** 平台对应的模型 tag 样式（背景+文字） */
export function getPlatformTagClass(platform: string): string {
  switch (platform) {
    case 'anthropic': return 'bg-orange-100 text-orange-700 dark:bg-orange-900/30 dark:text-orange-400'
    case 'openai': return 'bg-emerald-100 text-emerald-700 dark:bg-emerald-900/30 dark:text-emerald-400'
    case 'gemini': return 'bg-blue-100 text-blue-700 dark:bg-blue-900/30 dark:text-blue-400'
    case 'antigravity': return 'bg-purple-100 text-purple-700 dark:bg-purple-900/30 dark:text-purple-400'
    case 'grok': return 'bg-slate-100 text-slate-700 dark:bg-slate-800 dark:text-slate-300'
    default: return 'bg-gray-100 text-gray-700 dark:bg-gray-900/30 dark:text-gray-400'
  }
}

/** 平台对应的模型文字色（仅 text-*，用于 input/text 场景）— 与 getPlatformTagClass 同色系 */
export function getPlatformTextClass(platform: string): string {
  switch (platform) {
    case 'anthropic': return 'text-orange-700 dark:text-orange-400'
    case 'openai': return 'text-emerald-700 dark:text-emerald-400'
    case 'gemini': return 'text-blue-700 dark:text-blue-400'
    case 'antigravity': return 'text-purple-700 dark:text-purple-400'
    case 'grok': return 'text-slate-700 dark:text-slate-300'
    default: return ''
  }
}
