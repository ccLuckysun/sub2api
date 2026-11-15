//go:build wireinject
// +build wireinject

package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/handler"
	"github.com/Wei-Shaw/sub2api/internal/monitoring"
	"github.com/Wei-Shaw/sub2api/internal/payment"
	"github.com/Wei-Shaw/sub2api/internal/repository"
	"github.com/Wei-Shaw/sub2api/internal/securityaudit"
	"github.com/Wei-Shaw/sub2api/internal/server"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/google/wire"
	"github.com/redis/go-redis/v9"
)

type Application struct {
	Server        *http.Server
	MetricsServer *monitoring.Server
	PromptAudit   *securityaudit.PromptService
	PluginManager *service.PluginManager
	Cleanup       func()
}

func initializeApplication(buildInfo handler.BuildInfo) (*Application, error) {
	wire.Build(
		// Infrastructure layer ProviderSets
		config.ProviderSet,

		// Business layer ProviderSets
		repository.ProviderSet,
		service.ProviderSet,
		securityaudit.ProviderSet,
		payment.ProviderSet,
		middleware.ProviderSet,
		handler.ProviderSet,

		// Monitoring (Prometheus /metrics)
		monitoring.ProviderSet,
		provideAccountLabels,
		provideOpsSampleWriter,
		provideOpsSampleSink,

		// Server layer ProviderSet
		server.ProviderSet,

		// Privacy client factory for OpenAI training opt-out
		providePrivacyClientFactory,

		// BuildInfo provider
		provideServiceBuildInfo,
		providePluginHostInfo,

		// Cleanup function provider
		provideCleanup,
		provideOpsCleanup,

		// Application struct
		wire.Struct(new(Application), "Server", "MetricsServer", "PromptAudit", "PluginManager", "Cleanup"),
	)
	return nil, nil
}

// provideAccountLabels reads one deterministic, immutable startup snapshot.
// Never refresh at runtime: new/renamed/colliding accounts stay unknown.
func provideAccountLabels(instruments *monitoring.RegistryAndMetrics, db *sql.DB) *monitoring.AccountLabels {
	if instruments == nil || instruments.Ops == nil {
		return nil // V2 disabled: no database query or startup side effects.
	}
	empty := func(err error) *monitoring.AccountLabels {
		slog.Warn("ops account label snapshot unavailable; using unknown", "error", err)
		return monitoring.NewAccountLabels(nil)
	}
	if db == nil {
		return empty(fmt.Errorf("nil database"))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	rows, err := db.QueryContext(ctx, "SELECT name FROM accounts WHERE deleted_at IS NULL ORDER BY id")
	if err != nil {
		return empty(err)
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return empty(err)
		}
		names = append(names, name)
	}
	if err := rows.Err(); err != nil {
		return empty(err)
	}
	accounts := monitoring.NewAccountLabels(names)
	labels := make(map[string]struct{})
	for _, name := range names {
		if label := accounts.Label(name); label != "unknown" {
			labels[label] = struct{}{}
		}
	}
	slog.Info("ops account label snapshot loaded", "accounts", len(labels))
	return accounts
}

func providePrivacyClientFactory() service.PrivacyClientFactory {
	return repository.CreatePrivacyReqClient
}

func provideServiceBuildInfo(buildInfo handler.BuildInfo) service.BuildInfo {
	return service.BuildInfo{
		Version:   buildInfo.Version,
		BuildType: buildInfo.BuildType,
	}
}

func providePluginHostInfo(buildInfo handler.BuildInfo) service.PluginHostInfo {
	return service.PluginHostInfo{
		Version:   buildInfo.Version,
		BuildType: buildInfo.BuildType,
	}
}

