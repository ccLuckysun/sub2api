package repository

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/monitoring"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/require"
)

func TestHTTPUpstreamOpsPublicTLSProxyConnectStatus(t *testing.T) {
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodConnect, r.Method)
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer proxy.Close()
	ctx, rec, attempt, metrics := repositoryOpsFixture(t)
	observedStatus := 0
	ctx = tlsfingerprint.WithProxyConnectObserver(ctx, func(status int) { observedStatus = status })
	req, err := http.NewRequestWithContext(ctx, "POST", "https://fixture.invalid/v1/messages", strings.NewReader("unchanged"))
	require.NoError(t, err)
	response, err := NewHTTPUpstream(nil).DoWithTLS(req, proxy.URL, 1, 1, &tlsfingerprint.Profile{Name: "CONNECT-observation"})
	require.Nil(t, response)
	require.ErrorContains(t, err, "proxy CONNECT failed: 502 Bad Gateway")
	require.Equal(t, 502, observedStatus)
	ev := monitoring.FailureEvidence{ProxyUsed: true, Stage: "connect", StatusCode: observedStatus}
	attempt.Done("upstream_error", "", ev)
	rec.Finalize(monitoring.FinalizeInput{Status: 502})
	require.Equal(t, 1.0, testutil.ToFloat64(metrics.UpstreamRequests.WithLabelValues("unknown", "upstream_error", "proxy_unreachable")))
	require.Equal(t, 1.0, testutil.ToFloat64(metrics.GatewayRequests.WithLabelValues("upstream_error", "proxy_unreachable")))
}

func repositoryOpsFixture(t *testing.T) (context.Context, *monitoring.OpsRecorder, *monitoring.OpsAttempt, *monitoring.OpsMetrics) {
	t.Helper()
	m, err := monitoring.NewOpsMetrics(prometheus.NewRegistry())
	require.NoError(t, err)
	ctx, rec := monitoring.BeginGatewayRequest(context.Background(), "/v1/messages", m)
	a := rec.StartAttempt("unknown")
	return monitoring.WithAttempt(ctx, a), rec, a, m
}

func TestHTTPUpstreamOpsDecodedOutermostAndCancellation(t *testing.T) {
	payload := strings.Repeat("decoded-中文-", 40)
	requestBody := "request-body"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		require.Equal(t, requestBody, string(data))
		require.Equal(t, "unchanged", r.Header.Get("X-Ops-Test"))
		w.Header().Set("Content-Encoding", "gzip")
		gz := gzip.NewWriter(w)
		_, _ = gz.Write([]byte(payload))
		_ = gz.Close()
	}))
	defer server.Close()
	ctx, rec, a, m := repositoryOpsFixture(t)
	req, err := http.NewRequestWithContext(ctx, "POST", server.URL, strings.NewReader(requestBody))
	require.NoError(t, err)
	req.Header.Set("X-Ops-Test", "unchanged")
	req.Header.Set("Accept-Encoding", "gzip")
	resp, err := doUpstreamRequest(server.Client(), req)
	require.NoError(t, err)
	handle := monitoring.ResponseHandle(resp)
	require.NotNil(t, handle)
	a.UseResponseFor(handle)
	require.Equal(t, "resp_upstream", resp.Body.(monitoring.OpsObservation).OpsObservationDirection())
	require.Equal(t, "", resp.Header.Get("Content-Encoding"))
	data, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, payload, string(data))
	require.NoError(t, resp.Body.Close())
	require.ErrorIs(t, resp.Request.Context().Err(), context.Canceled, "original cancelOnClose semantics remain")
	a.Done("success", "", monitoring.FailureEvidence{})
	rec.Finalize(monitoring.FinalizeInput{Status: 200})
	require.Equal(t, float64(len(requestBody)), testutil.ToFloat64(m.TransferBytes.WithLabelValues("req_upstream")))
	require.Equal(t, float64(len(payload)), testutil.ToFloat64(m.TransferBytes.WithLabelValues("resp_upstream")))
	require.Equal(t, 1.0, testutil.ToFloat64(m.UpstreamRequests.WithLabelValues("unknown", "success", "")))
}

