package config

import (
	"fmt"
	"log" // Нужен для логгирования предупреждений
	"strings"
)

// ValidateConfig проверяет корректность загруженной конфигурации
func ValidateConfig(cfg *Config) error {
	if cfg.TelegramToken == "" {
		return fmt.Errorf("ошибка конфигурации: TELEGRAM_TOKEN не установлен")
	}

	// Валидация LLM Provider и ключей
	switch cfg.LLMProvider {
	case ProviderGemini:
		if cfg.GeminiAPIKey == "" {
			return fmt.Errorf("ошибка конфигурации: LLM_PROVIDER='gemini', но GEMINI_API_KEY не установлен")
		}
		if cfg.GeminiModelName == "" {
			return fmt.Errorf("ошибка конфигурации: LLM_PROVIDER='gemini', но GEMINI_MODEL_NAME не установлен")
		}
		if cfg.LongTermMemoryEnabled && cfg.GeminiEmbeddingModelName == "" {
			return fmt.Errorf("LONG_TERM_MEMORY_ENABLED=true, но GEMINI_EMBEDDING_MODEL_NAME не установлен")
		}
	case ProviderDeepSeek:
		if cfg.DeepSeekAPIKey == "" {
			return fmt.Errorf("ошибка конфигурации: LLM_PROVIDER='deepseek', но DEEPSEEK_API_KEY не установлен")
		}
		if cfg.DeepSeekModelName == "" {
			return fmt.Errorf("ошибка конфигурации: LLM_PROVIDER='deepseek', но DEEPSEEK_MODEL_NAME не установлен")
		}
		// Для DeepSeek также нужен GeminiEmbeddingModelName, если LongTermMemoryEnabled
		if cfg.LongTermMemoryEnabled && cfg.GeminiEmbeddingModelName == "" {
			log.Println("[WARN] LLM_PROVIDER='deepseek' и LONG_TERM_MEMORY_ENABLED=true, но GEMINI_EMBEDDING_MODEL_NAME не установлен. Используется 'embedding-001' по умолчанию.")
			// Можно установить дефолт здесь или при загрузке
			if cfg.GeminiEmbeddingModelName == "" {
				cfg.GeminiEmbeddingModelName = "embedding-001"
			}
		}
		if cfg.LongTermMemoryEnabled && cfg.GeminiAPIKey == "" {
			return fmt.Errorf("LLM_PROVIDER='deepseek' и LONG_TERM_MEMORY_ENABLED=true, но GEMINI_API_KEY не установлен (нужен для эмбеддингов)")
		}
	case ProviderOpenRouter:
		if cfg.OpenRouterAPIKey == "" {
			return fmt.Errorf("ошибка конфигурации: LLM_PROVIDER='openrouter', но OPENROUTER_API_KEY не установлен")
		}
		if cfg.OpenRouterModelName == "" {
			return fmt.Errorf("ошибка конфигурации: LLM_PROVIDER='openrouter', но OPENROUTER_MODEL_NAME не установлен")
		}
		// Для OpenRouter также нужен GeminiEmbeddingModelName, если LongTermMemoryEnabled
		if cfg.LongTermMemoryEnabled && cfg.GeminiEmbeddingModelName == "" {
			log.Println("[WARN] LLM_PROVIDER='openrouter' и LONG_TERM_MEMORY_ENABLED=true, но GEMINI_EMBEDDING_MODEL_NAME не установлен. Используется 'embedding-001' по умолчанию.")
			if cfg.GeminiEmbeddingModelName == "" {
				cfg.GeminiEmbeddingModelName = "embedding-001"
			}
		}
		if cfg.LongTermMemoryEnabled && cfg.GeminiAPIKey == "" {
			return fmt.Errorf("LLM_PROVIDER='openrouter' и LONG_TERM_MEMORY_ENABLED=true, но GEMINI_API_KEY не установлен (нужен для эмбеддингов)")
		}
	default:
		return fmt.Errorf("неизвестный LLM_PROVIDER: '%s'. Допустимые значения: 'gemini', 'deepseek', 'openrouter'", cfg.LLMProvider)
	}

	// Валидация интервалов
	if cfg.DailyTakeTime < 0 || cfg.DailyTakeTime > 23 {
		return fmt.Errorf("ошибка конфигурации: DAILY_TAKE_TIME (%d) должен быть в диапазоне 0-23", cfg.DailyTakeTime)
	}
	if cfg.MinMessages < 1 || cfg.MinMessages > cfg.MaxMessages {
		return fmt.Errorf("ошибка конфигурации: MIN_MESSAGES (%d) должен быть >= 1 и <= MAX_MESSAGES (%d)", cfg.MinMessages, cfg.MaxMessages)
	}
	if cfg.MaxMessages < 1 {
		return fmt.Errorf("ошибка конфигурации: MAX_MESSAGES (%d) должен быть >= 1", cfg.MaxMessages)
	}
	if cfg.ContextWindow < 1 {
		return fmt.Errorf("ошибка конфигурации: CONTEXT_WINDOW (%d) должен быть >= 1", cfg.ContextWindow)
	}
	if cfg.SummaryIntervalHours < 0 {
		return fmt.Errorf("ошибка конфигурации: SUMMARY_INTERVAL_HOURS (%d) должен быть >= 0", cfg.SummaryIntervalHours)
	}

	// Валидация настроек хранилища
	switch cfg.StorageType {
	case StorageTypeFile:
		// Дополнительных проверок для файла пока нет
	case StorageTypePostgres:
		if cfg.PostgresqlHost == "" || cfg.PostgresqlUser == "" || cfg.PostgresqlDbname == "" {
			return fmt.Errorf("ошибка конфигурации: STORAGE_TYPE='postgres', но не все POSTGRESQL_* переменные установлены (HOST, USER, DBNAME)")
		}
	case StorageTypeMongo:
		if cfg.MongoDbURI == "" {
			return fmt.Errorf("ошибка конфигурации: STORAGE_TYPE='mongo', но MONGODB_URI не установлен")
		}
		if cfg.MongoDbName == "" {
			return fmt.Errorf("ошибка конфигурации: STORAGE_TYPE='mongo', но MONGODB_DBNAME не установлен")
		}
		if cfg.MongoDbMessagesCollection == "" {
			return fmt.Errorf("ошибка конфигурации: STORAGE_TYPE='mongo', но MONGODB_MESSAGES_COLLECTION не установлен")
		}
		if cfg.MongoDbUserProfilesCollection == "" {
			return fmt.Errorf("ошибка конфигурации: STORAGE_TYPE='mongo', но MONGODB_USER_PROFILES_COLLECTION не установлен")
		}
		if cfg.MongoDbSettingsCollection == "" {
			return fmt.Errorf("ошибка конфигурации: STORAGE_TYPE='mongo', но MONGODB_SETTINGS_COLLECTION не установлен")
		}
	}

	// Валидация Администраторов
	if len(cfg.AdminUsernames) == 0 {
		return fmt.Errorf("ошибка конфигурации: список ADMIN_USERNAMES не должен быть пустым")
	}

	// Валидация температуры
	if cfg.DefaultTemperature < 0.0 || cfg.DefaultTemperature > 2.0 {
		return fmt.Errorf("ошибка конфигурации: DEFAULT_TEMPERATURE (%.2f) должен быть в диапазоне [0.0, 2.0]", cfg.DefaultTemperature)
	}

	if cfg.SeriousDirectPrompt == "" {
		return fmt.Errorf("ошибка конфигурации: SERIOUS_DIRECT_PROMPT не должен быть пустым")
	}
	if cfg.DirectReplyLimitPrompt == "" {
		return fmt.Errorf("ошибка конфигурации: DIRECT_REPLY_LIMIT_PROMPT не должен быть пустым")
	}
	if cfg.PromptEnterDirectLimitCount == "" {
		return fmt.Errorf("ошибка конфигурации: PROMPT_ENTER_DIRECT_LIMIT_COUNT не должен быть пустым")
	}
	if cfg.PromptEnterDirectLimitDuration == "" {
		return fmt.Errorf("ошибка конфигурации: PROMPT_ENTER_DIRECT_LIMIT_DURATION не должен быть пустым")
	}

	// Проверка ключа Gemini, если включена долгосрочная память или транскрипция
	if cfg.LongTermMemoryEnabled || cfg.VoiceTranscriptionEnabledDefault || cfg.PhotoAnalysisEnabled {
		if cfg.GeminiAPIKey == "" {
			return fmt.Errorf("GEMINI_API_KEY должен быть установлен, так как включена долгосрочная память, транскрипция голоса или анализ фото")
		}
	}

	// --- Валидация настроек автоочистки MongoDB ---
	if cfg.MongoCleanupEnabled {
		if cfg.StorageType != StorageTypeMongo {
			return fmt.Errorf("MONGO_CLEANUP_ENABLED=true, но STORAGE_TYPE не 'mongo'")
		}
		if cfg.MongoCleanupSizeLimitMB <= 0 {
			return fmt.Errorf("MONGO_CLEANUP_SIZE_LIMIT_MB (%d) должен быть > 0", cfg.MongoCleanupSizeLimitMB)
		}
		if cfg.MongoCleanupIntervalMinutes <= 0 {
			return fmt.Errorf("MONGO_CLEANUP_INTERVAL_MINUTES (%d) должен быть > 0", cfg.MongoCleanupIntervalMinutes)
		}
		if cfg.MongoCleanupChunkDurationHours <= 0 {
			return fmt.Errorf("MONGO_CLEANUP_CHUNK_DURATION_HOURS (%d) должен быть > 0", cfg.MongoCleanupChunkDurationHours)
		}
	}
	// --- Конец валидации ---

	// --- Валидация настроек Auto Bio ---
	if cfg.AutoBioEnabled {
		if cfg.AutoBioIntervalHours <= 0 {
			return fmt.Errorf("AUTO_BIO_INTERVAL_HOURS (%d) должен быть > 0, если AutoBio включен", cfg.AutoBioIntervalHours)
		}
		if cfg.AutoBioInitialAnalysisPrompt == "" {
			return fmt.Errorf("AUTO_BIO_INITIAL_ANALYSIS_PROMPT не должен быть пустым, если AutoBio включен")
		}
		if cfg.AutoBioUpdatePrompt == "" {
			return fmt.Errorf("AUTO_BIO_UPDATE_PROMPT не должен быть пустым, если AutoBio включен")
		}
		if cfg.AutoBioMessagesLookbackDays <= 0 {
			return fmt.Errorf("AUTO_BIO_MESSAGES_LOOKBACK_DAYS (%d) должен быть > 0", cfg.AutoBioMessagesLookbackDays)
		}
		if cfg.AutoBioMinMessagesForAnalysis < 0 { // Может быть 0, если хотим анализировать даже с одним сообщением
			return fmt.Errorf("AUTO_BIO_MIN_MESSAGES_FOR_ANALYSIS (%d) должен быть >= 0", cfg.AutoBioMinMessagesForAnalysis)
		}
		if cfg.AutoBioMaxMessagesForAnalysis <= 0 {
			return fmt.Errorf("AUTO_BIO_MAX_MESSAGES_FOR_ANALYSIS (%d) должен быть > 0", cfg.AutoBioMaxMessagesForAnalysis)
		}
		// Дополнительно: Проверить, что промпты содержат нужные плейсхолдеры? (пока опционально)
		// Проверим наличие плейсхолдеров %s
		if cfg.AutoBioInitialAnalysisPrompt != "" && (!strings.Contains(cfg.AutoBioInitialAnalysisPrompt, "%s")) {
			log.Println("[WARN] AUTO_BIO_INITIAL_ANALYSIS_PROMPT не содержит плейсхолдеры %s для имени и сообщений.")
		}
		if cfg.AutoBioUpdatePrompt != "" && (!strings.Contains(cfg.AutoBioUpdatePrompt, "%s") || strings.Count(cfg.AutoBioUpdatePrompt, "%s") < 3) {
			log.Println("[WARN] AUTO_BIO_UPDATE_PROMPT не содержит плейсхолдеры %s для имени, старого био и новых сообщений.")
		}
	}
	// --- Конец валидации Auto Bio ---

	// --- Валидация настроек модерации ---
	if cfg.ModInterval <= 0 {
		return fmt.Errorf("MOD_INTERVAL (%d) должен быть > 0", cfg.ModInterval)
	}
	if cfg.ModMuteTimeMin < 0 {
		return fmt.Errorf("MOD_MUTE_TIME_MIN (%d) должен быть >= 0 (0 - навсегда)", cfg.ModMuteTimeMin)
	}
	if cfg.ModBanTimeMin < 0 {
		return fmt.Errorf("MOD_BAN_TIME_MIN (%d) должен быть >= 0 (0 - навсегда)", cfg.ModBanTimeMin)
	}
	if cfg.ModPurgeDuration <= 0 {
		return fmt.Errorf("MOD_PURGE_TIME_MIN (%s) должен быть > 0 (например '30s' или '1m')", cfg.ModPurgeDuration)
	}

	// Валидация правил
	validPunishments := map[PunishmentType]bool{
		PunishMute:  true,
		PunishKick:  true,
		PunishBan:   true,
		PunishPurge: true,
		PunishNone:  true,
		PunishEdit:  true,
	}
	for i, rule := range cfg.ModRules {
		if rule.RuleName == "" {
			log.Printf("[Config Validate WARN] Правило модерации #%d не имеет имени (rule_name).", i+1)
			// Не фатально, но лучше предупредить
		}
		if !validPunishments[rule.Punishment] {
			return fmt.Errorf("недопустимый тип наказания '%s' в правиле '%s' (№%d)", rule.Punishment, rule.RuleName, i+1)
		}
		if rule.ParsedChatID == -1 {
			log.Printf("[Config Validate WARN] Не удалось распарсить chat_id ('%s') в правиле '%s' (№%d). Правило будет применяться только если chat_id='none'.", rule.ChatID, rule.RuleName, i+1)
			// Не фатально, правило просто не будет срабатывать для конкретных чатов
		}
		if rule.ParsedUserID == -1 {
			log.Printf("[Config Validate WARN] Не удалось распарсить user_id ('%s') в правиле '%s' (№%d). Правило будет применяться только если user_id='none'.", rule.UserID, rule.RuleName, i+1)
			// Не фатально, правило просто не будет срабатывать для конкретных пользователей
		}
		if len(rule.Keywords) == 0 {
			return fmt.Errorf("список keywords не должен быть пустым в правиле '%s' (№%d). Используйте [\"Любые\"] для срабатывания без ключевых слов.", rule.RuleName, i+1)
		}
	}
	// --- Конец валидации модерации ---

	return nil
}
