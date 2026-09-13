const ipGeoMocks = vi.hoisted(() => ({
  getEntry: vi.fn(() => ({ status: 'idle' as const })),
  fetchOne: vi.fn(),
  fetchBatch: vi.fn(),
}))

const appStoreMocks = vi.hoisted(() => ({
  showSuccess: vi.fn(),
  showError: vi.fn(),
}))

vi.mock('@/utils/ipGeoLookup', () => ipGeoMocks)
vi.mock('@/stores/app', () => ({ useAppStore: () => appStoreMocks }))

import { describe, expect, it, vi, beforeEach } from 'vitest'
import { mount } from '@vue/test-utils'
import { nextTick } from 'vue'

import UsageTable from '../UsageTable.vue'

const setDesktopViewport = (desktop: boolean) => {
  Object.defineProperty(window, 'matchMedia', {
    configurable: true,
    writable: true,
    value: vi.fn().mockImplementation((query: string) => ({
      matches: query === '(min-width: 768px)' ? desktop : false,
      media: query,
      onchange: null,
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
      addListener: vi.fn(),
      removeListener: vi.fn(),
      dispatchEvent: vi.fn(),
    })),
  })
}

const messages: Record<string, string> = {
  'admin.usage.userDeletedBadge': 'Deleted',
  'usage.costDetails': 'Cost Breakdown',
  'admin.usage.inputCost': 'Input Cost',
  'admin.usage.outputCost': 'Output Cost',
  'admin.usage.cacheCreationCost': 'Cache Creation Cost',
  'admin.usage.cacheReadCost': 'Cache Read Cost',
  'usage.inputTokenPrice': 'Input price',
  'usage.outputTokenPrice': 'Output price',
  'usage.perMillionTokens': '/ 1M tokens',
  'usage.serviceTier': 'Service tier',
  'usage.serviceTierPriority': 'Fast',
  'usage.serviceTierFlex': 'Flex',
  'usage.serviceTierStandard': 'Standard',
  'usage.rate': 'Rate',
  'usage.accountMultiplier': 'Account rate',
  'usage.original': 'Original',
  'usage.userBilled': 'User billed',
  'usage.accountBilled': 'Account billed',
  'usage.grossCost': 'Gross',
  'usage.refund': 'Refund',
  'usage.netCost': 'Net',
  'usage.refunded': 'Refunded',
  'usage.refundedLabel': 'Refunded usage',
  'usage.videoMetadata': '{duration} · {resolution} · {count}',
  'usage.videoCount': '1 video | {count} videos',
  'usage.imageUnit': ' images',
  'usage.imageCount': 'Image count',
  'usage.imageBillingSize': 'Billing size',
  'usage.imageInputSize': 'Input size',
  'usage.imageOutputSize': 'Output size',
  'usage.imageSizeSource': 'Size source',
  'usage.imageSizeBreakdown': 'Size breakdown',
  'usage.imageSizeSourceOutput': 'Upstream output',
  'usage.imageSizeSourceInput': 'Request input',
  'usage.imageSizeSourceDefault': 'Default billing tier',
  'usage.imageSizeSourceLegacy': 'Legacy record',
  'usage.imageSizeSourceMissing': 'Not recorded',
  'usage.imageSizeNotRecorded': 'not recorded',
  'usage.imageSizeLegacyUnstandardized': 'legacy unstandardized',
  'usage.imageSizeUnknown': 'unknown',
  'usage.imageUnitPrice': 'Per-image price',
  'usage.imageTotalPrice': 'Image total price',
  'usage.videoResolution': 'Resolution',
  'usage.videoDuration': 'Duration',
  'usage.videoOutputCount': 'Output count',
  'usage.videoUnitPrice': 'Per-second price',
  'usage.videoPriceCalculation': 'Calculation',
  'admin.usage.billingModeToken': 'Token',
  'admin.usage.billingModePerRequest': 'Per request',
  'admin.usage.billingModeImage': 'Image',
	'admin.usage.requestIdCopied': 'Request ID copied',
	'keys.copied': 'Copied',
	'keys.copyToClipboard': 'Copy to clipboard',
	'common.copyFailed': 'Copy failed',
	'usage.requestedModel': 'Requested',
	'usage.sentUpstreamModel': 'Sent upstream',
	'usage.upstreamResponseModel': 'Upstream response',
	'usage.modelVariant': 'Possible version variant',
	'usage.modelMismatch': 'Different model',
  'admin.usage.billingModeVideo': 'Per-second video',
}

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string, params?: { count?: number }, plural?: number) => {
        const message = messages[key] ?? key
        const count = params?.count ?? plural
        const variant = message.includes('|')
          ? message.split('|')[count === 1 ? 0 : 1].trim()
          : message
        return count == null ? variant : variant.replace('{count}', String(count))
      },
    }),
  }
})

const DataTableStub = {
  props: ['data'],
  template: `
    <div>
      <div v-for="row in data" :key="row.request_id">
        <slot name="cell-model" :row="row" :value="row.model" />
        <slot name="cell-billing_mode" :row="row" />
        <slot name="cell-tokens" :row="row" />
        <slot name="cell-cost" :row="row" />
        <slot name="cell-request_id" :row="row" />
      </div>
    </div>
  `,
}

