package monitoring

import (
	"context"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
)

func TestWSObserverNilAndUnsupportedEndpointAreNoops(t *testing.T) {
	var nilObs *WSObserver
	nilObs.MarkRejected()
	nilObs.MarkHandshakeFailed()
	nilObs.MarkConnected()
	nilObs.MarkClosed(true)
	nilObs.FinalizeFallback(500)

	reg := prometheus.NewRegistry()
	m, err := NewOpsMetrics(reg)
	require.NoError(t, err)

	require.Nil(t, NewWSObserver(nil, "responses"))
	require.Nil(t, NewWSObserver(m, "other"))
	require.Nil(t, WSObserverFromContext(context.TODO()))

	ctx := WithWSObserver(context.TODO(), nil)
	require.Nil(t, WSObserverFromContext(ctx))
}

func TestWSObserverLifecycleAndIdempotency(t *testing.T) {
	reg := prometheus.NewRegistry()
	m, err := NewOpsMetrics(reg)
	require.NoError(t, err)

	t.Run("connected then closed_normal idempotent", func(t *testing.T) {
		obs := NewWSObserver(m, "responses")
		ctx := WithWSObserver(context.Background(), obs)
		require.Same(t, obs, WSObserverFromContext(ctx))

		obs.MarkConnected()
		obs.MarkConnected() // duplicate ignored
		obs.MarkRejected()  // ignored after connected
		require.Equal(t, 1.0, testutil.ToFloat64(m.WSActiveConnections.WithLabelValues("responses")))
		require.Equal(t, 1.0, testutil.ToFloat64(m.WSConnectionsTotal.WithLabelValues("responses", "connected")))

		obs.MarkClosed(true)
		obs.MarkClosed(false)      // duplicate ignored
		obs.FinalizeFallback(500)  // no-op after close
		require.Equal(t, 0.0, testutil.ToFloat64(m.WSActiveConnections.WithLabelValues("responses")))
		require.Equal(t, 1.0, testutil.ToFloat64(m.WSConnectionsTotal.WithLabelValues("responses", "closed_normal")))
		require.Equal(t, 0.0, testutil.ToFloat64(m.WSConnectionsTotal.WithLabelValues("responses", "closed_error")))
		require.Equal(t, 0.0, testutil.ToFloat64(m.WSConnectionsTotal.WithLabelValues("responses", "rejected")))
	})

	t.Run("handshake failed does not double count on finalize fallback", func(t *testing.T) {
		obs := NewWSObserver(m, "realtime")
		obs.MarkHandshakeFailed()
		obs.FinalizeFallback(400)
		require.Equal(t, 0.0, testutil.ToFloat64(m.WSActiveConnections.WithLabelValues("realtime")))
		require.Equal(t, 1.0, testutil.ToFloat64(m.WSConnectionsTotal.WithLabelValues("realtime", "handshake_failed")))
		require.Equal(t, 0.0, testutil.ToFloat64(m.WSConnectionsTotal.WithLabelValues("realtime", "rejected")))
	})

	t.Run("middleware rejection fallback and active panic cleanup", func(t *testing.T) {
		rejObs := NewWSObserver(m, "realtime")
		rejObs.FinalizeFallback(401)
		rejObs.FinalizeFallback(401)
		require.Equal(t, 1.0, testutil.ToFloat64(m.WSConnectionsTotal.WithLabelValues("realtime", "rejected")))

		panicObs := NewWSObserver(m, "realtime")
		panicObs.MarkConnected()
		require.Equal(t, 1.0, testutil.ToFloat64(m.WSActiveConnections.WithLabelValues("realtime")))
		panicObs.FinalizeFallback(500)
		require.Equal(t, 0.0, testutil.ToFloat64(m.WSActiveConnections.WithLabelValues("realtime")))
		require.Equal(t, 1.0, testutil.ToFloat64(m.WSConnectionsTotal.WithLabelValues("realtime", "closed_error")))
	})
}
