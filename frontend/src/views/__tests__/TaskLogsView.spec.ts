import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import type { VueWrapper } from '@vue/test-utils'
import TaskLogsView from '../TaskLogsView.vue'
import type { TaskLog } from '@/api/taskLogs'

const { get, showError, showSuccess, saveAs } = vi.hoisted(() => ({ get: vi.fn(), showError: vi.fn(), showSuccess: vi.fn(), saveAs: vi.fn() }))
vi.mock('@/api/client', () => ({ apiClient: { get } }))
vi.mock('@/stores/app', () => ({ useAppStore: () => ({ showError, showSuccess }) }))
vi.mock('@/composables/useClipboard', () => ({ useClipboard: () => ({ copyToClipboard: vi.fn() }) }))
vi.mock('file-saver', () => ({ saveAs }))
vi.mock('vue-i18n', async importOriginal => ({
  ...await importOriginal<typeof import('vue-i18n')>(),
  useI18n: () => ({ t: (key: string) => key })
}))

const task = (overrides: Partial<TaskLog> = {}): TaskLog => ({
  type: 'video', task_id: 'task_1', platform: 'openai', model: 'sora-2',
  status: 'succeeded', raw_status: 'completed', progress: 100,
  submitted_at: '2026-11-13T00:00:00Z', finished_at: '2026-11-13T00:01:23Z',
  cost_usd: 0.8, prompt: 'draw a cat', has_result: true, ...overrides
})
const page = (rows: TaskLog[]) => ({ data: { items: rows, total: rows.length, page: 1, page_size: 20, pages: 1 } })
let wrapper: VueWrapper | undefined
function render(admin = false) {
  wrapper = mount(TaskLogsView, {
    props: { admin },
    global: { stubs: {
      AppLayout: { template: '<main><slot /></main>' },
      TablePageLayout: { template: '<div><slot name="filters" /><slot name="table" /><slot name="pagination" /></div>' },
      BaseDialog: { props: ['show', 'title'], template: '<section v-if="show" role="dialog"><h2>{{ title }}</h2><slot /><slot name="footer" /></section>' },
      DateRangePicker: true, Select: true, Pagination: true, VideoSettlementModal: true
    } }
  })
  return wrapper
}
function button(key: string) {
  const found = wrapper!.findAll('button').find(node => node.text() === key)
  expect(found, `button ${key}`).toBeDefined()
  return found!
}
function deferred<T>() {
  let resolve!: (value: T) => void
  const promise = new Promise<T>(res => { resolve = res })
  return { promise, resolve }
}

beforeEach(() => {
  vi.resetAllMocks()
  vi.useFakeTimers()
  vi.spyOn(document, 'hidden', 'get').mockReturnValue(false)
  Object.defineProperty(URL, 'createObjectURL', { configurable: true, value: vi.fn(() => 'blob:preview') })
  Object.defineProperty(URL, 'revokeObjectURL', { configurable: true, value: vi.fn() })
})
afterEach(() => {
  wrapper?.unmount()
  wrapper = undefined
  vi.useRealTimers()
  vi.restoreAllMocks()
})