func provideCleanup(
	entClient *ent.Client,
	rdb *redis.Client,
	metricsServer *monitoring.Server,
	opsMetricsCollector *service.OpsMetricsCollector,
	opsAggregation *service.OpsAggregationService,
	opsAlertEvaluator *service.OpsAlertEvaluatorService,
	opsCleanup *service.OpsCleanupService,
	opsScheduledReport *service.OpsScheduledReportService,
	opsSystemLogSink *service.OpsSystemLogSink,
	opsService *service.OpsService,
	opsIngressReject *service.OpsIngressRejectAggregator,
	apiKeyService *service.APIKeyService,
	authCacheInvalidationWorker *service.AuthCacheInvalidationWorker,
	schedulerSnapshot *service.SchedulerSnapshotService,
	tokenRefresh *service.TokenRefreshService,
	accountExpiry *service.AccountExpiryService,
	cnProviderBalanceCheck *service.CNProviderBalanceCheckService,
	codexVersionSync *service.OpenAICodexVersionSyncService,
	claudeCodeVersionSync *service.ClaudeCodeVersionSyncService,
	proxyExpiry *service.ProxyExpiryService,
	subscriptionExpiry *service.SubscriptionExpiryService,
	usageCleanup *service.UsageCleanupService,
	idempotencyCleanup *service.IdempotencyCleanupService,
	geminiCachedContent *service.GeminiCachedContentService,
	batchImageCleanup *service.BatchImageCleanupService,
	batchImageWorker *service.BatchImageWorkerRuntime,
	pricing *service.PricingService,
	emailQueue *service.EmailQueueService,
	billingCache *service.BillingCacheService,
	usageRecordWorkerPool *service.UsageRecordWorkerPool,
	balanceNotify *service.BalanceNotifyService,
	digestSessions *service.DigestSessionStore,
	subscriptionService *service.SubscriptionService,
	oauth *service.OAuthService,
	openaiOAuth *service.OpenAIOAuthService,
	geminiOAuth *service.GeminiOAuthService,
	antigravityOAuth *service.AntigravityOAuthService,
	grokOAuth *service.GrokOAuthService,
	openAIGateway *service.OpenAIGatewayService,
	scheduledTestRunner *service.ScheduledTestRunnerService,
	backupSvc *service.BackupService,
	paymentOrderExpiry *service.PaymentOrderExpiryService,
	channelMonitorRunner *service.ChannelMonitorRunner,
	channelMonitorV2Aggregator *service.ChannelMonitorV2Aggregator,
	quotaFlusher *service.UserPlatformQuotaUsageFlusher,
	upstreamBillingProbe *service.UpstreamBillingProbeService,
	ollamaCloudUsage *service.OllamaCloudUsageService,
	opencodeGoUsage *service.OpenCodeGoUsageService,
	auditLog *service.AuditLogService,
	openAIAutoReset *service.OpenAIQuotaAutoResetService,
	promptAudit *securityaudit.PromptService,
	pluginManager *service.PluginManager,
) applicationCleanup {
	return func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		type cleanupStep struct {
			name string
			fn   func() error
		}

		// 应用层清理步骤可并行执行，基础设施资源（Redis/Ent）最后按顺序关闭。
		parallelSteps := []cleanupStep{
			{"MetricsServer", func() error {
				if metricsServer != nil {
					return metricsServer.Stop(ctx)
				}
				return nil
			}},
			{"DigestSessionStore", func() error {
				if digestSessions != nil {
					digestSessions.Close()
				}
				return nil
			}},
			{"BalanceNotifyService", func() error {
				if balanceNotify != nil {
					balanceNotify.Stop()
				}
				return nil
			}},
			{"PluginManager", func() error {
				if pluginManager != nil {
					pluginManager.Stop()
				}
				return nil
			}},
			{"OpenAIQuotaAutoResetService", func() error {
				if openAIAutoReset != nil {
					openAIAutoReset.Stop()
				}
				return nil
			}},
			{"OpsIngressRejectAggregator", func() error {
				if opsIngressReject != nil {
					opsIngressReject.Stop()
				}
				return nil
			}},
			{"AuthCacheInvalidationWorker", func() error {
				if authCacheInvalidationWorker != nil {
					authCacheInvalidationWorker.Stop()
				}
				return nil
			}},
			{"AuthCacheInvalidationSubscriber", func() error {
				if apiKeyService != nil {
					apiKeyService.StopAuthCacheInvalidationSubscriber()
				}
				return nil
			}},
			{"OpsRuntimeSettingsRefresh", func() error {
				if opsService != nil {
					opsService.StopRuntimeSettingsRefresh()
				}
				return nil
			}},
			{"PromptAuditService", func() error {
				if promptAudit != nil {
					return promptAudit.Shutdown(ctx)
				}
				return nil
			}},
			{"OpsScheduledReportService", func() error {
				if opsScheduledReport != nil {
					opsScheduledReport.Stop()
				}
				return nil
			}},
			{"OpsCleanupService", func() error {
				if opsCleanup != nil {
					opsCleanup.Stop()
				}
				return nil
			}},
			{"OpsSystemLogSink", func() error {
				if opsSystemLogSink != nil {
					opsSystemLogSink.Stop()
				}
				return nil
			}},
			{"AuditLogService", func() error {
				if auditLog != nil {
					auditLog.Stop()
				}
				return nil
			}},
			{"OpsAlertEvaluatorService", func() error {
				if opsAlertEvaluator != nil {
					opsAlertEvaluator.Stop()
				}
				return nil
			}},
			{"OpsAggregationService", func() error {
				if opsAggregation != nil {
					opsAggregation.Stop()
				}
				return nil
			}},
			{"OpsMetricsCollector", func() error {
				if opsMetricsCollector != nil {
					opsMetricsCollector.Stop()
				}
				return nil
			}},
			{"SchedulerSnapshotService", func() error {
				if schedulerSnapshot != nil {
					schedulerSnapshot.Stop()
				}
				return nil
			}},
			{"UsageCleanupService", func() error {
				if usageCleanup != nil {
					usageCleanup.Stop()
				}
				return nil
			}},
			{"IdempotencyCleanupService", func() error {
				if idempotencyCleanup != nil {
					idempotencyCleanup.Stop()
				}
				return nil
			}},
			{"GeminiCachedContentService", func() error {
				if geminiCachedContent != nil {
					geminiCachedContent.Stop()
				}
				return nil
			}},
			{"BatchImageCleanupService", func() error {
				if batchImageCleanup != nil {
					batchImageCleanup.Stop()
				}
				return nil
			}},
			{"BatchImageWorkerRuntime", func() error {
				if batchImageWorker != nil {
					batchImageWorker.Stop()
				}
				return nil
			}},
			{"TokenRefreshService", func() error {
				tokenRefresh.Stop()
				return nil
			}},
			{"AccountExpiryService", func() error {
				accountExpiry.Stop()
				return nil
			}},
			{"CNProviderBalanceCheckService", func() error {
				if cnProviderBalanceCheck != nil {
					cnProviderBalanceCheck.Stop()
				}
				return nil
			}},
			{"OpenAICodexVersionSyncService", func() error {
				codexVersionSync.Stop()
				return nil
			}},
			{"ClaudeCodeVersionSyncService", func() error {
				claudeCodeVersionSync.Stop()
				return nil
			}},
			{"ProxyExpiryService", func() error {
				proxyExpiry.Stop()
				return nil
			}},
			{"SubscriptionExpiryService", func() error {
				subscriptionExpiry.Stop()
				return nil
			}},
			{"SubscriptionService", func() error {
				if subscriptionService != nil {
					subscriptionService.Stop()
				}
				return nil
			}},
			{"PricingService", func() error {
				pricing.Stop()
				return nil
			}},
			{"EmailQueueService", func() error {
				emailQueue.Stop()
				return nil
			}},
			{"BillingCacheService", func() error {
				billingCache.Stop()
				return nil
			}},
			{"UsageRecordWorkerPool", func() error {
				if usageRecordWorkerPool != nil {
					usageRecordWorkerPool.Stop()
				}
				return nil
			}},
			{"OAuthService", func() error {
				oauth.Stop()
				return nil
			}},
			{"OpenAIOAuthService", func() error {
				openaiOAuth.Stop()
				return nil
			}},
			{"GeminiOAuthService", func() error {
				geminiOAuth.Stop()
				return nil
			}},
			{"AntigravityOAuthService", func() error {
				antigravityOAuth.Stop()
				return nil
			}},
			{"GrokOAuthService", func() error {
				if grokOAuth != nil {
					grokOAuth.Stop()
				}
				return nil
			}},
			{"OpenAIWSPool", func() error {
				if openAIGateway != nil {
					openAIGateway.CloseOpenAIWSPool()
				}
				return nil
			}},
			{"ScheduledTestRunnerService", func() error {
				if scheduledTestRunner != nil {
					scheduledTestRunner.Stop()
				}
				return nil
			}},
			{"BackupService", func() error {
				if backupSvc != nil {
					backupSvc.Stop()
				}
				return nil
			}},
			{"PaymentOrderExpiryService", func() error {
				if paymentOrderExpiry != nil {
					paymentOrderExpiry.Stop()
				}
				return nil
			}},
			{"ChannelMonitorV2Aggregator", func() error {
				if channelMonitorV2Aggregator != nil {
					channelMonitorV2Aggregator.Stop()
				}
				return nil
			}},
			{"ChannelMonitorRunner", func() error {
				if channelMonitorRunner != nil {
					channelMonitorRunner.Stop()
				}
				return nil
			}},
			{"UserPlatformQuotaUsageFlusher", func() error {
				if quotaFlusher != nil {
					quotaFlusher.Stop()
				}
				return nil
			}},
			{"UpstreamBillingProbeService", func() error {
				if upstreamBillingProbe != nil {
					upstreamBillingProbe.Stop()
				}
				return nil
			}},
			{"OllamaCloudUsageService", func() error {
				if ollamaCloudUsage != nil {
					ollamaCloudUsage.Stop()
				}
				return nil
			}},
			{"OpenCodeGoUsageService", func() error {
				if opencodeGoUsage != nil {
					opencodeGoUsage.Stop()
				}
				return nil
			}},
		}

		infraSteps := []cleanupStep{
			{"Redis", func() error {
				if rdb == nil {
					return nil
				}
				return rdb.Close()
			}},
			{"Ent", func() error {
				if entClient == nil {
					return nil
				}
				return entClient.Close()
			}},
		}

		runParallel := func(steps []cleanupStep) {
			var wg sync.WaitGroup
			for i := range steps {
				step := steps[i]
				wg.Add(1)
				go func() {
					defer wg.Done()
					if err := step.fn(); err != nil {
						log.Printf("[Cleanup] %s failed: %v", step.name, err)
						return
					}
					log.Printf("[Cleanup] %s succeeded", step.name)
				}()
			}
			wg.Wait()
		}

		runSequential := func(steps []cleanupStep) {
			for i := range steps {
				step := steps[i]
				if err := step.fn(); err != nil {
					log.Printf("[Cleanup] %s failed: %v", step.name, err)
					continue
				}
				log.Printf("[Cleanup] %s succeeded", step.name)
			}
		}

		runParallel(parallelSteps)
		runSequential(infraSteps)

		// Check if context timed out
		select {
		case <-ctx.Done():
			log.Printf("[Cleanup] Warning: cleanup timed out after 10 seconds")
		default:
			log.Printf("[Cleanup] All cleanup steps completed")
		}
	}
}

// applicationCleanup keeps the existing business cleanup constructor separate
// from the V2 lifecycle wrapper in the Wire graph.
type applicationCleanup func()

func provideOpsCleanup(cleanup applicationCleanup, runtime *monitoring.OpsRuntime, writer *service.OpsSampleWriter) func() {
	return func() { runtime.Stop(); writer.Stop(); cleanup() }
}

// provideOpsSampleWriter starts only for explicitly enabled sample persistence.
// Recorder emission is wired separately; this provider performs no DB queries.
func provideOpsSampleWriter(cfg *config.Config, db *sql.DB) *service.OpsSampleWriter {
	if cfg == nil || !cfg.Monitoring.OpsMetricsEnabled || !cfg.Monitoring.OpsSamplesEnabled {
		return nil
	}
	writer := service.NewOpsSampleWriter(repository.NewOpsSampleRepository(db))
	writer.Start()
	return writer
}

func provideOpsSampleSink(writer *service.OpsSampleWriter) monitoring.OpsSampleSink {
	if writer == nil {
		return nil
	}
	return writer
}
