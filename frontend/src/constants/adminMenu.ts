import type { AdminBuiltinMenuItemKey } from '@/types'

export interface AdminMenuVisibilityOption {
  key: AdminBuiltinMenuItemKey
  labelKey: string
}

export interface AdminMenuVisibilityItem {
  path: string
  hideInSimpleMode?: boolean
  menuKey?: AdminBuiltinMenuItemKey
  children?: AdminMenuVisibilityItem[]
}

export const ADMIN_MENU_VISIBILITY_OPTIONS: readonly AdminMenuVisibilityOption[] = [
  { key: 'ops', labelKey: 'nav.ops' },
  { key: 'users', labelKey: 'nav.users' },
  { key: 'groups', labelKey: 'nav.groups' },
  { key: 'channels', labelKey: 'nav.channels' },
  { key: 'subscriptions', labelKey: 'nav.subscriptions' },
  { key: 'accounts', labelKey: 'nav.accounts' },
  { key: 'announcements', labelKey: 'nav.announcements' },
  { key: 'proxies', labelKey: 'nav.proxies' },
  { key: 'redeem', labelKey: 'nav.redeemCodes' },
  { key: 'promoCodes', labelKey: 'nav.promoCodes' },
  { key: 'paymentDashboard', labelKey: 'nav.paymentDashboard' },
  { key: 'paymentOrders', labelKey: 'nav.orderManagement' },
  { key: 'paymentPlans', labelKey: 'nav.paymentPlans' },
  { key: 'usage', labelKey: 'nav.usage' },
] as const

export function filterHiddenAdminMenuItems<T extends AdminMenuVisibilityItem>(
  items: readonly T[],
  hiddenKeys: ReadonlySet<AdminBuiltinMenuItemKey>
): T[] {
  const filtered: T[] = []

  for (const item of items) {
    if (item.menuKey && hiddenKeys.has(item.menuKey)) {
      continue
    }

    if (!item.children?.length) {
      filtered.push(item)
      continue
    }

    const visibleChildren = filterHiddenAdminMenuItems(item.children, hiddenKeys)
    if (visibleChildren.length === 0) {
      continue
    }

    if (visibleChildren.length === 1) {
      filtered.push({
        ...visibleChildren[0],
        hideInSimpleMode: item.hideInSimpleMode || visibleChildren[0].hideInSimpleMode,
      } as T)
      continue
    }

    filtered.push({
      ...item,
      children: visibleChildren,
    } as T)
  }

  return filtered
}
