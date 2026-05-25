package ref

import (
	"regexp"
	"testing"
)

func TestLineToken(t *testing.T) {
	t.Parallel()

	got := LineToken("  return err  ", 3)
	if len(got) != 3 {
		t.Fatalf("len(LineToken) = %d, want 3", len(got))
	}

	if got != LineToken("return err", 3) {
		t.Fatalf("LineToken should ignore surrounding whitespace")
	}

	if got != LineToken("return err\r", 3) {
		t.Fatalf("LineToken should ignore trailing carriage returns")
	}

	if got == LineToken("return fmt.Errorf(\"wrap: %w\", err)", 3) {
		t.Fatalf("different lines should not share the same test token")
	}

	re := regexp.MustCompile(`^[a-z2-7]{3}$`)
	if !re.MatchString(got) {
		t.Fatalf("LineToken(%q) = %q, want lowercase base32 token", "return err", got)
	}
}

func TestLineTokenLengthValidation(t *testing.T) {
	t.Parallel()

	if got := LineToken("return err", 4); len(got) != 4 {
		t.Fatalf("len(LineToken(..., 4)) = %d, want 4", len(got))
	}
}