func repositoryOpsHistogram(t *testing.T, observer prometheus.Observer) *dto.Histogram {
	t.Helper()
	var metric dto.Metric
	require.NoError(t, observer.(prometheus.Metric).Write(&metric))
	return metric.GetHistogram()
}

func TestHTTPUpstreamOpsPublicResponseIdentity(t *testing.T) {
	for _, withTLS := range []bool{false, true} {
		name := "Do"
		if withTLS {
			name = "DoWithTLS"
		}
		for _, tc := range []struct {
			name, payload string
			gzip          bool
		}{
			{name: "JSON", payload: `{"message":"ok"}`},
			{name: "SSE", payload: "data: ok\n\n"},
			{name: "gzip", payload: strings.Repeat("decoded-中文-", 40), gzip: true},
			{name: "empty"},
		} {
			t.Run(name+"/"+tc.name, func(t *testing.T) {
				const requestBody = "public-request-body"
				requests := make(chan string, 1)
				server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					data, err := io.ReadAll(r.Body)
					if err != nil {
						t.Errorf("read upstream request: %v", err)
						return
					}
					requests <- string(data)
					if tc.gzip {
						w.Header().Set("Content-Encoding", "gzip")
						gz := gzip.NewWriter(w)
						_, _ = io.WriteString(gz, tc.payload)
						_ = gz.Close()
					} else {
						_, _ = io.WriteString(w, tc.payload)
					}
				}))
				if withTLS {
					server.StartTLS()
				} else {
					server.Start()
				}
				defer server.Close()
				upstream, do := lifecycleClient(t, server, withTLS)
				ctx, rec, a, m := repositoryOpsFixture(t)
				req, err := http.NewRequestWithContext(ctx, "POST", server.URL, strings.NewReader(requestBody))
				require.NoError(t, err)
				req.Header.Set("Accept-Encoding", "gzip")
				resp, err := do(req)
				require.NoError(t, err)
				t.Cleanup(func() { _ = resp.Body.Close() })
				require.Equal(t, requestBody, <-requests)
				handle := monitoring.ResponseHandle(resp)
				require.NotNil(t, handle, "public response retains identity through trackedBody")
				require.Equal(t, "resp_upstream", resp.Body.(monitoring.OpsObservation).OpsObservationDirection())
				a.UseResponseFor(handle)
				outer := resp.Body
				resp.Body = monitoring.ObserveResponseBody(rec, a, handle, resp.Body)
				require.Same(t, outer, resp.Body, "reinstallation preserves tracking and adds no counter")
				require.Same(t, handle, monitoring.ResponseHandle(resp))
				require.Zero(t, testutil.ToFloat64(m.TransferBytes.WithLabelValues("resp_upstream")), "identity lookup and reinstallation never read the body")
				ttft := m.UpstreamTTFT.WithLabelValues("unknown")
				require.Zero(t, repositoryOpsHistogram(t, ttft).GetSampleCount())
				data, err := io.ReadAll(resp.Body)
				require.NoError(t, err)
				require.Equal(t, tc.payload, string(data))
				require.Zero(t, repositoryOpsHistogram(t, ttft).GetSampleCount(), "only explicit Done observes TTFT")
				for _, entry := range upstream.clients {
					require.EqualValues(t, 1, atomic.LoadInt64(&entry.inFlight), "EOF does not release the Close callback")
				}
				require.NoError(t, resp.Body.Close())
				requireNoUpstreamInFlight(t, upstream)
				require.NoError(t, resp.Body.Close())
				requireNoUpstreamInFlight(t, upstream)
				a.Done("success", "", monitoring.FailureEvidence{})
				wantTTFT := uint64(1)
				if tc.payload == "" {
					wantTTFT = 0
				}
				require.Equal(t, wantTTFT, repositoryOpsHistogram(t, ttft).GetSampleCount())
				rec.Finalize(monitoring.FinalizeInput{Status: 200})
				require.Equal(t, wantTTFT, repositoryOpsHistogram(t, ttft).GetSampleCount(), "Finalize cannot observe again")
				require.Equal(t, float64(len(requestBody)), testutil.ToFloat64(m.TransferBytes.WithLabelValues("req_upstream")))
				require.Equal(t, float64(len(tc.payload)), testutil.ToFloat64(m.TransferBytes.WithLabelValues("resp_upstream")), "actual decoded n counted exactly once")
			})
		}
	}
}

