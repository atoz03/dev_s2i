package service

import "strings"

// 区分 Codex 工具上下文项和工具输出项，供 websocket/HTTP bridge 续链重放使用。
func isCodexToolCallContextItemType(typ string) bool {
	switch strings.TrimSpace(typ) {
	case "function_call",
		"tool_call",
		"local_shell_call",
		"tool_search_call",
		"custom_tool_call",
		"mcp_tool_call":
		return true
	default:
		return false
	}
}

func isCodexToolCallOutputItemType(typ string) bool {
	switch strings.TrimSpace(typ) {
	case "function_call_output",
		"mcp_tool_call_output",
		"custom_tool_call_output",
		"tool_search_output":
		return true
	default:
		return false
	}
}
