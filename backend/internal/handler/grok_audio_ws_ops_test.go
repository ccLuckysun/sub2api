package handler

import (
	"errors"
	"io"
	"testing"

	coderws "github.com/coder/websocket"
	"github.com/stretchr/testify/require"
)

func TestGrokAudioWSCloseClassification(t *testing.T) {
	abnormal := coderws.CloseError{Code: coderws.StatusAbnormalClosure, Reason: "unexpected EOF"}
	require.True(t, isExpectedGrokRealtimeClose(abnormal), "1006 should still count as quiet expected log level")
	require.False(t, isNormalWSClose(abnormal), "1006 must NOT count as closed_normal in WS metrics")

	internalErr := coderws.CloseError{Code: coderws.StatusInternalError, Reason: "server error"}
	require.False(t, isNormalWSClose(internalErr))
	require.False(t, isNormalWSClose(io.EOF))
	require.False(t, isNormalWSClose(errors.New("tcp connection reset by peer")))

	for _, normalErr := range []error{
		nil,
		coderws.CloseError{Code: coderws.StatusNormalClosure},
		coderws.CloseError{Code: coderws.StatusGoingAway},
		coderws.CloseError{Code: coderws.StatusNoStatusRcvd},
	} {
		require.True(t, isNormalWSClose(normalErr), "expected normal close for %v", normalErr)
	}
}
