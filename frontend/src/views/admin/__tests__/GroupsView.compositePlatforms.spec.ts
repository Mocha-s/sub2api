import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { describe, expect, it } from 'vitest'
import { CONCRETE_PLATFORM_OPTIONS } from '@/constants/platforms'

describe('GroupsView Composite route options', () => {
  it('offers Kimi, Zhipu GLM, and DeepSeek as route targets', () => {
    expect(CONCRETE_PLATFORM_OPTIONS.map((option) => option.value)).toEqual(
      expect.arrayContaining(['kimi', 'zhipu', 'deepseek', 'minimax', 'opencode_go'])
    )
  })

  it('exposes dynamic target references and rate billing controls in the route editor', () => {
    const source = readFileSync(resolve(process.cwd(), 'src/views/admin/GroupsView.vue'), 'utf8')
    expect(source).toContain('listCompositeTargets')
    expect(source).toContain('createCompositeTarget')
    expect(source).toContain('updateCompositeTarget')
    expect(source).toContain('deleteCompositeTarget')
    expect(source).toContain('dynamic_rate_billing_enabled')
    expect(source).toContain('compositeRouteForm.target_group_id')
    expect(source).toContain('g.platform !== "composite"')
  })

  it('places dynamic targets in the Composite group edit form, not the route dialog', () => {
    const source = readFileSync(resolve(process.cwd(), 'src/views/admin/GroupsView.vue'), 'utf8')
    const editFormStart = source.indexOf('id="edit-group-form"')
    const routeDialogStart = source.indexOf('<!-- Composite Routes Modal -->')
    const editForm = source.slice(editFormStart, routeDialogStart)
    const routeDialog = source.slice(routeDialogStart)

    expect(editForm).toContain('editCompositeTargets')
    expect(editForm).toContain('editForm.dynamic_rate_billing_enabled')
    expect(routeDialog).not.toContain('admin.groups.compositeTargets.title')
  })
})
