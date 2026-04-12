package repository

import (
	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/group"
	"github.com/Wei-Shaw/sub2api/ent/user"
)

func userSelectFieldsForService() []string {
	return []string{
		user.FieldID,
		user.FieldEmail,
		user.FieldUsername,
		user.FieldNotes,
		user.FieldPasswordHash,
		user.FieldRole,
		user.FieldBalance,
		user.FieldConcurrency,
		user.FieldStatus,
		user.FieldTotpSecretEncrypted,
		user.FieldTotpEnabled,
		user.FieldTotpEnabledAt,
		user.FieldCreatedAt,
		user.FieldUpdatedAt,
	}
}

func groupSelectFieldsForService() []string {
	return []string{
		group.FieldID,
		group.FieldName,
		group.FieldDescription,
		group.FieldPlatform,
		group.FieldRateMultiplier,
		group.FieldIsExclusive,
		group.FieldStatus,
		group.FieldSubscriptionType,
		group.FieldDailyLimitUsd,
		group.FieldWeeklyLimitUsd,
		group.FieldMonthlyLimitUsd,
		group.FieldImagePrice1k,
		group.FieldImagePrice2k,
		group.FieldImagePrice4k,
		group.FieldDefaultValidityDays,
		group.FieldClaudeCodeOnly,
		group.FieldFallbackGroupID,
		group.FieldFallbackGroupIDOnInvalidRequest,
		group.FieldModelRouting,
		group.FieldModelRoutingEnabled,
		group.FieldMcpXMLInject,
		group.FieldSupportedModelScopes,
		group.FieldSortOrder,
		group.FieldAllowMessagesDispatch,
		group.FieldRequireOauthOnly,
		group.FieldRequirePrivacySet,
		group.FieldDefaultMappedModel,
		group.FieldMessagesDispatchModelConfig,
		group.FieldCreatedAt,
		group.FieldUpdatedAt,
	}
}

func selectUserForService(q *dbent.UserQuery) {
	q.Select(userSelectFieldsForService()...)
}

func selectGroupForService(q *dbent.GroupQuery) {
	q.Select(groupSelectFieldsForService()...)
}
