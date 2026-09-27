package service

import (
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// openAIFastTierModelSuffix 是 Fast 档在模型名里的写法。
//
// `gpt-6-astra-fast` 与「model=gpt-6-astra + service_tier=fast」是同一件事；
// Sol/Luna 也沿用相同规则：
// 前者是本 fork 对外售卖 Fast 档的 SKU 写法，后者是 OpenAI 原生写法
// （`normalizeOpenAIServiceTier` 已把 "fast" 归一化为 "priority"）。
// 网关在入口处把前者折叠成后者，fast policy、上游请求与计费便都沿用既有那条
// service_tier 路径，无需每处各自识别后缀。
const openAIFastTierModelSuffix = "-fast"

// splitOpenAIFastTierModel 拆出模型名末段的 `-fast`：
// gpt-6-astra-fast → ("gpt-6-astra", true)，gpt-6-fast → ("gpt-6", true)。
//
// 只在剥掉后缀后仍是**已知** OpenAI 型号时成立，`some-other-fast` 这类第三方
// 模型名不受影响。判定复用 normalizeKnownOpenAICodexModel，因此 `openai/` 前缀、
// 大小写与裸别名（gpt-6 → Astra）都自动生效。
//
// 不把 "fast" 加进 isKnownCodexModelSuffix：那个谓词的语义是「推理档位或日期」，
// 用于模型名归一化；Fast 是服务档位，两者不该混在一处。
func splitOpenAIFastTierModel(model string) (base string, isFast bool) {
	trimmed := strings.TrimSpace(model)
	if len(trimmed) <= len(openAIFastTierModelSuffix) {
		return trimmed, false
	}
	cut := len(trimmed) - len(openAIFastTierModelSuffix)
	if !strings.EqualFold(trimmed[cut:], openAIFastTierModelSuffix) {
		return trimmed, false
	}
	stripped := trimmed[:cut]
	if normalizeKnownOpenAICodexModel(stripped) == "" {
		return trimmed, false
	}
	return stripped, true
}

// applyOpenAIFastTierModelSuffix 把请求体里模型名末段的 `-fast` 折叠成
// service_tier: "priority"，并把 model 改写成去掉后缀的型号。
//
// body 里已显式给出 service_tier 时**不覆盖**——客户端的显式声明优先，
// 例如 `model=gpt-6-astra-fast` + `service_tier=flex` 仍按 flex 处理。
func applyOpenAIFastTierModelSuffix(body []byte) []byte {
	if len(body) == 0 {
		return body
	}
	base, isFast := splitOpenAIFastTierModel(gjson.GetBytes(body, "model").String())
	if !isFast {
		return body
	}
	updated, err := sjson.SetBytes(body, "model", base)
	if err != nil {
		return body
	}
	if strings.TrimSpace(gjson.GetBytes(updated, "service_tier").String()) != "" {
		return updated
	}
	withTier, err := sjson.SetBytes(updated, "service_tier", "priority")
	if err != nil {
		return updated
	}
	return withTier
}