const baseImageRow = {
  request_id: 'req-admin-image',
  model: 'gpt-image-2',
  actual_cost: 0.4,
  total_cost: 0.4,
  account_rate_multiplier: 1,
  rate_multiplier: 1,
  service_tier: null,
  input_cost: 0,
  output_cost: 0,
  cache_creation_cost: 0,
  cache_read_cost: 0,
  input_tokens: 0,
  output_tokens: 0,
  cache_creation_tokens: 0,
  cache_read_tokens: 0,
  cache_creation_5m_tokens: 0,
  cache_creation_1h_tokens: 0,
  cache_ttl_overridden: false,
  billing_mode: 'image',
  image_count: 2,
  image_size: '2K',
  image_input_size: null,
  image_output_size: null,
  image_size_source: null,
  image_size_breakdown: null,
}

describe('admin UsageTable tooltip', () => {
  beforeEach(() => {
    setDesktopViewport(true)
    vi.spyOn(HTMLElement.prototype, 'getBoundingClientRect').mockReturnValue({
      x: 0,
      y: 0,
      top: 20,
      left: 20,
      right: 120,
      bottom: 40,
      width: 100,
      height: 20,
      toJSON: () => ({}),
    } as DOMRect)
  })

  it('marks only usage rows that actually applied long-context billing', () => {
    const wrapper = mount(UsageTable, {
      props: {
        data: [
          {
            ...baseImageRow,
            request_id: 'req-long-context-enabled',
            long_context_billing_applied: true,
          },
          {
            ...baseImageRow,
            request_id: 'req-long-context-disabled',
            long_context_billing_applied: false,
          },
        ],
        loading: false,
        columns: [],
      },
      global: {
        stubs: {
          DataTable: DataTableStub,
          EmptyState: true,
          Icon: true,
          Teleport: true,
        },
      },
    })

    expect(wrapper.findAll('[data-testid="long-context-billing-marker"]')).toHaveLength(1)
    expect(wrapper.get('[data-testid="long-context-billing-marker"]').text()).toBe('x2')
  })

  it('shows service tier and billing breakdown in cost tooltip', async () => {
    const row = {
      request_id: 'req-admin-1',
      actual_cost: 0.092883,
      total_cost: 0.092883,
      account_rate_multiplier: 1,
      rate_multiplier: 1,
      service_tier: 'priority',
      input_cost: 0.020285,
      output_cost: 0.00303,
      cache_creation_cost: 0,
      cache_read_cost: 0.069568,
      input_tokens: 4057,
      output_tokens: 101,
    }

    const wrapper = mount(UsageTable, {
      props: {
        data: [row],
        loading: false,
        columns: [],
      },
      global: {
        stubs: {
          DataTable: DataTableStub,
          EmptyState: true,
          Icon: true,
          Teleport: true,
        },
      },
    })

    const tooltipTriggers = wrapper.findAll('.group.relative')
    await tooltipTriggers[tooltipTriggers.length - 1].trigger('mouseenter')
    await nextTick()

    const text = wrapper.text()
    expect(text).toContain('Service tier')
    expect(text).toContain('Fast')
    expect(text).toContain('Rate')
    expect(text).toContain('1.00x')
    expect(text).toContain('Account rate')
    expect(text).toContain('User billed')
    expect(text).toContain('Account billed')
    expect(text).toContain('$0.092883')
    expect(text).toContain('$5.0000 / 1M tokens')
    expect(text).toContain('$30.0000 / 1M tokens')
    expect(text).toContain('$0.069568')
  })

  it('shows requested and upstream models separately for admin rows', () => {
    const row = {
      request_id: 'req-admin-model-1',
      model: 'claude-sonnet-4',
      upstream_model: 'claude-sonnet-4-20250514',
      actual_cost: 0,
      total_cost: 0,
      account_rate_multiplier: 1,
      rate_multiplier: 1,
      input_cost: 0,
      output_cost: 0,
      cache_creation_cost: 0,
      cache_read_cost: 0,
      input_tokens: 0,
      output_tokens: 0,
    }

    const wrapper = mount(UsageTable, {
      props: {
        data: [row],
        loading: false,
        columns: [],
      },
      global: {
        stubs: {
          DataTable: DataTableStub,
          EmptyState: true,
          Icon: true,
          Teleport: true,
        },
      },
    })

    const text = wrapper.text()
    expect(text).toContain('claude-sonnet-4')
    expect(text).toContain('claude-sonnet-4-20250514')
  })

	it.each([
		{
			name: 'possible version variant',
			responseModel: 'gpt-5.5-2026-08-01',
			expectedBadge: 'Possible version variant',
		},
		{
			name: 'different upstream model',
			responseModel: 'gpt-5.4',
			expectedBadge: 'Different model',
		},
	])('shows a compact upstream response audit marker for $name', ({ responseModel, expectedBadge }) => {
		const wrapper = mount(UsageTable, {
			props: {
				data: [{
					request_id: `req-${responseModel}`,
					model: 'gpt-5.6-sol',
					upstream_model: 'gpt-5.5',
					model_mapping_chain: 'gpt-5.6-sol→gpt-5.5',
					upstream_response_model: responseModel,
					upstream_model_mismatch: true,
				}],
				loading: false,
				columns: [],
			},
			global: {
				stubs: {
					DataTable: DataTableStub,
					EmptyState: true,
					Icon: true,
					Teleport: true,
				},
			},
		})

		const text = wrapper.text()
		expect(text).toContain('gpt-5.6-sol')
		expect(text).toContain('gpt-5.5')
		expect(text).toContain(responseModel)
		expect(text).toContain(expectedBadge)
	})

  it.each([
    {
      name: 'defaulted row',
      row: {
        ...baseImageRow,
        request_id: 'req-admin-default-image',
        image_size: '2K',
        image_input_size: 'auto',
        image_output_size: null,
        image_size_source: 'default',
      },
      expected: ['2K', 'Default billing tier', 'auto', 'unknown'],
    },
    {
      name: 'output-sourced row',
      row: {
        ...baseImageRow,
        request_id: 'req-admin-output-image',
        image_size: '4K',
        image_input_size: '1024x1024',
        image_output_size: '3840x2160',
        image_size_source: 'output',
        image_size_breakdown: { '4K': 1 },
      },
      expected: ['4K', 'Upstream output', '1024x1024', '3840x2160', '4K x 1'],
    },
    {
      name: 'input-sourced row',
      row: {
        ...baseImageRow,
        request_id: 'req-admin-input-image',
        image_size: '1K',
        image_input_size: '1024x1024',
        image_output_size: null,
        image_size_source: 'input',
      },
      expected: ['1K', 'Request input', '1024x1024', 'unknown'],
    },
    {
      name: 'legacy unstandardized row',
      row: {
        ...baseImageRow,
        request_id: 'req-admin-legacy-unstandardized-image',
        image_size: '512x512',
        image_input_size: null,
        image_output_size: null,
        image_size_source: null,
      },
      expected: ['legacy unstandardized: 512x512', 'Legacy record', 'unknown'],
    },
  ])('shows image usage metadata for $name', async ({ row, expected }) => {
    const wrapper = mount(UsageTable, {
      props: {
        data: [row],
        loading: false,
        columns: [],
      },
      global: {
        stubs: {
          DataTable: DataTableStub,
          EmptyState: true,
          Icon: true,
          Teleport: true,
        },
      },
    })

    await wrapper.find('.group.relative').trigger('mouseenter')
    await nextTick()

    const text = wrapper.text()
    expect(text).toContain('Image count')
    expect(text).toContain('Billing size')
    expect(text).toContain('Size source')
    expect(text).toContain('Input size')
    expect(text).toContain('Output size')
    expect(text).toContain('Per-image price')
    expect(text).toContain('Image total price')
    for (const value of expected) {
      expect(text).toContain(value)
    }
  })

  it.each([
    ['desktop', true],
    ['mobile', false],
  ])('supports keyboard, mouse, and tap interactions with linked ARIA state on %s', async (_viewport, desktop) => {
    setDesktopViewport(desktop)
    const wrapper = mount(UsageTable, {
      props: {
        data: [{
          ...baseImageRow,
          request_id: `req-accessible-${_viewport}`,
          billing_mode: 'video',
          image_count: 0,
          video_count: 1,
          video_resolution: '720p',
          video_duration_seconds: 5,
          refunded_cost: 0.4,
          refunded_total_cost: 0.4,
          refunded_at: '2026-07-12T00:00:00Z',
        }],
        loading: false,
        columns: [{ key: 'cost', label: 'Cost' }],
      },
      global: { stubs: { EmptyState: true, Icon: true, Teleport: true } },
    })
    await nextTick()

    const trigger = wrapper.get('[data-testid="cost-details-trigger"]')
    expect(trigger.element.tagName).toBe('BUTTON')
    expect(trigger.attributes('aria-expanded')).toBe('false')
    expect(trigger.classes()).toEqual(expect.arrayContaining([
      'h-8',
      'w-8',
      'focus-visible:ring-2',
    ]))
    expect(trigger.get('div').classes()).toEqual(expect.arrayContaining(['h-4', 'w-4']))

    await trigger.trigger('focus')
    await nextTick()
    const tooltip = wrapper.get('[role="tooltip"]')
    expect(trigger.attributes('aria-expanded')).toBe('true')
    expect(trigger.attributes('aria-controls')).toBe(tooltip.attributes('id'))
    expect(trigger.attributes('aria-describedby')).toBe(tooltip.attributes('id'))

    await trigger.trigger('blur')
    await nextTick()
    expect(wrapper.find('[role="tooltip"]').exists()).toBe(false)

    await trigger.trigger('focus')
    await nextTick()
    await trigger.trigger('keydown', { key: 'Escape' })
    await nextTick()
    expect(wrapper.find('[role="tooltip"]').exists()).toBe(false)
    expect(trigger.attributes('aria-expanded')).toBe('false')

    await trigger.trigger('click')
    await nextTick()
    expect(wrapper.find('[role="tooltip"]').exists()).toBe(true)
    await trigger.trigger('mouseleave')
    await nextTick()
    expect(wrapper.find('[role="tooltip"]').exists()).toBe(true)

    await trigger.trigger('click')
    await nextTick()
    expect(wrapper.find('[role="tooltip"]').exists()).toBe(false)

    await trigger.trigger('mouseenter')
    await nextTick()
    expect(wrapper.find('[role="tooltip"]').exists()).toBe(true)
    await trigger.trigger('mouseleave')
    await nextTick()
    expect(wrapper.find('[role="tooltip"]').exists()).toBe(false)
  })

  it.each([
    ['desktop', true],
    ['mobile', false],
  ])('dismisses pinned tooltips outside and transfers ARIA state between rows on %s', async (_viewport, desktop) => {
    setDesktopViewport(desktop)
    const addSpy = vi.spyOn(document, 'addEventListener')
    const removeSpy = vi.spyOn(document, 'removeEventListener')
    const wrapper = mount(UsageTable, {
      props: {
        data: [
          { ...baseImageRow, request_id: `req-pointer-1-${_viewport}`, refunded_cost: 0.4, refunded_at: '2026-07-12T00:00:00Z' },
          { ...baseImageRow, request_id: `req-pointer-2-${_viewport}`, refunded_cost: 0.2, refunded_at: '2026-07-12T00:00:00Z' },
        ],
        columns: [{ key: 'cost', label: 'Cost' }],
      },
      global: { stubs: { EmptyState: true, Icon: true, Teleport: true } },
    })
    await nextTick()
    const triggers = wrapper.findAll('[data-testid="cost-details-trigger"]')
    expect(triggers).toHaveLength(2)

    const touchDown = new Event('pointerdown', { bubbles: true })
    Object.defineProperty(touchDown, 'pointerType', { value: 'touch' })
    triggers[0].element.dispatchEvent(touchDown)
    await triggers[0].trigger('click')
    await nextTick()
    expect(triggers[0].attributes('aria-expanded')).toBe('true')
    expect(wrapper.findAll('[role="tooltip"]')).toHaveLength(1)

    triggers[1].element.dispatchEvent(new Event('pointerdown', { bubbles: true }))
    await triggers[1].trigger('click')
    await nextTick()
    expect(triggers[0].attributes('aria-expanded')).toBe('false')
    expect(triggers[1].attributes('aria-expanded')).toBe('true')
    expect(wrapper.findAll('[role="tooltip"]')).toHaveLength(1)
    expect(triggers[1].attributes('aria-controls')).toBe(wrapper.get('[role="tooltip"]').attributes('id'))

    document.body.dispatchEvent(new Event('pointerdown', { bubbles: true }))
    await nextTick()
    expect(wrapper.find('[role="tooltip"]').exists()).toBe(false)
    expect(triggers[1].attributes('aria-expanded')).toBe('false')
    const pointerRegistration = addSpy.mock.calls.find(([event]) => event === 'pointerdown')
    expect(removeSpy).toHaveBeenCalledWith('pointerdown', pointerRegistration?.[1])
  })

  it.each([
    ['desktop right placement', true, 1024, 768, { left: 100, right: 116, top: 100, bottom: 116 }, 300, 180, 'right'],
    ['desktop right-edge fallback', true, 1024, 768, { left: 900, right: 916, top: 100, bottom: 116 }, 300, 180, 'left'],
    ['desktop left-edge clamp', true, 300, 768, { left: 130, right: 146, top: 100, bottom: 116 }, 180, 180, 'left'],
    ['desktop bottom flip', true, 1024, 768, { left: 100, right: 116, top: 740, bottom: 756 }, 300, 180, 'right'],
    ['mobile narrow viewport', false, 280, 640, { left: 130, right: 146, top: 300, bottom: 316 }, 400, 180, 'left'],
  ])('keeps the cost tooltip in bounds for %s', async (_name, desktop, viewportWidth, viewportHeight, triggerRect, measuredWidth, measuredHeight, expectedSide) => {
    setDesktopViewport(desktop)
    Object.defineProperty(window, 'innerWidth', { configurable: true, value: viewportWidth })
    Object.defineProperty(window, 'innerHeight', { configurable: true, value: viewportHeight })
    vi.spyOn(HTMLElement.prototype, 'getBoundingClientRect').mockImplementation(function () {
      if (this.getAttribute('role') === 'tooltip') {
        return {
          x: 0, y: 0, top: 0, left: 0,
          right: measuredWidth, bottom: measuredHeight,
          width: measuredWidth, height: measuredHeight,
          toJSON: () => ({}),
        } as DOMRect
      }
      return {
        x: triggerRect.left,
        y: triggerRect.top,
        ...triggerRect,
        width: triggerRect.right - triggerRect.left,
        height: triggerRect.bottom - triggerRect.top,
        toJSON: () => ({}),
      } as DOMRect
    })

    const wrapper = mount(UsageTable, {
      props: {
        data: [{
          ...baseImageRow,
          request_id: `req-position-${_name}`,
          refunded_cost: 0.4,
          refunded_total_cost: 0.4,
          refunded_at: '2026-07-12T00:00:00Z',
        }],
        loading: false,
        columns: [{ key: 'cost', label: 'Cost' }],
      },
      global: { stubs: { EmptyState: true, Icon: true, Teleport: true } },
    })
    await nextTick()
    await wrapper.get('[data-testid="cost-details-trigger"]').trigger('click')
    await nextTick()
    await nextTick()

    const tooltip = wrapper.get('[role="tooltip"]')
    const left = Number.parseFloat(tooltip.element.style.left)
    const top = Number.parseFloat(tooltip.element.style.top)
    const effectiveWidth = Math.min(measuredWidth, viewportWidth - 16)
    const effectiveHeight = Math.min(measuredHeight, viewportHeight - 16)
    expect(tooltip.attributes('data-side')).toBe(expectedSide)
    expect(tooltip.get('.cost-tooltip').classes()).toEqual(expect.arrayContaining([
      'max-h-[calc(100vh-1rem)]',
      'overflow-y-auto',
    ]))
    expect(left).toBeGreaterThanOrEqual(8)
    expect(left + effectiveWidth).toBeLessThanOrEqual(viewportWidth - 8)
    expect(top).toBeGreaterThanOrEqual(8)
    expect(top + effectiveHeight).toBeLessThanOrEqual(viewportHeight - 8)
    if (_name.includes('bottom flip')) expect(top).toBeLessThan(triggerRect.top)
  })

  it('repositions while open and removes viewport listeners on unmount', async () => {
    setDesktopViewport(false)
    Object.defineProperty(window, 'innerWidth', { configurable: true, value: 400 })
    Object.defineProperty(window, 'innerHeight', { configurable: true, value: 640 })
    let triggerLeft = 40
    vi.spyOn(HTMLElement.prototype, 'getBoundingClientRect').mockImplementation(function () {
      if (this.getAttribute('role') === 'tooltip') {
        return { x: 0, y: 0, top: 0, left: 0, right: 180, bottom: 180, width: 180, height: 180, toJSON: () => ({}) } as DOMRect
      }
      return {
        x: triggerLeft, y: 100, left: triggerLeft, right: triggerLeft + 16,
        top: 100, bottom: 116, width: 16, height: 16, toJSON: () => ({}),
      } as DOMRect
    })
    const addSpy = vi.spyOn(window, 'addEventListener')
    const removeSpy = vi.spyOn(window, 'removeEventListener')
    const wrapper = mount(UsageTable, {
      props: {
        data: [{ ...baseImageRow, request_id: 'req-reposition', refunded_cost: 0.4, refunded_at: '2026-07-12T00:00:00Z' }],
        columns: [{ key: 'cost', label: 'Cost' }],
      },
      global: { stubs: { EmptyState: true, Icon: true, Teleport: true } },
    })
    await nextTick()
    await wrapper.get('[data-testid="cost-details-trigger"]').trigger('click')
    await nextTick()
    await nextTick()
    expect(wrapper.get('[role="tooltip"]').attributes('data-side')).toBe('right')

    triggerLeft = 360
    window.dispatchEvent(new Event('resize'))
    await nextTick()
    await nextTick()
    expect(wrapper.get('[role="tooltip"]').attributes('data-side')).toBe('left')

    const scrollRegistration = addSpy.mock.calls.find(([event]) => event === 'scroll')
    const resizeRegistration = addSpy.mock.calls.find(([event]) => event === 'resize')
    expect(scrollRegistration?.[2]).toBe(true)
    wrapper.unmount()
    expect(removeSpy).toHaveBeenCalledWith('scroll', scrollRegistration?.[1], true)
    expect(removeSpy).toHaveBeenCalledWith('resize', resizeRegistration?.[1])
  })

  it('displays historical image rows with missing billing_mode as image usage without a 2K fallback', async () => {
    const wrapper = mount(UsageTable, {
      props: {
        data: [
          {
            ...baseImageRow,
            request_id: 'req-admin-legacy-missing-image',
            billing_mode: null,
            image_size: null,
            image_input_size: null,
            image_output_size: null,
            image_size_source: null,
            image_size_breakdown: null,
          },
        ],
        loading: false,
        columns: [],
      },
      global: {
        stubs: {
          DataTable: DataTableStub,
          EmptyState: true,
          Icon: true,
          Teleport: true,
        },
      },
    })

    await wrapper.find('.group.relative').trigger('mouseenter')
    await nextTick()

    const text = wrapper.text()
    expect(text).toContain('Image')
    expect(text).toContain('Image count')
    expect(text).toContain('Per-image price')
    expect(text).toContain('not recorded')
    expect(text).not.toContain('(2K)')
  })

  it.each([
    { viewport: 'desktop', desktop: true, selector: 'table' },
    { viewport: 'mobile', desktop: false, selector: '.space-y-3' },
  ])('renders refunded video metadata and net zero through the real $viewport DataTable branch', async ({ desktop, selector }) => {
    setDesktopViewport(desktop)
    const row = {
      ...baseImageRow,
      request_id: 'req-refunded-video',
      model: 'sora-2',
      billing_mode: 'video',
      image_count: 0,
      video_count: 1,
      video_resolution: '720p',
      video_duration_seconds: 5,
      actual_cost: 0.4,
      total_cost: 0.4,
      refunded_cost: 0.4,
      refunded_total_cost: 0.4,
      net_actual_cost: 0,
      net_total_cost: 0,
      account_stats_cost: 0.3,
      refunded_account_cost: 0.3,
      net_account_cost: 0,
      refund_reason: 'upstream failed',
      refunded_at: '2026-07-12T00:00:00Z',
      input_tokens: 123,
      output_tokens: 456,
    }
    const wrapper = mount(UsageTable, {
      props: {
        data: [row],
        loading: false,
        columns: [
          { key: 'billing_mode', label: 'Billing mode' },
          { key: 'tokens', label: 'Media' },
          { key: 'cost', label: 'Cost' },
        ],
      },
      global: { stubs: { EmptyState: true, Icon: true, Teleport: true } },
    })
    await nextTick()

    expect(wrapper.find(selector).exists()).toBe(true)
    expect(wrapper.text()).toContain('5s · 720p · 1 video')
    expect(wrapper.text()).toContain('$0.000000')
    expect(wrapper.text()).toContain('Refunded')
    expect(wrapper.text()).not.toContain('123')
    expect(wrapper.text()).not.toContain('456')

    await wrapper.find('.group.relative').trigger('mouseenter')
    await nextTick()
    expect(wrapper.find('.cost-tooltip').classes()).toEqual(expect.arrayContaining([
      'max-w-[min(24rem,calc(100vw-2rem))]',
      'whitespace-normal',
    ]))
    expect(wrapper.find('[data-testid="customer-refund-details"]').classes()).toContain('flex-wrap')
    expect(wrapper.find('[data-testid="account-refund-details"]').classes()).toContain('flex-wrap')
    expect(wrapper.text()).toContain('Gross')
    expect(wrapper.text()).toContain('Refund')
    expect(wrapper.text()).toContain('Net')
    expect(wrapper.text()).toContain('$0.400000')
    expect(wrapper.text()).toContain('$0.300000')
  })

  it.each([
    ['EN', 'Customer gross amount before refund', 'Customer refund amount', 'Customer net amount after refund'],
    ['ZH', '退款前客户计费金额', '客户退款金额', '退款后客户净计费金额'],
  ])('keeps long %s refund labels in wrapping tooltip rows', async (_locale, gross, refund, net) => {
    const originalLabels = [messages['usage.grossCost'], messages['usage.refund'], messages['usage.netCost']]
    messages['usage.grossCost'] = gross
    messages['usage.refund'] = refund
    messages['usage.netCost'] = net

    const wrapper = mount(UsageTable, {
      props: {
        data: [{
          ...baseImageRow,
          request_id: `req-long-label-${_locale}`,
          billing_mode: 'video',
          image_count: 0,
          video_count: 1,
          video_resolution: '720p',
          video_duration_seconds: 5,
          refunded_cost: 0.4,
          refunded_total_cost: 0.4,
          refunded_at: '2026-07-12T00:00:00Z',
        }],
        loading: false,
        columns: [{ key: 'cost', label: 'Cost' }],
      },
      global: { stubs: { EmptyState: true, Icon: true, Teleport: true } },
    })
    await nextTick()
    await wrapper.find('.group.relative').trigger('mouseenter')
    await nextTick()

    const details = wrapper.find('[data-testid="customer-refund-details"]')
    expect(details.text()).toContain(gross)
    expect(details.text()).toContain(refund)
    expect(details.text()).toContain(net)
    expect(details.classes()).toContain('flex-wrap')
    for (const row of details.findAll(':scope > div')) {
      expect(row.classes()).toContain('flex-wrap')
      expect(row.find('span').classes()).toContain('min-w-0')
    }

    messages['usage.grossCost'] = originalLabels[0]
    messages['usage.refund'] = originalLabels[1]
    messages['usage.netCost'] = originalLabels[2]
  })

  it('renders non-refunded video metadata and tolerates null historical refund fields', () => {
    const wrapper = mount(UsageTable, {
      props: {
        data: [{
          ...baseImageRow,
          request_id: 'req-video',
          billing_mode: 'video',
          image_count: 0,
          video_count: 2,
          video_resolution: '1080p',
          video_duration_seconds: 10,
          refunded_cost: null,
          refunded_total_cost: null,
          refunded_account_cost: null,
          refunded_at: null,
        }],
        loading: false,
        columns: [],
      },
      global: { stubs: { DataTable: DataTableStub, EmptyState: true, Icon: true, Teleport: true } },
    })

    expect(wrapper.text()).toContain('10s · 1080p · 2 videos')
    expect(wrapper.text()).toContain('$0.400000')
    expect(wrapper.text()).not.toContain('Refunded')
  })

  it('shows per-second video pricing and customer and account settlement details', async () => {
    const wrapper = mount(UsageTable, {
      props: {
        data: [{
          ...baseImageRow,
          request_id: 'req-video-per-second',
          billing_mode: 'video',
          image_count: 0,
          video_count: 2,
          video_resolution: '1080p',
          video_duration_seconds: 5,
          total_cost: 1.2,
          actual_cost: 0.3,
          rate_multiplier: 1.5,
          refunded_cost: 0.1,
          net_actual_cost: 0.2,
          refunded_at: '2026-07-12T00:00:00Z',
          account_rate_multiplier: 0.75,
          account_stats_cost: 0.9,
          refunded_account_cost: 0.2,
          net_account_cost: 0.7,
        }],
        loading: false,
        columns: [],
      },
      global: { stubs: { DataTable: DataTableStub, EmptyState: true, Icon: true, Teleport: true } },
    })

    await wrapper.find('.group.relative').trigger('mouseenter')
    await nextTick()

    const text = wrapper.text()
    expect(text).toContain('Resolution')
    expect(text).toContain('1080p')
    expect(text).toContain('Duration')
    expect(text).toContain('5s')
    expect(text).toContain('Output count')
    expect(text).toContain('2 videos')
    expect(text).toContain('Per-second price')
    expect(text).toContain('$0.120000')
    expect(text).toContain('Calculation')
    expect(text).toContain('$0.120000 x 5 x 2 = $1.200000')
    expect(text).toContain('Rate')
    expect(text).toContain('1.50x')
    expect(text).toContain('Gross')
    expect(text).toContain('$0.300000')
    expect(text).toContain('Refund')
    expect(text).toContain('-$0.100000')
    expect(text).toContain('Net')
    expect(text).toContain('$0.200000')
    expect(text).toContain('Account rate')
    expect(text).toContain('0.75x')
    expect(text).toContain('Account billed')
    expect(text).toContain('$0.900000')
    expect(text).toContain('-$0.200000')
    expect(text).toContain('$0.700000')
  })

  it('marks rounded repeating video price calculations as approximate', async () => {
    const wrapper = mount(UsageTable, {
      props: {
        data: [{
          ...baseImageRow,
          request_id: 'req-video-repeating-unit-price',
          billing_mode: 'video',
          image_count: 0,
          video_count: 1,
          video_resolution: '1080p',
          video_duration_seconds: 3,
          total_cost: 1,
        }],
        loading: false,
        columns: [],
      },
      global: { stubs: { DataTable: DataTableStub, EmptyState: true, Icon: true, Teleport: true } },
    })

    await wrapper.find('.group.relative').trigger('mouseenter')
    await nextTick()

    const text = wrapper.text()
    expect(text).toContain('$0.333333 x 3 x 1 ≈ $1.000000')
    expect(text).not.toContain('$0.333333 x 3 x 1 = $1.000000')
  })

  it('hides account billing details in user mode for video usage', async () => {
    const wrapper = mount(UsageTable, {
      props: {
        data: [{
          ...baseImageRow,
          request_id: 'req-user-video-per-second',
          billing_mode: 'video',
          image_count: 0,
          video_count: 2,
          video_resolution: '1080p',
          video_duration_seconds: 5,
          total_cost: 1.2,
          actual_cost: 0.3,
          refunded_cost: 0.1,
          net_actual_cost: 0.2,
          refunded_at: '2026-07-12T00:00:00Z',
          account_rate_multiplier: 0.75,
          account_stats_cost: 0.9,
          refunded_account_cost: 0.2,
          net_account_cost: 0.7,
        }],
        loading: false,
        columns: [],
        showAccountBilling: false,
      },
      global: { stubs: { DataTable: DataTableStub, EmptyState: true, Icon: true, Teleport: true } },
    })

    await wrapper.find('.group.relative').trigger('mouseenter')
    await nextTick()

    const text = wrapper.text()
    expect(text).toContain('$0.120000 x 5 x 2 = $1.200000')
    expect(text).not.toContain('Account rate')
    expect(text).not.toContain('Account billed')
    expect(text).not.toContain('$0.900000')
    expect(text).not.toContain('-$0.200000')
    expect(text).not.toContain('$0.700000')
  })

  it.each([
    { name: 'zero duration', video_duration_seconds: 0, video_count: 2 },
    { name: 'missing duration', video_duration_seconds: null, video_count: 2 },
    { name: 'zero count', video_duration_seconds: 5, video_count: 0 },
    { name: 'missing count', video_duration_seconds: 5, video_count: null },
  ])('uses a zero video unit price without a calculation for $name', async ({ video_duration_seconds, video_count }) => {
    const wrapper = mount(UsageTable, {
      props: {
        data: [{
          ...baseImageRow,
          request_id: `req-video-invalid-${video_duration_seconds}-${video_count}`,
          billing_mode: 'video',
          image_count: 0,
          video_count,
          video_resolution: '1080p',
          video_duration_seconds,
          total_cost: 1.2,
        }],
        loading: false,
        columns: [],
      },
      global: { stubs: { DataTable: DataTableStub, EmptyState: true, Icon: true, Teleport: true } },
    })

    await wrapper.find('.group.relative').trigger('mouseenter')
    await nextTick()

    const text = wrapper.text()
    expect(text).toContain('Per-second price')
    expect(text).toContain('$0.000000')
    expect(text).toContain('Calculation-')
    expect(text).not.toContain('Infinity')
    expect(text).not.toContain('NaN')
    expect(text).not.toContain('$0.120000 x')
  })
})

