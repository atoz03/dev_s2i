import { describe, expect, it } from 'vitest'

import { filterHiddenAdminMenuItems } from '@/constants/adminMenu'
import type { AdminBuiltinMenuItemKey } from '@/types'

interface TestNavItem {
  path: string
  label: string
  icon: string
  hideInSimpleMode?: boolean
  menuKey?: AdminBuiltinMenuItemKey
  children?: TestNavItem[]
}

describe('filterHiddenAdminMenuItems', () => {
  it('filters hidden top-level menu items', () => {
    const items: TestNavItem[] = [
      { path: '/admin/dashboard', label: 'Dashboard', icon: 'dashboard' },
      { path: '/admin/proxies', label: 'Proxies', icon: 'proxies', menuKey: 'proxies' },
    ]

    expect(filterHiddenAdminMenuItems(items, new Set(['proxies']))).toEqual([
      { path: '/admin/dashboard', label: 'Dashboard', icon: 'dashboard' },
    ])
  })

  it('flattens grouped menus when only one child remains visible', () => {
    const items: TestNavItem[] = [
      {
        path: '/admin/orders',
        label: 'Orders',
        icon: 'orders',
        hideInSimpleMode: true,
        children: [
          { path: '/admin/orders/dashboard', label: 'Dashboard', icon: 'dashboard', menuKey: 'paymentDashboard' },
          { path: '/admin/orders', label: 'Orders', icon: 'orders', menuKey: 'paymentOrders' },
        ],
      },
    ]

    expect(filterHiddenAdminMenuItems(items, new Set(['paymentDashboard']))).toEqual([
      {
        path: '/admin/orders',
        label: 'Orders',
        icon: 'orders',
        hideInSimpleMode: true,
        menuKey: 'paymentOrders',
      },
    ])
  })

  it('removes grouped menus when all children are hidden', () => {
    const items: TestNavItem[] = [
      {
        path: '/admin/orders',
        label: 'Orders',
        icon: 'orders',
        children: [
          { path: '/admin/orders/dashboard', label: 'Dashboard', icon: 'dashboard', menuKey: 'paymentDashboard' },
        ],
      },
    ]

    expect(filterHiddenAdminMenuItems(items, new Set(['paymentDashboard']))).toEqual([])
  })
})