describe('task log results', () => {
  it('shows media loading and a preview title instead of task metadata while fetching', async () => {
    const pending = deferred<{ data: Blob }>()
    get.mockResolvedValueOnce(page([task()])).mockReturnValueOnce(pending.promise)
    render(true)
    await flushPromises()
    await button('taskLogs.preview').trigger('click')
    expect(wrapper!.get('[role=dialog] h2').text()).toBe('taskLogs.videoPreview')
    expect(wrapper!.get('[role=dialog]').text()).not.toContain('taskLogs.rawStatus')
    expect(wrapper!.get('[role=status]').text()).toContain('taskLogs.loadingMedia')
    pending.resolve({ data: new Blob(['video'], { type: 'video/mp4' }) })
    await flushPromises()
  })

  it('shows in-flight download and completion feedback', async () => {
    const pending = deferred<{ data: Blob }>()
    get.mockResolvedValueOnce(page([task()])).mockReturnValueOnce(pending.promise)
    render()
    await flushPromises()
    await button('taskLogs.download').trigger('click')
    expect(wrapper!.text()).toContain('taskLogs.downloading')
    pending.resolve({ data: new Blob(['video'], { type: 'video/mp4' }) })
    await flushPromises()
    expect(saveAs).toHaveBeenCalled()
    expect(showSuccess).toHaveBeenCalledWith('taskLogs.downloadStarted')
  })

  it('shows preview failures inside the media dialog', async () => {
    get.mockResolvedValueOnce(page([task()])).mockRejectedValueOnce(new Error('Upstream content expired'))
    render()
    await flushPromises()
    await button('taskLogs.preview').trigger('click')
    await flushPromises()
    expect(wrapper!.get('[role=alert]').text()).toContain('Upstream content expired')
    expect(wrapper!.find('video').exists()).toBe(false)
  })

  it('does not show a 100 percent completion bar for unsuccessful terminal tasks', async () => {
    get.mockResolvedValueOnce(page(['failed', 'cancelled', 'expired'].map((status, i) => task({
      task_id: `task_${i}`, status: status as TaskLog['status'], progress: 100, has_result: false
    }))))
    render()
    await flushPromises()
    expect(wrapper!.findAll('[role=progressbar]')).toHaveLength(0)
    expect(wrapper!.text()).not.toContain('100%')
  })

  it.each([false, true])('previews and downloads completed video without a public URL (admin=%s)', async admin => {
    const video = new Blob(['video'], { type: 'video/mp4' })
    get.mockResolvedValueOnce(page([task()])).mockResolvedValue({ data: video })
    render(admin)
    await flushPromises()
    expect(wrapper!.text()).toContain('taskLogs.download')
    await button('taskLogs.preview').trigger('click')
    await flushPromises()
    expect(wrapper!.get('video').attributes('src')).toBe('blob:preview')
    expect(get).toHaveBeenCalledWith(`${admin ? '/admin' : ''}/task-logs/video/task_1/content`, expect.objectContaining({ responseType: 'blob', timeout: 0 }))
    await button('taskLogs.download').trigger('click')
    await flushPromises()
    expect(saveAs).toHaveBeenCalledWith(video, 'task_1.mp4')
    await button('common.close').trigger('click')
    expect(URL.revokeObjectURL).toHaveBeenCalledWith('blob:preview')
  })

  it('clears the previous video when viewing a different task', async () => {
    get.mockResolvedValueOnce(page([task(), task({ task_id: 'task_2' })])).mockResolvedValue({ data: new Blob(['video']) })
    render()
    await flushPromises()
    await button('taskLogs.preview').trigger('click')
    await flushPromises()
    const views = wrapper!.findAll('button').filter(node => node.text() === 'taskLogs.view')
    await views[1].trigger('click')
    expect(wrapper!.find('video').exists()).toBe(false)
    expect(wrapper!.get('[role=dialog]').text()).toContain('task_2')
    expect(URL.revokeObjectURL).toHaveBeenCalledWith('blob:preview')
  })

  it('does not reopen a closed preview when content finishes loading', async () => {
    const pending = deferred<{ data: Blob }>()
    get.mockResolvedValueOnce(page([task()])).mockReturnValueOnce(pending.promise)
    render()
    await flushPromises()
    await button('taskLogs.preview').trigger('click')
    expect(wrapper!.find('[role=dialog]').exists()).toBe(true)
    await button('common.close').trigger('click')
    pending.resolve({ data: new Blob(['video']) })
    await flushPromises()
    expect(wrapper!.find('[role=dialog]').exists()).toBe(false)
    expect(URL.createObjectURL).not.toHaveBeenCalled()
  })

  it('discards a pending preview when changing filters', async () => {
    const pending = deferred<{ data: Blob }>()
    get.mockResolvedValueOnce(page([task()])).mockReturnValueOnce(pending.promise).mockResolvedValueOnce(page([]))
    render()
    await flushPromises()
    await button('taskLogs.preview').trigger('click')
    wrapper!.findAllComponents({ name: 'Select' })[0].vm.$emit('change', 'image')
    await flushPromises()
    pending.resolve({ data: new Blob(['old video']) })
    await flushPromises()
    expect(wrapper!.find('[role=dialog]').exists()).toBe(false)
    expect(URL.createObjectURL).not.toHaveBeenCalled()
  })

  it.each([false, true])('previews each image and downloads the selected image (admin=%s)', async admin => {
    const image = new Blob(['image'], { type: 'image/webp' })
    get.mockResolvedValueOnce(page([task({ type: 'image', task_id: 'imgtask_1', request_mode: 'async', item_count: 2, success_count: 2 })])).mockResolvedValue({ data: image })
    render(admin)
    await flushPromises()
    expect(wrapper!.text()).toContain('taskLogs.types.image')
    await button('taskLogs.preview').trigger('click')
    await flushPromises()
    expect(wrapper!.get('[role=dialog] img').attributes('src')).toBe('blob:preview')
    await button('common.next').trigger('click')
    await flushPromises()
    expect(get).toHaveBeenLastCalledWith(`${admin ? '/admin' : ''}/task-logs/image/imgtask_1/content`, expect.objectContaining({ params: { index: 1 }, responseType: 'blob' }))
    const downloads = wrapper!.findAll('[role=dialog] button').filter(node => node.text() === 'taskLogs.download')
    expect(downloads).toHaveLength(1)
    await downloads[0].trigger('click')
    await flushPromises()
    expect(saveAs).toHaveBeenCalledWith(image, 'imgtask_1-2.webp')
  })

  it('keeps the batch image zip download', async () => {
    const zip = new Blob(['zip'], { type: 'application/zip' })
    get.mockResolvedValueOnce(page([task({ type: 'batch_image', task_id: 'batch_1' })])).mockResolvedValue({ data: zip })
    render(true)
    await flushPromises()
    await button('taskLogs.download').trigger('click')
    await flushPromises()
    expect(get).toHaveBeenLastCalledWith('/admin/task-logs/batch-image/batch_1/download', expect.objectContaining({ responseType: 'blob' }))
    expect(saveAs).toHaveBeenCalledWith(zip, 'batch_1.zip')
  })
})

