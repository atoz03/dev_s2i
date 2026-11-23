package setup

import (
	"os"
	"strings"
	"testing"
)

func TestPrepareAdminCredentials(t *testing.T) {
	t.Parallel()

	var generated AdminConfig
	emailGenerated, passwordGenerated, err := prepareAdminCredentials(&generated)
	if err != nil || !emailGenerated || !passwordGenerated {
		t.Fatalf("prepareAdminCredentials() = (%t, %t, %v)", emailGenerated, passwordGenerated, err)
	}
	if !strings.HasPrefix(generated.Email, "admin-") || !validateEmail(generated.Email) {
		t.Fatal("生成的管理员邮箱不符合登录规则")
	}
	if err := validatePassword(generated.Password); err != nil {
		t.Fatalf("生成的管理员密码不符合要求: %v", err)
	}

	explicit := AdminConfig{Email: "operator@example.com", Password: strings.Repeat("a", maxPasswordBytes)}
	original := explicit
	emailGenerated, passwordGenerated, err = prepareAdminCredentials(&explicit)
	if err != nil || emailGenerated || passwordGenerated || explicit != original {
		t.Fatal("显式提供的有效管理员凭据应保留")
	}

	invalid := AdminConfig{Email: original.Email, Password: strings.Repeat("a", maxPasswordBytes+1)}
	if _, _, err := prepareAdminCredentials(&invalid); err == nil {
		t.Fatal("超过 bcrypt 输入上限的密码应被拒绝")
	}
}

func TestDecideAdminBootstrap(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		totalUsers int64
		adminUsers int64
		should     bool
		reason     string
	}{
		{
			name:       "empty database should create admin",
			totalUsers: 0,
			adminUsers: 0,
			should:     true,
			reason:     adminBootstrapReasonEmptyDatabase,
		},
		{
			name:       "admin exists should skip",
			totalUsers: 10,
			adminUsers: 1,
			should:     false,
			reason:     adminBootstrapReasonAdminExists,
		},
		{
			name:       "users exist without admin should skip",
			totalUsers: 5,
			adminUsers: 0,
			should:     false,
			reason:     adminBootstrapReasonUsersExistWithoutAdmin,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := decideAdminBootstrap(tc.totalUsers, tc.adminUsers)
			if got.shouldCreate != tc.should {
				t.Fatalf("shouldCreate=%v, want %v", got.shouldCreate, tc.should)
			}
			if got.reason != tc.reason {
				t.Fatalf("reason=%q, want %q", got.reason, tc.reason)
			}
		})
	}
}

func TestSetupDefaultAdminConcurrency(t *testing.T) {
	t.Run("simple mode admin uses higher concurrency", func(t *testing.T) {
		t.Setenv("RUN_MODE", "simple")
		if got := setupDefaultAdminConcurrency(); got != simpleModeAdminConcurrency {
			t.Fatalf("setupDefaultAdminConcurrency()=%d, want %d", got, simpleModeAdminConcurrency)
		}
	})

	t.Run("standard mode keeps existing default", func(t *testing.T) {
		t.Setenv("RUN_MODE", "standard")
		if got := setupDefaultAdminConcurrency(); got != defaultUserConcurrency {
			t.Fatalf("setupDefaultAdminConcurrency()=%d, want %d", got, defaultUserConcurrency)
		}
	})
}

func TestWriteConfigFileKeepsDefaultUserConcurrency(t *testing.T) {
	t.Setenv("RUN_MODE", "simple")
	t.Setenv("DATA_DIR", t.TempDir())

	if err := writeConfigFile(&SetupConfig{}); err != nil {
		t.Fatalf("writeConfigFile() error = %v", err)
	}

	data, err := os.ReadFile(GetConfigFilePath())
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}

	if !strings.Contains(string(data), "user_concurrency: 5") {
		t.Fatalf("config missing default user concurrency, got:\n%s", string(data))
	}
}
