package clawdbridge

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

const (
	MaxTitleRunes  = 120
	MaxDetailRunes = 200
	redactedMarker = "[REDACTED]"
)

var redactionPatterns = []struct {
	re   *regexp.Regexp
	repl string
}{
	{
		re:   regexp.MustCompile(`\b\d{6,12}:[A-Za-z0-9_-]{30,}\b`),
		repl: redactedMarker,
	},
	{
		re:   regexp.MustCompile(`(?i)\bBearer\s+[A-Za-z0-9._~+/=-]{8,}`),
		repl: "Bearer " + redactedMarker,
	},
	{
		re:   regexp.MustCompile(`(?i)\bAuthorization\s*:\s*(Basic|Token)\s+[A-Za-z0-9._~+/=-]{8,}`),
		repl: "Authorization: " + redactedMarker,
	},
	{
		re:   regexp.MustCompile(`\b(AKIA|ASIA)[0-9A-Z]{16}\b`),
		repl: redactedMarker,
	},
	{
		re:   regexp.MustCompile(`\bxox[abprs]-[A-Za-z0-9-]{10,}\b`),
		repl: redactedMarker,
	},
	{
		re:   regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\b`),
		repl: redactedMarker,
	},
	{
		re:   regexp.MustCompile(`\b(sk-[A-Za-z0-9_-]{16,})\b`),
		repl: redactedMarker,
	},
	{
		re:   regexp.MustCompile(`(?i)\b(api[_-]?key|x-api-key|token|access[_-]?token|refresh[_-]?token|id[_-]?token|bot[_-]?token|bearer[_-]?token|secret|client[_-]?secret|password|passwd|pwd|credential|cookie|session|session[_-]?token|private[_-]?key)\s*[:=]\s*("[^"\r\n]*"|'[^'\r\n]*'|[^\s,;]+)`),
		repl: `$1=` + redactedMarker,
	},
}

func RedactText(s string) string {
	out := s
	for _, p := range redactionPatterns {
		out = p.re.ReplaceAllString(out, p.repl)
	}
	return out
}

func RedactTextWithSecrets(s string, secrets []string) string {
	out := RedactText(s)
	for _, secret := range secrets {
		secret = strings.TrimSpace(secret)
		if secret == "" {
			continue
		}
		out = strings.ReplaceAll(out, secret, redactedMarker)
	}
	return out
}

func RedactAndTruncate(s string, maxRunes int) string {
	return RedactAndTruncateWithSecrets(s, maxRunes, nil)
}

func RedactAndTruncateWithSecrets(s string, maxRunes int, secrets []string) string {
	s = strings.TrimSpace(RedactTextWithSecrets(s, secrets))
	if maxRunes <= 0 {
		return ""
	}
	if utf8.RuneCountInString(s) <= maxRunes {
		return s
	}
	runes := []rune(s)
	if maxRunes <= 3 {
		return strings.Repeat(".", maxRunes)
	}
	return string(runes[:maxRunes-3]) + "..."
}

func SanitizeApprovalText(title, detail string) (string, string) {
	return SanitizeApprovalTextWithSecrets(title, detail, nil)
}

func SanitizeApprovalTextWithSecrets(title, detail string, secrets []string) (string, string) {
	return RedactAndTruncateWithSecrets(title, MaxTitleRunes, secrets),
		RedactAndTruncateWithSecrets(detail, MaxDetailRunes, secrets)
}
