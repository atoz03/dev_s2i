package openai

import "testing"

func TestPairCodexClientIdentity(t *testing.T) {
	tests := []struct {
		name           string
		ua             string
		wantOriginator string
		wantUA         string
		wantOK         bool
	}{
		{
			name:           "codex_cli_rs 首段直接配对",
			ua:             "codex_cli_rs/0.146.0 (Ubuntu 22.4.0; x86_64) xterm-256color",
			wantOriginator: "codex_cli_rs",
			wantUA:         "codex_cli_rs/0.146.0 (Ubuntu 22.4.0; x86_64) xterm-256color",
			wantOK:         true,
		},
		{
			// 真实流量占比最高的一支：必须原样保留，绝不能被改写成 codex_cli_rs
			// 而与客户端自报的 originator=codex-tui 错配（上游 404）。
			name:           "codex-tui 首段直接配对",
			ua:             "codex-tui/0.146.0 (Ubuntu 22.4.0; x86_64) xterm-256color",
			wantOriginator: "codex-tui",
			wantUA:         "codex-tui/0.146.0 (Ubuntu 22.4.0; x86_64) xterm-256color",
			wantOK:         true,
		},
		{
			name:           "大小写归一为规范小写",
			ua:             "CODEX_CLI_RS/0.146.0 (Ubuntu 22.4.0; x86_64)",
			wantOriginator: "codex_cli_rs",
			wantUA:         "codex_cli_rs/0.146.0 (Ubuntu 22.4.0; x86_64)",
			wantOK:         true,
		},
		{
			// CODEX_INTERNAL_ORIGINATOR_OVERRIDE 只改前缀不改尾部括号组，
			// 从尾部恢复真实身份并重写首段，保留版本/OS/终端指纹。
			name:           "尾部括号组恢复被 override 的身份",
			ua:             "cccc/0.142.0 (Ubuntu 22.4.0; x86_64) xterm-256color (codex-tui; 0.142.0)",
			wantOriginator: "codex-tui",
			wantUA:         "codex-tui/0.142.0 (Ubuntu 22.4.0; x86_64) xterm-256color (codex-tui; 0.142.0)",
			wantOK:         true,
		},
		{
			name:           "Codex 家族保留原大小写",
			ua:             "Codex Desktop/1.2.3 (macOS 15.0; arm64)",
			wantOriginator: "Codex Desktop",
			wantUA:         "Codex Desktop/1.2.3 (macOS 15.0; arm64)",
			wantOK:         true,
		},
		{name: "第三方 UA 推导失败", ua: "cccc/1.0.0", wantOK: false},
		{name: "浏览器 UA 推导失败", ua: "Mozilla/5.0 (X11; Linux x86_64)", wantOK: false},
		{name: "无斜杠", ua: "codex_cli_rs", wantOK: false},
		{name: "空 UA", ua: "", wantOK: false},
		{name: "尾部含斜杠被拒绝", ua: "cccc/1.0.0 (codex-tui/x; 1.0)", wantOK: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			originator, pairedUA, ok := PairCodexClientIdentity(tt.ua)
			if ok != tt.wantOK {
				t.Fatalf("PairCodexClientIdentity(%q) ok = %v, want %v", tt.ua, ok, tt.wantOK)
			}
			if !tt.wantOK {
				return
			}
			if originator != tt.wantOriginator {
				t.Fatalf("originator = %q, want %q", originator, tt.wantOriginator)
			}
			if pairedUA != tt.wantUA {
				t.Fatalf("pairedUA = %q, want %q", pairedUA, tt.wantUA)
			}
			// 配套性不变量：originator 必须等于最终 UA 的首段，否则上游 404。
			if got := pairedUA[:len(originator)]; got != originator {
				t.Fatalf("UA 首段 %q 与 originator %q 不配套", got, originator)
			}
		})
	}
}

// TestCodexOfficialClientMatchersRejectForgery 守住 `codex ` 家族前缀不得退化成裸 "codex"：
// 退化后任何含 codex 子串的伪造标识都会被当成官方客户端，绕过 codex_cli_only 访问限制。
func TestCodexOfficialClientMatchersRejectForgery(t *testing.T) {
	forged := []string{"evil-codex_thing", "my-codex-proxy", "notcodex_cli_rs_fake"}
	for _, v := range forged {
		if IsCodexOfficialClientOriginator(v) {
			t.Fatalf("IsCodexOfficialClientOriginator(%q) = true, want false", v)
		}
	}
	if IsCodexOfficialClientRequestStrict("Mozilla/5.0 codex_app/0.1.0") {
		t.Fatal("strict 版不应把「浏览器前缀 + 中段 codex token」判为官方客户端")
	}
	// 宽松版保留 Contains 子串兜底（历史透传路径依赖），此处固化该差异。
	if !IsCodexOfficialClientRequest("Mozilla/5.0 codex_app/0.1.0") {
		t.Fatal("宽松版应保留 Contains 子串兜底")
	}
}

// TestCodexTUIRecognizedAsOfficial codex-tui 是真实流量占比最高的官方客户端，
// 必须被 UA 与 originator 两条识别路径接受。
func TestCodexTUIRecognizedAsOfficial(t *testing.T) {
	ua := "codex-tui/0.146.0 (Ubuntu 22.4.0; x86_64) xterm-256color"
	if !IsCodexOfficialClientRequest(ua) || !IsCodexOfficialClientRequestStrict(ua) {
		t.Fatalf("codex-tui UA 未被识别为官方客户端: %q", ua)
	}
	for _, o := range []string{"codex-tui", "codex_vscode_copilot"} {
		if !IsCodexOfficialClientOriginator(o) {
			t.Fatalf("IsCodexOfficialClientOriginator(%q) = false, want true", o)
		}
	}
}
