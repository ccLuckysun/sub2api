package service

import (
	"context"
	"net/http"
	"sync/atomic"

	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
)

type httpUpstreamProxyConnectKey struct{}

// HTTPUpstreamProxyConnectObservation preserves CONNECT status out of band;
// net/http's original error, response and body behavior remain unchanged.
type HTTPUpstreamProxyConnectObservation struct{ status atomic.Int64 }

func withHTTPUpstreamProxyConnectObservation(ctx context.Context) (context.Context, *HTTPUpstreamProxyConnectObservation) {
	observation := &HTTPUpstreamProxyConnectObservation{}
	ctx = context.WithValue(ctx, httpUpstreamProxyConnectKey{}, observation)
	ctx = tlsfingerprint.WithProxyConnectObserver(ctx, func(status int) { observation.status.Store(int64(status)) })
	return ctx, observation
}

// NoteHTTPUpstreamProxyConnectResponse only records an already available status.
// A zero status resets evidence before another physical HTTP send.
func NoteHTTPUpstreamProxyConnectResponse(ctx context.Context, status int) {
	if observation, _ := ctx.Value(httpUpstreamProxyConnectKey{}).(*HTTPUpstreamProxyConnectObservation); observation != nil {
		observation.status.Store(int64(status))
	}
}

func (o *HTTPUpstreamProxyConnectObservation) failedStatus() int {
	if o != nil {
		if status := int(o.status.Load()); status >= 400 {
			return status
		}
	}
	return 0
}

// HTTPUpstream 上游 HTTP 请求接口
// 用于向上游 API（Claude、OpenAI、Gemini 等）发送请求
type HTTPUpstream interface {
	// Do 执行 HTTP 请求（不启用 TLS 指纹）
	Do(req *http.Request, proxyURL string, accountID int64, accountConcurrency int) (*http.Response, error)

	// DoWithTLS 执行带 TLS 指纹伪装的 HTTP 请求
	//
	// profile 参数:
	//   - nil: 不启用 TLS 指纹，行为与 Do 方法相同
	//   - non-nil: 使用指定的 Profile 进行 TLS 指纹伪装
	//
	// Profile 由调用方通过 TLSFingerprintProfileService 解析后传入，
	// 支持按账号绑定的数据库 profile 或内置默认 profile。
	DoWithTLS(req *http.Request, proxyURL string, accountID int64, accountConcurrency int, profile *tlsfingerprint.Profile) (*http.Response, error)
}