describe('task log refresh', () => {
  it('renders upstream progress every ten seconds and stops at completion', async () => {
    get.mockResolvedValueOnce(page([task({ status: 'running', progress: 12, has_result: false })]))
      .mockResolvedValueOnce(page([task({ status: 'running', progress: 68, has_result: false })]))
      .mockResolvedValueOnce(page([task()]))
    render()
    await flushPromises()
    expect(wrapper!.get('[role=progressbar]').attributes('aria-valuenow')).toBe('12')
    await vi.advanceTimersByTimeAsync(10_000)
    expect(wrapper!.get('[role=progressbar]').attributes('aria-valuenow')).toBe('68')
    await vi.advanceTimersByTimeAsync(10_000)
    expect(wrapper!.get('[role=progressbar]').attributes('aria-valuenow')).toBe('100')
    await vi.advanceTimersByTimeAsync(30_000)
    expect(get).toHaveBeenCalledTimes(3)
  })

  it('does not refresh hidden pages', async () => {
    get.mockResolvedValue(page([task({ status: 'running', has_result: false })]))
    render()
    await flushPromises()
    vi.spyOn(document, 'hidden', 'get').mockReturnValue(true)
    await vi.advanceTimersByTimeAsync(30_000)
    expect(get).toHaveBeenCalledTimes(1)
  })

  it('does not start overlapping automatic refresh requests', async () => {
    const pending = deferred<ReturnType<typeof page>>()
    get.mockResolvedValueOnce(page([task({ status: 'running', has_result: false })])).mockReturnValue(pending.promise)
    render()
    await flushPromises()
    await vi.advanceTimersByTimeAsync(30_000)
    expect(get).toHaveBeenCalledTimes(2)
    pending.resolve(page([task()]))
    await flushPromises()
  })

  it('discards an outdated list response after a manual refresh', async () => {
    const pending = deferred<ReturnType<typeof page>>()
    get.mockResolvedValueOnce(page([task({ status: 'running', progress: 12 })])).mockReturnValueOnce(pending.promise).mockResolvedValueOnce(page([task()]))
    render()
    await flushPromises()
    await vi.advanceTimersByTimeAsync(10_000)
    await button('common.refresh').trigger('click')
    await flushPromises()
    pending.resolve(page([task({ status: 'running', progress: 68 })]))
    await flushPromises()
    expect(wrapper!.get('[role=progressbar]').attributes('aria-valuenow')).toBe('100')
  })
})
