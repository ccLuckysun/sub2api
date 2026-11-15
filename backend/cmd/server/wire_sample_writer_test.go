package main

import (
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestProvideOpsSampleWriterDisabled(t *testing.T) {
	require.Nil(t, provideOpsSampleWriter(nil, nil))
	cfg := &config.Config{}
	require.Nil(t, provideOpsSampleWriter(cfg, nil))
	cfg.Monitoring.OpsMetricsEnabled = true
	require.Nil(t, provideOpsSampleWriter(cfg, nil))
	cfg.Monitoring.OpsSamplesEnabled = true
	writer := provideOpsSampleWriter(cfg, nil)
	require.NotNil(t, writer)
	writer.Stop()
	writer.Stop()
}

func TestProvideOpsSampleSinkNil(t *testing.T) {
	require.Nil(t, provideOpsSampleSink(nil))
	cfg := &config.Config{}
	cfg.Monitoring.OpsMetricsEnabled = true
	cfg.Monitoring.OpsSamplesEnabled = true
	writer := provideOpsSampleWriter(cfg, nil)
	defer writer.Stop()
	require.Same(t, writer, provideOpsSampleSink(writer))
}
