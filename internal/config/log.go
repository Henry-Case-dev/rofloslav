package config

import (
	"log"
	"strings"

	"github.com/Henry-Case-dev/rofloslav/internal/utils"
)

// logLoadedConfig выводит загруженную конфигурацию в лог (маскируя секреты)
func logLoadedConfig(cfg *Config) {
	log.Println("--- Загруженная конфигурация: ---")
	log.Printf("  TelegramToken: %s", maskSecret(cfg.TelegramToken))
	log.Printf("  LLMProvider: %s", cfg.LLMProvider)
	log.Printf("  DefaultPrompt: %s...", utils.TruncateString(cfg.DefaultPrompt, 100))
	log.Printf("  DirectPrompt: %s...", utils.TruncateString(cfg.DirectPrompt, 100))
	log.Printf("  ClassifyDirectMessagePrompt: %s...", utils.TruncateString(cfg.ClassifyDirectMessagePrompt, 100))
	log.Printf("  SeriousDirectPrompt: %s...", utils.TruncateString(cfg.SeriousDirectPrompt, 100))
	log.Printf("  DailyTakePrompt: %s...", utils.TruncateString(cfg.DailyTakePrompt, 100))
	log.Printf("  SummaryPrompt: %s...", utils.TruncateString(cfg.SummaryPrompt, 100))
	log.Printf("  RateLimitStaticText: %s", cfg.RateLimitStaticText)
	log.Printf("  RateLimitPrompt: %s...", utils.TruncateString(cfg.RateLimitPrompt, 100))
	log.Printf("  SRACH_WARNING_PROMPT: %s...", utils.TruncateString(cfg.SRACH_WARNING_PROMPT, 100))
	log.Printf("  SRACH_ANALYSIS_PROMPT: %s...", utils.TruncateString(cfg.SRACH_ANALYSIS_PROMPT, 100))
	log.Printf("  SRACH_CONFIRM_PROMPT: %s...", utils.TruncateString(cfg.SRACH_CONFIRM_PROMPT, 100))
	log.Printf("  WelcomePrompt: %s...", utils.TruncateString(cfg.WelcomePrompt, 100))
	log.Printf("  VoiceFormatPrompt: %s...", utils.TruncateString(cfg.VoiceFormatPrompt, 100))
	log.Printf("  DirectReplyLimitPrompt: %s...", utils.TruncateString(cfg.DirectReplyLimitPrompt, 100))

	if cfg.LLMProvider == ProviderGemini {
		log.Printf("  GeminiAPIKey: %s", maskSecret(cfg.GeminiAPIKey))
		log.Printf("  GeminiModelName: %s", cfg.GeminiModelName)
	}
	if cfg.LLMProvider == ProviderDeepSeek {
		log.Printf("  DeepSeekAPIKey: %s", maskSecret(cfg.DeepSeekAPIKey))
		log.Printf("  DeepSeekModelName: %s", cfg.DeepSeekModelName)
		log.Printf("  DeepSeekBaseURL: %s", cfg.DeepSeekBaseURL) // Не секрет
	}
	if cfg.LLMProvider == ProviderOpenRouter {
		log.Printf("  OpenRouterAPIKey: %s", maskSecret(cfg.OpenRouterAPIKey))
		log.Printf("  OpenRouterModelName: %s", cfg.OpenRouterModelName)
		log.Printf("  OpenRouterSiteURL: %s", cfg.OpenRouterSiteURL)     // Не секрет
		log.Printf("  OpenRouterSiteTitle: %s", cfg.OpenRouterSiteTitle) // Не секрет
	}

	log.Printf("  MinMessages: %d", cfg.MinMessages)
	log.Printf("  MaxMessages: %d", cfg.MaxMessages)
	log.Printf("  ContextWindow: %d", cfg.ContextWindow)
	log.Printf("  DailyTakeTime: %d", cfg.DailyTakeTime)
	log.Printf("  TimeZone: %s", cfg.TimeZone)
	log.Printf("  SummaryIntervalHours: %d", cfg.SummaryIntervalHours)
	log.Printf("  SrachAnalysisEnabled Default: %t", cfg.SrachAnalysisEnabled)
	log.Printf("  SrachKeywords: [%s] (%d keywords)", utils.TruncateString(strings.Join(cfg.SrachKeywords, ", "), 100), len(cfg.SrachKeywords))
	log.Printf("  VoiceTranscriptionEnabled Default: %t", cfg.VoiceTranscriptionEnabledDefault)
	log.Printf("  DirectReplyLimitEnabled Default: %t", cfg.DirectReplyLimitEnabledDefault)
	log.Printf("  DirectReplyLimitCount Default: %d", cfg.DirectReplyLimitCountDefault)
	log.Printf("  DirectReplyLimitDuration Default: %v", cfg.DirectReplyLimitDurationDefault)
	log.Printf("  StorageType: %s", cfg.StorageType)

	if cfg.StorageType == StorageTypePostgres {
		log.Printf("  PostgresqlHost: %s", cfg.PostgresqlHost)
		log.Printf("  PostgresqlPort: %s", cfg.PostgresqlPort)
		log.Printf("  PostgresqlUser: %s", cfg.PostgresqlUser)
		log.Printf("  PostgresqlPassword: %s", maskSecret(cfg.PostgresqlPassword))
		log.Printf("  PostgresqlDbname: %s", cfg.PostgresqlDbname)
	}
	if cfg.StorageType == StorageTypeMongo {
		log.Printf("  MongoDbURI: %s", maskSecretURI(cfg.MongoDbURI))
		log.Printf("  MongoDbName: %s", cfg.MongoDbName)
		log.Printf("  MongoDbMessagesCollection: %s", cfg.MongoDbMessagesCollection)
		log.Printf("  MongoDbUserProfilesCollection: %s", cfg.MongoDbUserProfilesCollection)
		log.Printf("  MongoDbSettingsCollection: %s", cfg.MongoDbSettingsCollection)
	}

	log.Printf("  AdminUsernames: [%s]", strings.Join(cfg.AdminUsernames, ", "))
	log.Printf("  Debug: %t", cfg.Debug)

	log.Printf("  LongTermMemoryEnabled: %t", cfg.LongTermMemoryEnabled)
	if cfg.LongTermMemoryEnabled {
		log.Printf("    GeminiEmbeddingModelName: %s", cfg.GeminiEmbeddingModelName)
		log.Printf("    MongoVectorIndexName: %s", cfg.MongoVectorIndexName)
		log.Printf("    LongTermMemoryFetchK: %d", cfg.LongTermMemoryFetchK)
	}
	log.Printf("  BackfillBatchSize: %d", cfg.BackfillBatchSize)
	log.Printf("  BackfillBatchDelay: %v", cfg.BackfillBatchDelay)
	log.Printf("  PhotoAnalysisEnabled: %t", cfg.PhotoAnalysisEnabled)
	log.Printf("  PhotoAnalysisPrompt: %s...", utils.TruncateString(cfg.PhotoAnalysisPrompt, 100))

	// --- Логгирование настроек автоочистки MongoDB ---
	log.Printf("  MongoCleanupEnabled: %t", cfg.MongoCleanupEnabled)
	if cfg.MongoCleanupEnabled {
		log.Printf("    MongoCleanupSizeLimitMB: %d", cfg.MongoCleanupSizeLimitMB)
		log.Printf("    MongoCleanupIntervalMinutes: %d", cfg.MongoCleanupIntervalMinutes)
		log.Printf("    MongoCleanupChunkDurationHours: %d", cfg.MongoCleanupChunkDurationHours)
	}
	// --- Конец логгирования ---

	log.Println("--- Конфигурация завершена ---")
}
