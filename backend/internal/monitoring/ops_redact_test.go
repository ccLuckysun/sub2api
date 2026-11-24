package monitoring

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/require"
)

func TestSanitizeOpsErrorMessageRedaction(t *testing.T) {
	tests := []struct {
		name           string
		input          string
		mustNotContain []string
		mustContain    []string
	}{
		{
			name:           "sk_and_bearer_plain_text",
			input:          "Upstream rejected key sk-proj-abc123456789xyz and Bearer tok_secret_987654",
			mustNotContain: []string{"sk-proj-abc123456789xyz", "tok_secret_987654"},
			mustContain:    []string{"sk-***", "Bearer ***"},
		},
		{
			name:           "query_string_secrets",
			input:          "POST https://generativelanguage.googleapis.com/v1beta/models?key=AIzaSyA12345678901234567890123456789012&api_key=myapikey123&token=tok123&access_token=acc123&refresh_token=ref123&client_secret=cs123&secret=sec123 failed",
			mustNotContain: []string{"AIzaSyA12345678901234567890123456789012", "myapikey123", "tok123", "acc123", "ref123", "cs123", "sec123"},
			mustContain:    []string{"?key=***", "&api_key=***", "&token=***", "&access_token=***", "&refresh_token=***", "&client_secret=***", "&secret=***"},
		},
		{
			name:           "header_secrets",
			input:          "headers x-api-key: sk-ant-secret123456 x-goog-api-key=goog-secret-val Authorization: Bearer header-jwt-token proxy-authorization: Basic dXNlcjpwYXNz",
			mustNotContain: []string{"sk-ant-secret123456", "goog-secret-val", "header-jwt-token", "Basic", "dXNlcjpwYXNz"},
			mustContain:    []string{"x-api-key: ***", "x-goog-api-key=***", "Authorization: ***", "proxy-authorization: ***"},
		},
		{
			name:           "json_wrapped_secrets_and_google_tokens",
			input:          `{"error":{"message":"invalid sk-1234567890abcdef and Bearer jsonSecret99 and GOCSPX-1234567890abcdefghijklmnop and AIzaSyB12345678901234567890123456789012 and AQ.Ab1234567890abcdefghijklmnopqrstuvwxyz1234567890 and https://x.test?api_key=qsecret","api_key":"rawval"}}`,
			mustNotContain: []string{"sk-1234567890abcdef", "jsonSecret99", "GOCSPX-1234567890abcdefghijklmnop", "AIzaSyB12345678901234567890123456789012", "AQ.Ab1234567890abcdefghijklmnopqrstuvwxyz1234567890", "qsecret", "rawval"},
			mustContain:    []string{"sk-***", "Bearer ***", "GOCSPX-***", "AIza***", "AQ.***", "api_key=***"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := sanitizeOpsErrorMessage(tc.input)
			for _, bad := range tc.mustNotContain {
				require.NotContains(t, got, bad)
			}
			for _, want := range tc.mustContain {
				require.Contains(t, got, want)
			}
		})
	}
	require.Empty(t, sanitizeOpsErrorMessage("   \t\n "))
}

func TestSanitizeOpsErrorMessageTruncationUTF8Safe(t *testing.T) {
	exact := strings.Repeat("a", maxOpsErrorMessageRunes)
	require.Equal(t, exact, sanitizeOpsErrorMessage(exact))

	overASCII := strings.Repeat("x", 700)
	gotASCII := sanitizeOpsErrorMessage(overASCII)
	require.LessOrEqual(t, utf8.RuneCountInString(gotASCII), maxOpsErrorMessageRunes)
	require.LessOrEqual(t, len(gotASCII), maxOpsErrorMessageRunes)
	require.True(t, strings.HasSuffix(gotASCII, "..."))

	overUTF8 := strings.Repeat("错", 600)
	gotUTF8 := sanitizeOpsErrorMessage(overUTF8)
	require.True(t, utf8.ValidString(gotUTF8))
	require.Equal(t, maxOpsErrorMessageRunes, utf8.RuneCountInString(gotUTF8))
	require.True(t, strings.HasSuffix(gotUTF8, "..."))
}
