package monitoring

import "strings"

// FailureEvidence contains only observations already available to the caller.
// For HTTP 5xx, Stage=connect with ProxyUsed identifies a proxy response;
// Stage=headers/body identifies an origin response. Other stages with a proxy
// do not establish the response's origin and therefore classify as unknown.
// body_read/response identify client cancellation, panic identifies recovery.
type FailureEvidence struct {
	StatusCode     int
	Stage          string
	TransportKind  string
	ProxyUsed      bool
	UpstreamCode   string
	ClientCanceled bool
	DBError        bool
	RateLimited    bool
}

// AccountLabels is an immutable startup snapshot. Lookups use the original
// name, so colliding names and names introduced after startup stay unknown.
type AccountLabels struct{ names map[string]string }

// NewAccountLabels admits the first ten distinct, nonempty normalized names.
// Collisions retain the first original name; unknown is reserved.
func NewAccountLabels(names []string) *AccountLabels {
	a := &AccountLabels{names: make(map[string]string)}
	seen := make(map[string]bool)
	for _, name := range names {
		label := normalizeAccount(name)
		if label == "" || label == "unknown" || seen[label] || len(a.names) >= 10 {
			continue
		}
		seen[label] = true
		a.names[name] = label
	}
	return a
}

func normalizeAccount(name string) string {
	var b strings.Builder
	for _, c := range strings.ToLower(strings.TrimSpace(name)) {
		if c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_' || c == '-' {
			b.WriteRune(c)
			if b.Len() == 32 {
				break
			}
		}
	}
	return b.String()
}

// Label returns a bounded account label; a nil snapshot accepts no accounts.
func (a *AccountLabels) Label(name string) string {
	if a != nil {
		if label, ok := a.names[name]; ok {
			return label
		}
	}
	return "unknown"
}

// EndpointClass maps route paths/templates to the five endpoint classes.
func EndpointClass(path string) string {
	switch path {
	case "/v1/messages", "/antigravity/v1/messages",
		"/v1/responses", "/responses", "/backend-api/codex/responses",
		"/v1/responses/*subpath", "/responses/*subpath", "/backend-api/codex/responses/*subpath",
		"/v1/responses/compact", "/responses/compact", "/backend-api/codex/responses/compact":
		return "responses"
	case "/v1/chat/completions", "/chat/completions":
		return "chat"
	case "/v1/embeddings", "/embeddings":
		return "embeddings"
	}
	if strings.HasPrefix(path, "/v1/images/") {
		return "images"
	}
	return "other"
}

// ModelClass classifies the original request model in contract priority order.
func ModelClass(model string) string {
	model = strings.ToLower(model)
	for _, class := range []string{"codex", "claude", "gemini", "gpt"} {
		if strings.Contains(model, class) {
			return class
		}
	}
	return "other"
}

// NormalizeResult bounds results; unknown input is a gateway error with an
// unknown cause (unknown is a reason, never a result label).
func NormalizeResult(result string) string {
	switch result {
	case "success", "upstream_error", "timeout", "client_abort", "gateway_error", "rejected":
		return result
	default:
		return "gateway_error"
	}
}

// ClassifyFailure returns the first matching reason in API section 4.4 order.
// Success always has an empty reason, regardless of stale failure evidence.
func ClassifyFailure(result string, ev FailureEvidence) string {
	if result == "success" {
		return ""
	}
	switch {
	case ev.StatusCode == 429 || ev.StatusCode == 529:
		return "upstream_rate_limit"
	case ev.StatusCode == 401 || ev.StatusCode == 403:
		return "upstream_auth"
	case (ev.StatusCode == 400 || ev.StatusCode == 404) && ev.UpstreamCode != "":
		return "upstream_model_or_request"
	}
	// An HTTP 200 stream can carry a structured failure. Recognize only explicit
	// semantic codes; do not invent an HTTP status or classify arbitrary codes.
	if ev.StatusCode == 200 && ev.Stage == "body" {
		switch ev.UpstreamCode {
		case "rate_limit_error", "rate_limit_exceeded", "overloaded_error":
			return "upstream_rate_limit"
		case "authentication_error", "permission_error", "invalid_api_key":
			return "upstream_auth"
		case "invalid_request_error", "invalid_request", "model_not_found", "invalid_model", "unsupported_parameter":
			return "upstream_model_or_request"
		}
	}
	serverError := ev.StatusCode == 500 || ev.StatusCode == 502 || ev.StatusCode == 503 || ev.StatusCode == 504
	proxyResponse := ev.ProxyUsed && ev.Stage == "connect"
	if serverError && !proxyResponse && (!ev.ProxyUsed || ev.Stage == "headers" || ev.Stage == "body") {
		return "upstream_server_error"
	}
	if ev.Stage == "connect" && ev.TransportKind == "timeout" {
		return "upstream_connect_timeout"
	}
	if (ev.Stage == "headers" || ev.Stage == "body") && ev.TransportKind == "timeout" {
		return "upstream_read_timeout"
	}
	if ev.ProxyUsed && ev.Stage == "connect" && (ev.TransportKind == "refused" || ev.TransportKind == "dns" || ev.TransportKind == "tls" || ev.StatusCode == 502 || ev.StatusCode == 503 || ev.StatusCode == 504) {
		return "proxy_unreachable"
	}
	if ev.Stage == "body_read" && ev.ClientCanceled {
		return "client_abort_during_body_read"
	}
	if ev.Stage == "response" && ev.ClientCanceled {
		return "client_abort_during_response"
	}
	if ev.Stage == "panic" {
		return "gateway_panic"
	}
	if ev.DBError {
		return "gateway_db"
	}
	if ev.RateLimited {
		return "gateway_ratelimit"
	}
	return "unknown"
}
