import { afterEach, beforeEach, describe, expect, it } from 'vitest'

import {
  clearDefaultAccountGroupFilter,
  getPreferredAccountGroupFilter,
  getStoredDefaultAccountGroupFilter,
  getStoredSelectedAccountGroupFilter,
  isDefaultAccountGroup,
  saveDefaultAccountGroupFilter,
  saveSelectedAccountGroupFilter
} from '../accountGroupPreference'

describe('accountGroupPreference', () => {
  beforeEach(() => {
    localStorage.clear()
  })

  afterEach(() => {
    localStorage.clear()
  })

  it('默认分组优先于最近一次选择的分组', () => {
    saveSelectedAccountGroupFilter('12')
    saveDefaultAccountGroupFilter('34')

    expect(getStoredSelectedAccountGroupFilter()).toBe('12')
    expect(getStoredDefaultAccountGroupFilter()).toBe('34')
    expect(getPreferredAccountGroupFilter()).toBe('34')
  })

  it('清除默认分组后回退到最近一次选择的分组', () => {
    saveSelectedAccountGroupFilter('12')
    saveDefaultAccountGroupFilter('34')

    clearDefaultAccountGroupFilter()

    expect(getStoredDefaultAccountGroupFilter()).toBeNull()
    expect(getPreferredAccountGroupFilter()).toBe('12')
  })

  it('仅保存一个默认分组并支持判定当前默认组', () => {
    saveDefaultAccountGroupFilter('8')
    expect(isDefaultAccountGroup(8)).toBe(true)
    expect(isDefaultAccountGroup('9')).toBe(false)

    saveDefaultAccountGroupFilter('10')

    expect(getStoredDefaultAccountGroupFilter()).toBe('10')
    expect(isDefaultAccountGroup(8)).toBe(false)
    expect(isDefaultAccountGroup('10')).toBe(true)
  })
})
