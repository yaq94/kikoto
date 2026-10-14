package translation_test

import (
	"strings"
	"testing"

	"github.com/yexca/kikoto/backend/internal/translation"
)

func TestProtectedNamesCannotRewritePlaceholders(t *testing.T) {
	original := "Example Voice and NAME"
	protected, err := translation.Protect(original, []string{"Example Voice", "NAME"})
	if err != nil {
		t.Fatal(err)
	}
	restored, err := protected.Restore(protected.Text)
	if err != nil || restored != original {
		t.Fatalf("protected name round trip failed: %q %v", restored, err)
	}
	if strings.Contains(protected.Text, "Example Voice") {
		t.Fatal("name escaped protection")
	}
	if _, err := protected.Restore("model dropped the names"); err == nil {
		t.Fatal("lost placeholders accepted")
	}
}
