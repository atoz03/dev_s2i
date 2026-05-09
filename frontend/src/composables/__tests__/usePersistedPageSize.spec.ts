import { afterEach, describe, expect, it } from 'vitest'

import { getPersistedPageSize } from '@/composables/usePersistedPageSize'

describe('usePersistedPageSize', () => {
  afterEach(() => {
    localStorage.clear()
    delete window.__APP_CONFIG__
  })

  it('prefers the stored page size when it is already present', () => {
    window.__APP_CONFIG__ = {
      table_default_page_size: 1000,
      table_page_size_options: [20, 50, 1000]
    } as any
    localStorage.setItem('table-page-size', '50')
    localStorage.setItem('table-page-size-source', 'user')

    expect(getPersistedPageSize()).toBe(50)
  })
})
