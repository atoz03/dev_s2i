package repository

import (
	"bufio"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const migrationChecksumSnapshotPath = "../../migrations/checksums.lock"

// 这份快照用于保护“已经进入主干的 migration 文件”不被静默改写。
// 规则是：
// 1. 当前 migrations 目录下的每个 .sql 文件都必须出现在快照里；
// 2. 如果某个 migration 文件内容变了，要么恢复原文件，要么显式补 checksum 兼容规则。
func TestMigrationChecksumSnapshotIsComplete(t *testing.T) {
	snapshot := loadMigrationChecksumSnapshot(t)
	current := loadCurrentMigrationChecksums(t)
	require.Equal(t, sortedMapKeys(snapshot), sortedMapKeys(current))
}

func TestMigrationChecksumSnapshotStaysImmutableOrExplicitlyCompatible(t *testing.T) {
	snapshot := loadMigrationChecksumSnapshot(t)
	current := loadCurrentMigrationChecksums(t)

	for name, snapshotChecksum := range snapshot {
		currentChecksum, ok := current[name]
		require.Truef(t, ok, "migration %s missing from current migrations directory", name)
		if currentChecksum == snapshotChecksum {
			continue
		}
		require.Truef(
			t,
			isMigrationChecksumCompatible(name, snapshotChecksum, currentChecksum),
			"migration %s checksum changed after being snapshotted (snapshot=%s current=%s); revert the file or add an explicit compatibility rule",
			name,
			snapshotChecksum,
			currentChecksum,
		)
	}
}

func loadMigrationChecksumSnapshot(t *testing.T) map[string]string {
	t.Helper()

	file, err := os.Open(filepath.Clean(migrationChecksumSnapshotPath))
	require.NoError(t, err)
	defer func() { _ = file.Close() }()

	out := make(map[string]string)
	scanner := bufio.NewScanner(file)
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		fields := strings.Fields(line)
		require.Lenf(t, fields, 2, "invalid checksum snapshot line %d", lineNo)
		name, checksum := fields[0], fields[1]
		require.NotEmptyf(t, name, "empty migration name on line %d", lineNo)
		require.Lenf(t, checksum, 64, "invalid checksum length for %s on line %d", name, lineNo)

		if previous, exists := out[name]; exists {
			t.Fatalf("duplicate snapshot entry for %s: %s and %s", name, previous, checksum)
		}
		out[name] = checksum
	}
	require.NoError(t, scanner.Err())
	return out
}

func loadCurrentMigrationChecksums(t *testing.T) map[string]string {
	t.Helper()

	files, err := filepath.Glob(filepath.Join("..", "..", "migrations", "*.sql"))
	require.NoError(t, err)
	require.NotEmpty(t, files)

	out := make(map[string]string, len(files))
	for _, path := range files {
		content, err := os.ReadFile(filepath.Clean(path))
		require.NoError(t, err)
		name := filepath.Base(path)
		out[name] = migrationChecksum(string(content))
	}
	return out
}

func sortedMapKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
