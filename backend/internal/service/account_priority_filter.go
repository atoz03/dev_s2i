package service

// filterAccountsByMinPriority 过滤出优先级最小的账号集合（数值越小优先级越高）
func filterAccountsByMinPriority(accounts []*Account) []*Account {
	if len(accounts) == 0 {
		return accounts
	}
	minPriority := accounts[0].Priority
	for i := 1; i < len(accounts); i++ {
		if accounts[i].Priority < minPriority {
			minPriority = accounts[i].Priority
		}
	}
	result := make([]*Account, 0, len(accounts))
	for i := range accounts {
		if accounts[i].Priority == minPriority {
			result = append(result, accounts[i])
		}
	}
	return result
}
