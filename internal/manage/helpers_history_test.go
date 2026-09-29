package manage

import (
	"testing"

	"github.com/backpack/backpack/internal/manage/tunnelspec"
)

// withTempConfigDir points the configuration history somewhere a test may
// write; see tunnelspec.HistoryRoot.
func withTempConfigDir(t *testing.T) {
	t.Helper()
	old := tunnelspec.HistoryRoot
	tunnelspec.HistoryRoot = t.TempDir()
	t.Cleanup(func() { tunnelspec.HistoryRoot = old })
}