describe('admin UsageTable request ID column', () => {
  beforeEach(() => {
    appStoreMocks.showSuccess.mockReset()
    appStoreMocks.showError.mockReset()
  })

  it('renders and copies the request ID', async () => {
    const writeText = vi.fn().mockResolvedValue(undefined)
    vi.stubGlobal('navigator', { clipboard: { writeText } })

    const wrapper = mount(UsageTable, {
      props: {
        data: [{ ...baseImageRow, request_id: 'req-admin-visible-id' }],
        loading: false,
        columns: [{ key: 'request_id', label: 'Request ID' }],
      },
      global: {
        stubs: {
          DataTable: DataTableStub,
          EmptyState: true,
          Icon: true,
          Teleport: true,
        },
      },
    })

    expect(wrapper.text()).toContain('req-admin-visible-id')
    await wrapper.get('button[title="Copy to clipboard"]').trigger('click')

    expect(writeText).toHaveBeenCalledWith('req-admin-visible-id')
    expect(appStoreMocks.showSuccess).toHaveBeenCalledWith('Request ID copied')
  })
})

describe('admin UsageTable IP geolocation batch toolbar', () => {
  const DataTableStubWithIp = {
    props: ['data'],
    template: `
      <div>
        <div v-for="row in data" :key="row.request_id">
          <slot name="cell-ip_address" :row="row" />
        </div>
      </div>
    `,
  }

  beforeEach(() => {
    ipGeoMocks.getEntry.mockReset()
    ipGeoMocks.fetchOne.mockReset()
    ipGeoMocks.fetchBatch.mockReset()
    ipGeoMocks.getEntry.mockReturnValue({ status: 'idle' })
  })

  it('does not render the batch toolbar when the ip_address column is not visible', () => {
    const wrapper = mount(UsageTable, {
      props: {
        data: [{ request_id: 'r1', ip_address: '8.8.8.8' }],
        loading: false,
        columns: [],
      },
      global: { stubs: { DataTable: DataTableStubWithIp, EmptyState: true, Teleport: true } },
    })
    expect(wrapper.text()).not.toContain('usage.ipGeo.batchFetch')
  })

  it('renders the batch toolbar with a pending count when the ip_address column is visible', () => {
    const wrapper = mount(UsageTable, {
      props: {
        data: [
          { request_id: 'r1', ip_address: '8.8.8.8' },
          { request_id: 'r2', ip_address: '8.8.8.8' },
          { request_id: 'r3', ip_address: '1.1.1.1' },
        ],
        loading: false,
        columns: [{ key: 'ip_address', label: 'IP' }],
      },
      global: { stubs: { DataTable: DataTableStubWithIp, EmptyState: true, Teleport: true } },
    })
    expect(wrapper.text()).toContain('usage.ipGeo.pending')
    const button = wrapper.find('button')
    expect(button.exists()).toBe(true)
    expect((button.element as HTMLButtonElement).disabled).toBe(false)
  })

  it('fetches deduplicated IPs from the current page when the batch button is clicked', async () => {
    ipGeoMocks.fetchBatch.mockResolvedValue(true)
    const wrapper = mount(UsageTable, {
      props: {
        data: [
          { request_id: 'r1', ip_address: '8.8.8.8' },
          { request_id: 'r2', ip_address: '8.8.8.8' },
          { request_id: 'r3', ip_address: '1.1.1.1' },
        ],
        loading: false,
        columns: [{ key: 'ip_address', label: 'IP' }],
      },
      global: { stubs: { DataTable: DataTableStubWithIp, EmptyState: true, Teleport: true } },
    })
    await wrapper.find('button').trigger('click')
    expect(ipGeoMocks.fetchBatch).toHaveBeenCalledWith(['8.8.8.8', '1.1.1.1'])
    expect(wrapper.emitted('ipGeoBatchFailed')).toBeUndefined()
  })

  it('emits ipGeoBatchFailed when the batch request reports a network-level failure', async () => {
    ipGeoMocks.fetchBatch.mockResolvedValue(false)
    const wrapper = mount(UsageTable, {
      props: {
        data: [{ request_id: 'r1', ip_address: '8.8.8.8' }],
        loading: false,
        columns: [{ key: 'ip_address', label: 'IP' }],
      },
      global: { stubs: { DataTable: DataTableStubWithIp, EmptyState: true, Teleport: true } },
    })
    await wrapper.find('button').trigger('click')
    expect(wrapper.emitted('ipGeoBatchFailed')).toHaveLength(1)
  })

  it('renders IpGeoCell content for ip_address cells', () => {
    ipGeoMocks.getEntry.mockReturnValue({ status: 'success', label: 'CN · Guangdong · Shenzhen', detail: {} })
    const wrapper = mount(UsageTable, {
      props: {
        data: [{ request_id: 'r1', ip_address: '121.35.47.43' }],
        loading: false,
        columns: [{ key: 'ip_address', label: 'IP' }],
      },
      global: { stubs: { DataTable: DataTableStubWithIp, EmptyState: true, Teleport: true } },
    })
    expect(wrapper.text()).toContain('121.35.47.43')
    expect(wrapper.text()).toContain('CN · Guangdong · Shenzhen')
  })
})

