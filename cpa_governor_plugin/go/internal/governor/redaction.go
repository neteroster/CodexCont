package governor

import (
	"regexp"
	"strings"
)

var (
	bearerRe = regexp.MustCompile(`(?i)\bBearer\s+[A-Za-z0-9._~+/=-]+`)
	secretRe = regexp.MustCompile(`(?i)\b(api[_-]?key|access[_-]?token|refresh[_-]?token|authorization|cookie|secret)\s*[:=]\s*['"]?[^'"\s,;]+`)
	spaceRe  = regexp.MustCompile(`\s+`)
)

func RedactString(value string) string {
	value = bearerRe.ReplaceAllString(value, "Bearer [REDACTED]")
	value = secretRe.ReplaceAllStringFunc(value, func(match string) string {
		parts := strings.FieldsFunc(match, func(r rune) bool { return r == ':' || r == '=' })
		if len(parts) == 0 {
			return "[REDACTED]"
		}
		return strings.TrimSpace(parts[0]) + "=[REDACTED]"
	})
	return value
}

func Brief(value string, limit int) string {
	value = spaceRe.ReplaceAllString(strings.TrimSpace(RedactString(value)), " ")
	if limit <= 0 || len(value) <= limit {
		return value
	}
	if limit <= 3 {
		return value[:limit]
	}
	return strings.TrimSpace(value[:limit-3]) + "..."
}
