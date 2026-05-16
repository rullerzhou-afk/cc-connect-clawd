package clawdbridge

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestRedactTextMasksTelegramBotToken(t *testing.T) {
	input := "token=123456789:ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghi"
	out := RedactText(input)
	if strings.Contains(out, "ABCDEFGHIJKLMNOPQRSTUVWXYZ") || strings.Contains(out, "123456789:") {
		t.Fatalf("token was not redacted: %q", out)
	}
	if !strings.Contains(out, redactedMarker) {
		t.Fatalf("redacted marker missing: %q", out)
	}
}

func TestRedactTextMasksBearerToken(t *testing.T) {
	out := RedactText("Authorization: Bearer abcdefghijklmnopqrstuvwxyz123456")
	if strings.Contains(out, "abcdefghijklmnopqrstuvwxyz") {
		t.Fatalf("bearer token was not redacted: %q", out)
	}
	if !strings.Contains(out, "Bearer "+redactedMarker) {
		t.Fatalf("bearer marker missing: %q", out)
	}
}

func TestRedactTextMasksAPIKeyAndEnvLikeSecrets(t *testing.T) {
	input := "OPENAI_API_KEY=sk-abcdefghijklmnopqrstuvwxyz token: my-secret password=\"pw123\" safe=value"
	out := RedactText(input)
	for _, secret := range []string{"sk-abcdefghijklmnopqrstuvwxyz", "my-secret", "pw123"} {
		if strings.Contains(out, secret) {
			t.Fatalf("secret %q was not redacted in %q", secret, out)
		}
	}
	if !strings.Contains(out, "safe=value") {
		t.Fatalf("non-secret field changed: %q", out)
	}
}

func TestRedactTextMasksCommonTokenFormats(t *testing.T) {
	input := "aws=AKIA1234567890ABCDEF slack=xoxb-123456789012-abcdef jwt=eyJaaaaaaaaaa.bbbbbbbbbbbb.cccccccccccc X-API-Key: header-secret"
	out := RedactText(input)
	for _, secret := range []string{"AKIA1234567890ABCDEF", "xoxb-123456789012-abcdef", "eyJaaaaaaaaaa.bbbbbbbbbbbb.cccccccccccc", "header-secret"} {
		if strings.Contains(out, secret) {
			t.Fatalf("secret %q was not redacted in %q", secret, out)
		}
	}
}

func TestRedactTextWithKnownSecrets(t *testing.T) {
	out := RedactTextWithSecrets("chat telegram:123456789 belongs to user 987654321", []string{"telegram:123456789", "987654321"})
	for _, secret := range []string{"telegram:123456789", "987654321"} {
		if strings.Contains(out, secret) {
			t.Fatalf("known secret %q was not redacted in %q", secret, out)
		}
	}
}

func TestRedactAndTruncateCapsRunes(t *testing.T) {
	input := strings.Repeat("a", 20)
	out := RedactAndTruncate(input, 10)
	if utf8.RuneCountInString(out) != 10 {
		t.Fatalf("rune count = %d, want 10", utf8.RuneCountInString(out))
	}
	if !strings.HasSuffix(out, "...") {
		t.Fatalf("truncated string = %q, want suffix ...", out)
	}
}

func TestSanitizeApprovalTextCapsTitleAndDetail(t *testing.T) {
	title, detail := SanitizeApprovalText(strings.Repeat("t", MaxTitleRunes+20), strings.Repeat("d", MaxDetailRunes+20))
	if utf8.RuneCountInString(title) > MaxTitleRunes {
		t.Fatalf("title length = %d, want <= %d", utf8.RuneCountInString(title), MaxTitleRunes)
	}
	if utf8.RuneCountInString(detail) > MaxDetailRunes {
		t.Fatalf("detail length = %d, want <= %d", utf8.RuneCountInString(detail), MaxDetailRunes)
	}
}
