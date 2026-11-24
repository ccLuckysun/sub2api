package monitoring

import (
	"encoding/json"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/Wei-Shaw/sub2api/internal/util/logredact"
)

const maxOpsErrorMessageRunes = 512

var (
	reOpsBearerToken  = regexp.MustCompile(`(?i)\b(bearer\s+)([^\s"',;}\]]+)`)
	reOpsSKKey        = regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{8,}`)
	reOpsQuerySecret  = regexp.MustCompile(`(?i)([?&](?:key|api_key|apikey|token|access_token|refresh_token|client_secret|id_token|session_token|secret|password)=)[^&"'\s]+`)
	reOpsHeaderSecret      = regexp.MustCompile(`(?i)\b(x-api-key|x-goog-api-key|authorization|proxy-authorization|api[_-]?key|api[_-]?secret|access[_-]?token|refresh[_-]?token|id[_-]?token|session[_-]?token|client[_-]?secret|private[_-]?key|password|passwd|token|secret)\b(\s*[:=]\s*)("(?:\\.|[^"\\])*"|'(?:\\.|[^'\\])*'|(?:(?:bearer|basic)\s+)?[^\s,;}"'&]+)`)
	reOpsJSONSecret        = regexp.MustCompile(`(?i)("(?:x-api-key|x-goog-api-key|authorization|proxy-authorization|api[_-]?key|api[_-]?secret|token|access_token|refresh_token|id_token|session_token|client_secret|private_key|password|passwd|secret)"\s*:\s*")((?:\\.|[^"\\])*)(")`)
	reOpsEscapedJSONSecret = regexp.MustCompile(`(?i)(\\"(?:x-api-key|x-goog-api-key|authorization|proxy-authorization|api[_-]?key|api[_-]?secret|token|access_token|refresh_token|id_token|session_token|client_secret|private_key|password|passwd|secret)\\"\s*:\s*\\")([^"\\]*)(\\")`)
	reOpsGOCSPX       = regexp.MustCompile(`GOCSPX-[0-9A-Za-z_-]{24,}`)
	reOpsAIza         = regexp.MustCompile(`AIza[0-9A-Za-z_-]{35}`)
	reOpsGeminiAuth   = regexp.MustCompile(`AQ\.Ab[0-9A-Za-z_-]{40,}`)
)

var benignOpsDiagnosticWords = map[string]bool{
	"expired":      true,
	"invalid":      true,
	"missing":      true,
	"required":     true,
	"revoked":      true,
	"disabled":     true,
	"denied":       true,
	"failed":       true,
	"empty":        true,
	"none":         true,
	"null":         true,
	"unknown":      true,
	"unauthorized": true,
}

func redactOpsHeaderSecretMatches(msg string) string {
	indices := reOpsHeaderSecret.FindAllStringSubmatchIndex(msg, -1)
	if len(indices) == 0 {
		return msg
	}
	var b strings.Builder
	b.Grow(len(msg))
	last := 0
	for _, m := range indices {
		b.WriteString(msg[last:m[0]])
		key := msg[m[2]:m[3]]
		sep := msg[m[4]:m[5]]
		val := msg[m[6]:m[7]]
		lowerKey := strings.ToLower(key)
		cleanedVal := strings.TrimRight(strings.ToLower(strings.TrimSpace(val)), ".,;:!?)]}")
		if (lowerKey == "token" || lowerKey == "secret") && strings.Contains(sep, ":") && benignOpsDiagnosticWords[cleanedVal] {
			b.WriteString(msg[m[0]:m[1]])
		} else {
			b.WriteString(key)
			b.WriteString(sep)
			b.WriteString("***")
		}
		last = m[1]
	}
	b.WriteString(msg[last:])
	return b.String()
}

func redactOpsSecrets(msg string) string {
	if json.Valid([]byte(msg)) {
		msg = logredact.RedactJSON(
			[]byte(msg),
			"api_key",
			"apikey",
			"api-key",
			"api_secret",
			"api-secret",
			"x-api-key",
			"x-goog-api-key",
			"authorization",
			"proxy-authorization",
			"token",
			"access_token",
			"refresh_token",
			"id_token",
			"session_token",
			"client_secret",
			"private_key",
			"password",
			"passwd",
			"secret",
		)
	}
	msg = reOpsGOCSPX.ReplaceAllString(msg, "GOCSPX-***")
	msg = reOpsAIza.ReplaceAllString(msg, "AIza***")
	msg = reOpsGeminiAuth.ReplaceAllString(msg, "AQ.***")
	msg = reOpsJSONSecret.ReplaceAllString(msg, `${1}***${3}`)
	msg = reOpsEscapedJSONSecret.ReplaceAllString(msg, `${1}***${3}`)
	msg = reOpsQuerySecret.ReplaceAllString(msg, `${1}***`)
	msg = redactOpsHeaderSecretMatches(msg)
	msg = reOpsBearerToken.ReplaceAllString(msg, `${1}***`)
	msg = reOpsSKKey.ReplaceAllString(msg, "sk-***")
	return msg
}

// sanitizeOpsErrorMessage redacts sensitive credentials (both plain text and
// JSON-wrapped payloads) and truncates the result to at most 512 runes on a
// valid UTF-8 boundary.
func sanitizeOpsErrorMessage(raw string) string {
	msg := strings.TrimSpace(raw)
	if msg == "" {
		return ""
	}
	msg = redactOpsSecrets(msg)
	return truncateOpsErrorMessage(msg, maxOpsErrorMessageRunes)
}

const maxOpsErrorCodeRunes = 128

// sanitizeOpsErrorCode strips JSON container payloads, redacts accidental secret
// tokens, collapses whitespace, and truncates upstream_error_code to <= 128 runes.
func sanitizeOpsErrorCode(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" || strings.HasPrefix(s, "{") || strings.HasPrefix(s, "[") {
		return ""
	}
	s = strings.NewReplacer("\r", " ", "\n", " ", "\t", " ").Replace(s)
	s = redactOpsSecrets(strings.TrimSpace(s))
	return truncateOpsErrorMessage(s, maxOpsErrorCodeRunes)
}

func truncateOpsErrorMessage(s string, maxRunes int) string {
	s = strings.TrimSpace(s)
	if s == "" || maxRunes <= 0 {
		return ""
	}
	if utf8.RuneCountInString(s) <= maxRunes {
		return s
	}
	var b strings.Builder
	if maxRunes <= 3 {
		count := 0
		for _, r := range s {
			if count >= maxRunes {
				break
			}
			b.WriteRune(r)
			count++
		}
		return b.String()
	}
	limit := maxRunes - 3
	growCap := limit*4 + 3
	if len(s) < growCap {
		growCap = len(s)
	}
	b.Grow(growCap)
	count := 0
	for _, r := range s {
		if count >= limit {
			break
		}
		b.WriteRune(r)
		count++
	}
	b.WriteString("...")
	return b.String()
}