// A DataTable stub that also renders cell-user, so the deleted badge can be asserted.
const DataTableStubWithUser = {
  props: ['data'],
  template: `
    <div>
      <div v-for="row in data" :key="row.request_id">
        <slot name="cell-user" :row="row" />
        <slot name="cell-model" :row="row" :value="row.model" />
        <slot name="cell-billing_mode" :row="row" />
        <slot name="cell-tokens" :row="row" />
        <slot name="cell-cost" :row="row" />
      </div>
    </div>
  `,
}

describe('admin UsageTable deleted-user badge', () => {
  it('renders deleted badge for a soft-deleted user row', () => {
    const row = {
      request_id: 'req-deleted-user-1',
      model: 'claude-3',
      user_id: 2,
      user: { id: 2, email: 'd@test.com', deleted_at: '2026-05-28T00:00:00Z' },
      actual_cost: 0,
      total_cost: 0,
      input_cost: 0,
      output_cost: 0,
      rate_multiplier: 1,
      input_tokens: 1,
      output_tokens: 1,
    }

    const wrapper = mount(UsageTable, {
      props: {
        data: [row],
        loading: false,
        columns: [{ key: 'user', label: 'User' }],
      },
      global: {
        stubs: {
          DataTable: DataTableStubWithUser,
          EmptyState: true,
          Icon: true,
          Teleport: true,
        },
      },
    })

    expect(wrapper.text()).toContain('Deleted')
    expect(wrapper.text()).toContain('d@test.com')
  })

  it('does NOT render deleted badge for an active user row', () => {
    const row = {
      request_id: 'req-active-user-1',
      model: 'claude-3',
      user_id: 3,
      user: { id: 3, email: 'active@test.com', deleted_at: null },
      actual_cost: 0,
      total_cost: 0,
      input_cost: 0,
      output_cost: 0,
      rate_multiplier: 1,
      input_tokens: 1,
      output_tokens: 1,
    }

    const wrapper = mount(UsageTable, {
      props: {
        data: [row],
        loading: false,
        columns: [{ key: 'user', label: 'User' }],
      },
      global: {
        stubs: {
          DataTable: DataTableStubWithUser,
          EmptyState: true,
          Icon: true,
          Teleport: true,
        },
      },
    })

    expect(wrapper.text()).not.toContain('Deleted')
    expect(wrapper.text()).toContain('active@test.com')
  })
})
