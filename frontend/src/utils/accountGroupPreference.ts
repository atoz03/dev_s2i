const ACCOUNT_SELECTED_GROUP_FILTER_STORAGE_KEY = 'account-selected-group-filter'
const ACCOUNT_DEFAULT_GROUP_FILTER_STORAGE_KEY = 'account-default-group-filter'

const normalizeGroupFilterValue = (value: string | number | null | undefined): string | null => {
  if (value === null || value === undefined) {
    return null
  }

  const normalized = String(value).trim()
  return normalized ? normalized : null
}

const getStorage = (): Storage | null => {
  if (typeof window === 'undefined') {
    return null
  }

  return window.localStorage
}

const readStoredValue = (key: string): string | null => {
  const storage = getStorage()
  if (!storage) {
    return null
  }

  return normalizeGroupFilterValue(storage.getItem(key))
}

const writeStoredValue = (key: string, value: string | number | null | undefined) => {
  const storage = getStorage()
  if (!storage) {
    return
  }

  const normalized = normalizeGroupFilterValue(value)
  if (!normalized) {
    storage.removeItem(key)
    return
  }

  storage.setItem(key, normalized)
}

export const getStoredSelectedAccountGroupFilter = (): string | null =>
  readStoredValue(ACCOUNT_SELECTED_GROUP_FILTER_STORAGE_KEY)

export const saveSelectedAccountGroupFilter = (value: string | number | null | undefined) => {
  writeStoredValue(ACCOUNT_SELECTED_GROUP_FILTER_STORAGE_KEY, value)
}

export const getStoredDefaultAccountGroupFilter = (): string | null =>
  readStoredValue(ACCOUNT_DEFAULT_GROUP_FILTER_STORAGE_KEY)

export const saveDefaultAccountGroupFilter = (value: string | number | null | undefined) => {
  writeStoredValue(ACCOUNT_DEFAULT_GROUP_FILTER_STORAGE_KEY, value)
}

export const clearDefaultAccountGroupFilter = () => {
  writeStoredValue(ACCOUNT_DEFAULT_GROUP_FILTER_STORAGE_KEY, null)
}

export const isDefaultAccountGroup = (groupID: string | number | null | undefined): boolean =>
  normalizeGroupFilterValue(groupID) !== null &&
  normalizeGroupFilterValue(groupID) === getStoredDefaultAccountGroupFilter()

export const getPreferredAccountGroupFilter = (): string | null =>
  getStoredDefaultAccountGroupFilter() ?? getStoredSelectedAccountGroupFilter()
