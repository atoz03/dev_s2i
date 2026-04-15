package handler

import (
	"context"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ip"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

func (h *OpenAIGatewayHandler) openAISubscriptionAndBillingEligibility(
	ctx context.Context,
	c *gin.Context,
	apiKey *service.APIKey,
) (*service.UserSubscription, error) {
	subscription, _ := middleware2.GetSubscriptionFromContext(c)
	if err := h.billingCacheService.CheckBillingEligibility(ctx, apiKey.User, apiKey, apiKey.Group, subscription); err != nil {
		return subscription, err
	}
	return subscription, nil
}

func (h *GatewayHandler) openAISubscriptionAndBillingEligibility(
	ctx context.Context,
	c *gin.Context,
	apiKey *service.APIKey,
) (*service.UserSubscription, error) {
	subscription, _ := middleware2.GetSubscriptionFromContext(c)
	if err := h.billingCacheService.CheckBillingEligibility(ctx, apiKey.User, apiKey, apiKey.Group, subscription); err != nil {
		return subscription, err
	}
	return subscription, nil
}

func (h *OpenAIGatewayHandler) submitOpenAIUsageRecordTask(
	c *gin.Context,
	component string,
	userID int64,
	apiKey *service.APIKey,
	account *service.Account,
	subscription *service.UserSubscription,
	result *service.OpenAIForwardResult,
	requestPayloadHash string,
	channelUsageFields service.ChannelUsageFields,
) {
	userAgent := c.GetHeader("User-Agent")
	clientIP := ip.GetClientIP(c)
	inboundEndpoint := GetInboundEndpoint(c)
	upstreamEndpoint := GetUpstreamEndpoint(c, account.Platform)

	h.submitUsageRecordTask(func(ctx context.Context) {
		if err := h.gatewayService.RecordUsage(ctx, &service.OpenAIRecordUsageInput{
			Result:             result,
			APIKey:             apiKey,
			User:               apiKey.User,
			Account:            account,
			Subscription:       subscription,
			InboundEndpoint:    inboundEndpoint,
			UpstreamEndpoint:   upstreamEndpoint,
			UserAgent:          userAgent,
			IPAddress:          clientIP,
			RequestPayloadHash: requestPayloadHash,
			APIKeyService:      h.apiKeyService,
			ChannelUsageFields: channelUsageFields,
		}); err != nil {
			logger.L().With(
				zap.String("component", component),
				zap.Int64("user_id", userID),
				zap.Int64("api_key_id", apiKey.ID),
				zap.Any("group_id", apiKey.GroupID),
				zap.Int64("account_id", account.ID),
			).Error("openai_usage_record_failed", zap.Error(err))
		}
	})
}

func (h *GatewayHandler) submitGatewayUsageRecordTask(
	c *gin.Context,
	reqLog *zap.Logger,
	apiKey *service.APIKey,
	account *service.Account,
	subscription *service.UserSubscription,
	result *service.ForwardResult,
	requestPayloadHash string,
	channelUsageFields service.ChannelUsageFields,
) {
	userAgent := c.GetHeader("User-Agent")
	clientIP := ip.GetClientIP(c)
	inboundEndpoint := GetInboundEndpoint(c)
	upstreamEndpoint := GetUpstreamEndpoint(c, account.Platform)

	h.submitUsageRecordTask(func(ctx context.Context) {
		if err := h.gatewayService.RecordUsage(ctx, &service.RecordUsageInput{
			Result:             result,
			APIKey:             apiKey,
			User:               apiKey.User,
			Account:            account,
			Subscription:       subscription,
			InboundEndpoint:    inboundEndpoint,
			UpstreamEndpoint:   upstreamEndpoint,
			UserAgent:          userAgent,
			IPAddress:          clientIP,
			RequestPayloadHash: requestPayloadHash,
			APIKeyService:      h.apiKeyService,
			ChannelUsageFields: channelUsageFields,
		}); err != nil {
			reqLog.Error("gateway.record_usage_failed",
				zap.Int64("account_id", account.ID),
				zap.Error(err),
			)
		}
	})
}