func TestHTTPUpstreamOpsTrackedBodyWithoutObservation(t *testing.T) {
	closes := 0
	body := wrapTrackedBody(io.NopCloser(strings.NewReader("unobserved")), func() { closes++ })
	observation := body.(monitoring.OpsObservation)
	require.Empty(t, observation.OpsObservationDirection())
	require.Nil(t, observation.OpsResponseHandle())
	require.Nil(t, monitoring.ResponseHandle(&http.Response{Body: body}))
	data, err := io.ReadAll(body)
	require.NoError(t, err)
	require.Equal(t, "unobserved", string(data))
	require.NoError(t, body.Close())
	require.NoError(t, body.Close())
	require.Equal(t, 1, closes)
}

func TestHTTPUpstreamOpsGrokFallbackAndReplay(t *testing.T) {
	denied := `{"error":"Access denied"}`
	for _, mode := range []string{"success", "fallback_error", "fallback_status", "not_compatible", "encoded_success"} {
		t.Run(mode, func(t *testing.T) {
			entryBefore := time.Now()
			ctx, rec, a, m := repositoryOpsFixture(t)
			entryAfter := time.Now()
			input := `{"model":"grok-test","input":"same bytes"}`
			output := `{"id":"fallback-ok"}`
			rec.MarkBodyRead(int64(len(input)), int64(len(input)), "complete")
			rec.AddBytes("req_decoded", int64(len(input)), "complete")
			firstBody := denied
			if mode == "not_compatible" {
				firstBody = `{"error":"subscription required"}`
			}
			var requests [][]byte
			var headers []http.Header
			var hosts []string
			client := httpClientWithGrokAccessDeniedFallback(&http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				body, err := io.ReadAll(req.Body)
				require.NoError(t, err)
				requests = append(requests, body)
				headers = append(headers, req.Header.Clone())
				hosts = append(hosts, req.URL.Hostname())
				if len(requests) == 1 {
					return &http.Response{StatusCode: 403, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(firstBody)), Request: req}, nil
				}
				if mode == "fallback_error" {
					return nil, errors.New("second send failed")
				}
				status := 200
				if mode == "fallback_status" {
					status = 503
				}
				header := http.Header{}
				var reader io.ReadCloser = io.NopCloser(strings.NewReader(output))
				if mode == "encoded_success" {
					var compressed bytes.Buffer
					gz := gzip.NewWriter(&compressed)
					_, _ = gz.Write([]byte(output))
					_ = gz.Close()
					header.Set("Content-Encoding", "gzip")
					reader = io.NopCloser(bytes.NewReader(compressed.Bytes()))
				}
				return &http.Response{StatusCode: status, Header: header, Body: reader, Request: req}, nil
			})})
			req, err := http.NewRequestWithContext(ctx, "POST", "https://cli-chat-proxy.grok.com/v1/responses", strings.NewReader(input))
			require.NoError(t, err)
			req.Header.Set("Authorization", "Bearer preserved")
			req.Header.Set("X-XAI-Token-Auth", "xai-grok-cli")
			req.Header.Set("X-Sentinel", "untouched")
			sendBefore := time.Now()
			resp, err := doUpstreamRequest(client, req)
			require.NoError(t, err)
			handle := monitoring.ResponseHandle(resp)
			require.NotNil(t, handle, "identity survives replay/decompression/cancel wrappers")
			success := mode == "success" || mode == "encoded_success"
			if success {
				a.UseResponseFor(handle)
			}
			data, err := io.ReadAll(resp.Body)
			require.NoError(t, err)
			require.NoError(t, resp.Body.Close())
			count := 2
			if mode == "not_compatible" {
				count = 1
			}
			require.Len(t, requests, count)
			for i, body := range requests {
				require.Equal(t, input, string(body))
				require.Equal(t, "untouched", headers[i].Get("X-Sentinel"))
				require.Equal(t, "Bearer preserved", headers[i].Get("Authorization"))
			}
			if count == 2 {
				require.Equal(t, []string{grokCLIProxyHost, grokOfficialAPIHost}, hosts)
				require.Empty(t, headers[1].Get("X-XAI-Token-Auth"))
			}
			expectedResponseBytes := len(firstBody)
			if success {
				require.Equal(t, output, string(data))
				require.Equal(t, 200, resp.StatusCode)
				expectedResponseBytes += len(output)
				a.Done("success", "", monitoring.FailureEvidence{})
			} else {
				require.Equal(t, firstBody, string(data))
				require.Equal(t, 403, resp.StatusCode)
				a.Done("upstream_error", "", monitoring.FailureEvidence{StatusCode: 403, Stage: "headers"})
			}
			rec.AddBytes("resp_downstream", int64(len(data)), "complete")
			terminalAfter := time.Now()
			rec.Finalize(monitoring.FinalizeInput{Status: resp.StatusCode})
			require.Equal(t, float64(count*len(input)), testutil.ToFloat64(m.TransferBytes.WithLabelValues("req_upstream")))
			require.Equal(t, float64(expectedResponseBytes), testutil.ToFloat64(m.TransferBytes.WithLabelValues("resp_upstream")), "buffer replay and outer wrapper cannot count a body twice")
			result, reason := "success", ""
			if !success {
				result, reason = "upstream_error", "upstream_auth"
			}
			require.Equal(t, 1.0, testutil.ToFloat64(m.UpstreamRequests.WithLabelValues("unknown", result, reason)), "fallback remains one logical attempt")
			require.Equal(t, 1.0, testutil.ToFloat64(m.ModelRequests.WithLabelValues("other", result)))
			require.Equal(t, 1.0, testutil.ToFloat64(m.GatewayRequests.WithLabelValues(result, reason)))
			outcome := "all_failed"
			wantTTFT := uint64(0)
			if success {
				outcome = "first_success"
				wantTTFT = 1
			}
			require.Equal(t, 1.0, testutil.ToFloat64(m.Attempts.WithLabelValues(outcome)))
			require.Equal(t, wantTTFT, repositoryOpsHistogram(t, m.UpstreamTTFT.WithLabelValues("unknown")).GetSampleCount())
			bodyMetric := repositoryOpsHistogram(t, m.RequestBody.WithLabelValues("responses"))
			require.EqualValues(t, 1, bodyMetric.GetSampleCount())
			require.Equal(t, float64(len(input)), bodyMetric.GetSampleSum())
			for direction, n := range map[string]int{"in_wire": len(input), "req_decoded": len(input), "resp_downstream": len(data)} {
				require.Equal(t, float64(n), testutil.ToFloat64(m.TransferBytes.WithLabelValues(direction)), direction)
			}
			total := 0.0
			for _, phase := range []string{"body_read", "wait", "gateway", "upstream", "response", "finish"} {
				value := repositoryOpsHistogram(t, m.RequestPhase.WithLabelValues(phase))
				require.EqualValues(t, 1, value.GetSampleCount(), phase)
				require.GreaterOrEqual(t, value.GetSampleSum(), 0.0, phase)
				total += value.GetSampleSum()
			}
			require.GreaterOrEqual(t, total, sendBefore.Sub(entryAfter).Seconds()-.001)
			require.LessOrEqual(t, total, terminalAfter.Sub(entryBefore).Seconds()+.001)
			t.Logf("Grok_%s physical=%d logical=1 M6=%s M10=%s/%s M11=%d req_upstream=%d resp_upstream=%d", mode, count, outcome, result, reason, wantTTFT, count*len(input), expectedResponseBytes)
		})
	}
}
