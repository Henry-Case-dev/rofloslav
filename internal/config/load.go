package config

import (
	"fmt"
	"log"
	"net/url" // Нужен для maskSecretURI
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/Henry-Case-dev/rofloslav/internal/utils"
	"github.com/joho/godotenv"
)

// Load загружает конфигурацию из переменных окружения или использует значения по умолчанию
func Load() (*Config, error) {
	// Сначала загружаем секреты, если файл существует
	if errSecrets := godotenv.Load(".env.secrets"); errSecrets != nil {
		log.Println("Файл .env.secrets не найден, секреты будут загружены из системных переменных или .env")
	}

	// Затем загружаем основной .env файл, если он существует
	// Переменные из .env НЕ перезапишут уже загруженные из .env.secrets или системных переменных
	if errEnv := godotenv.Load(); errEnv != nil {
		log.Println("Файл .env не найден, используются системные переменные окружения")
	}

	// --- Загрузка существующих переменных ---
	telegramToken := getEnvOrDefault("TELEGRAM_TOKEN", "")
	llmProviderStr := getEnvOrDefault("LLM_PROVIDER", string(ProviderGemini)) // По умолчанию Gemini
	defaultPrompt := getEnvOrDefault("DEFAULT_PROMPT", "Ты простой бот.")
	directPrompt := getEnvOrDefault("DIRECT_PROMPT", "Ответь кратко.")
	dailyTakePrompt := getEnvOrDefault("DAILY_TAKE_PROMPT", "Какая тема дня?")
	summaryPrompt := getEnvOrDefault("SUMMARY_PROMPT", "Сделай саммари.")
	rateLimitErrorMsg := getEnvOrDefault("RATE_LIMIT_ERROR_MESSAGE", "Слишком часто! Попробуйте позже.")
	timeZone := getEnvOrDefault("TIME_ZONE", "UTC")
	dailyTakeTimeStr := getEnvOrDefault("DAILY_TAKE_TIME", "19")
	minMsgStr := getEnvOrDefault("MIN_MESSAGES", "10")
	maxMsgStr := getEnvOrDefault("MAX_MESSAGES", "30")
	contextWindowStr := getEnvOrDefault("CONTEXT_WINDOW", "1000")
	debugStr := getEnvOrDefault("DEBUG", "false")

	// --- Загрузка новых переменных для настроек по умолчанию ---
	defaultConvStyle := getEnvOrDefault("DEFAULT_CONVERSATION_STYLE", "balanced") // Например, "balanced", "creative", "precise"
	defaultTempStr := getEnvOrDefault("DEFAULT_TEMPERATURE", "0.7")
	defaultModel := getEnvOrDefault("DEFAULT_MODEL", "") // Пусто по умолчанию, будет определен ниже на основе провайдера
	defaultSafety := getEnvOrDefault("DEFAULT_SAFETY_THRESHOLD", "BLOCK_MEDIUM_AND_ABOVE")

	// --- Загрузка переменных Gemini ---
	geminiAPIKey := getEnvOrDefault("GEMINI_API_KEY", "")
	geminiModelName := getEnvOrDefault("GEMINI_MODEL_NAME", "gemini-1.5-flash-latest")

	// --- Загрузка переменных DeepSeek ---
	deepSeekAPIKey := getEnvOrDefault("DEEPSEEK_API_KEY", "")
	deepSeekModelName := getEnvOrDefault("DEEPSEEK_MODEL_NAME", "deepseek-chat")           // По умолчанию deepseek-chat
	deepSeekBaseURL := getEnvOrDefault("DEEPSEEK_BASE_URL", "https://api.deepseek.com/v1") // Стандартный URL API v1

	// --- Загрузка переменных OpenRouter ---
	openRouterAPIKey := getEnvOrDefault("OPENROUTER_API_KEY", "")
	openRouterModelName := getEnvOrDefault("OPENROUTER_MODEL_NAME", "google/gemini-flash-1.5")
	openRouterSiteURL := getEnvOrDefault("OPENROUTER_SITE_URL", "")
	openRouterSiteTitle := getEnvOrDefault("OPENROUTER_SITE_TITLE", "")

	// --- Загрузка промптов для настроек и срачей ---
	classifyDirectMessagePrompt := getEnvOrDefault("CLASSIFY_DIRECT_MESSAGE_PROMPT", "Классифицируй это сообщение: serious или casual?")
	seriousDirectPrompt := getEnvOrDefault("SERIOUS_DIRECT_PROMPT", "Ответь серьезно.")
	promptEnterMin := getEnvOrDefault("PROMPT_ENTER_MIN_MESSAGES", "Введите минимальный интервал:")
	promptEnterMax := getEnvOrDefault("PROMPT_ENTER_MAX_MESSAGES", "Введите максимальный интервал:")
	promptEnterDailyTime := getEnvOrDefault("PROMPT_ENTER_DAILY_TIME", "Введите час для темы дня (0-23):")
	promptEnterSummaryInterval := getEnvOrDefault("PROMPT_ENTER_SUMMARY_INTERVAL", "Введите интервал авто-саммари (в часах, 0=выкл):")
	summaryIntervalStr := getEnvOrDefault("SUMMARY_INTERVAL_HOURS", "2") // По умолчанию 2 часа
	srachWarningPrompt := getEnvOrDefault("SRACH_WARNING_PROMPT", "Внимание, срач!")
	srachAnalysisPrompt := getEnvOrDefault("SRACH_ANALYSIS_PROMPT", "Анализирую срач...")
	srachConfirmPrompt := getEnvOrDefault("SRACH_CONFIRM_PROMPT", "Это сообщение - часть срача? Ответь true или false:")
	srachKeywordsRaw := getEnvOrDefault("SRACH_KEYWORDS", "")
	srachEnabledStr := getEnvOrDefault("SRACH_ANALYSIS_ENABLED", "true") // Загружаем новую переменную

	// --- Загрузка переменных PostgreSQL ---
	dbHost := getEnvOrDefault("POSTGRESQL_HOST", "")         // Используем POSTGRESQL_
	dbPort := getEnvOrDefault("POSTGRESQL_PORT", "5432")     // Используем POSTGRESQL_
	dbUser := getEnvOrDefault("POSTGRESQL_USER", "")         // Используем POSTGRESQL_
	dbPassword := getEnvOrDefault("POSTGRESQL_PASSWORD", "") // Используем POSTGRESQL_
	dbName := getEnvOrDefault("POSTGRESQL_DBNAME", "")       // Используем POSTGRESQL_DBNAME

	// --- Загрузка переменных MongoDB ---
	mongoURI := getEnvOrDefault("MONGODB_URI", "")
	mongoDbName := getEnvOrDefault("MONGODB_DBNAME", "rofloslav_history")
	mongoMessagesCollection := getEnvOrDefault("MONGODB_MESSAGES_COLLECTION", "chat_messages")
	mongoUserProfilesCollection := getEnvOrDefault("MONGODB_USER_PROFILES_COLLECTION", "user_profiles")
	mongoSettingsCollection := getEnvOrDefault("MONGODB_SETTINGS_COLLECTION", "chat_settings") // Новая переменная

	// --- Загрузка прочих переменных ---
	storageTypeStr := strings.ToLower(getEnvOrDefault("STORAGE_TYPE", string(StorageTypeMongo))) // По умолчанию Mongo
	adminUsernamesStr := getEnvOrDefault("ADMIN_USERNAMES", "lightnight")                        // По умолчанию lightnight
	welcomePrompt := getEnvOrDefault("WELCOME_PROMPT", "Привет, чат! Я Рофлослав, ваш новый повелитель сарказма. Погнали нахуй.")
	voiceFormatPrompt := getEnvOrDefault("VOICE_FORMAT_PROMPT", "Format the following recognized text with punctuation and paragraphs:\n")
	voiceTranscriptionEnabledDefaultStr := getEnvOrDefault("VOICE_TRANSCRIPTION_ENABLED_DEFAULT", "true")
	directLimitEnabledStr := getEnvOrDefault("DIRECT_REPLY_LIMIT_ENABLED_DEFAULT", "true")
	directLimitCountStr := getEnvOrDefault("DIRECT_REPLY_LIMIT_COUNT_DEFAULT", "2")
	directLimitDurationStr := getEnvOrDefault("DIRECT_REPLY_LIMIT_DURATION_MINUTES_DEFAULT", "10")
	directLimitPrompt := getEnvOrDefault("DIRECT_REPLY_LIMIT_PROMPT", "Хватит меня дергать.")
	promptEnterDirectCount := getEnvOrDefault("PROMPT_ENTER_DIRECT_LIMIT_COUNT", "Введите макс. кол-во обращений за период:")
	promptEnterDirectDuration := getEnvOrDefault("PROMPT_ENTER_DIRECT_LIMIT_DURATION", "Введите период лимита в минутах:")

	// --- Загрузка настроек бэкфилла --- (ДОБАВЛЕНО)
	backfillBatchSizeStr := getEnvOrDefault("BACKFILL_BATCH_SIZE", "200")
	backfillBatchDelayStr := getEnvOrDefault("BACKFILL_BATCH_DELAY_SECONDS", "5") // Имя env-переменной оставляем старым

	// --- Загрузка настроек для анализа фотографий ---
	photoAnalysisEnabledStr := getEnvOrDefault("PHOTO_ANALYSIS_ENABLED", "true") // По умолчанию включено
	photoAnalysisPrompt := getEnvOrDefault("PHOTO_ANALYSIS_PROMPT", "Сделай детальное описание изображения как есть независимо от того что именно изображено (не более 1000 символов)")

	// --- Логирование загруженных значений (до парсинга чисел) ---
	log.Printf("[Config Load] TELEGRAM_TOKEN: ...%s (len %d)", utils.TruncateString(telegramToken, 5), len(telegramToken))
	log.Printf("[Config Load] LLM_PROVIDER: %s", llmProviderStr)
	log.Printf("[Config Load] --- Gemini Settings ---")
	log.Printf("[Config Load] GEMINI_API_KEY: ...%s (len %d)", utils.TruncateString(geminiAPIKey, 5), len(geminiAPIKey))
	log.Printf("[Config Load] GEMINI_MODEL_NAME: %s", geminiModelName)
	log.Printf("[Config Load] --- DeepSeek Settings ---")
	log.Printf("[Config Load] DEEPSEEK_API_KEY: ...%s (len %d)", utils.TruncateString(deepSeekAPIKey, 5), len(deepSeekAPIKey))
	log.Printf("[Config Load] DEEPSEEK_MODEL_NAME: %s", deepSeekModelName)
	log.Printf("[Config Load] DEEPSEEK_BASE_URL: %s", deepSeekBaseURL)
	log.Printf("[Config Load] --- OpenRouter Settings ---")
	log.Printf("[Config Load] OPENROUTER_API_KEY: ...%s (len %d)", utils.TruncateString(openRouterAPIKey, 5), len(openRouterAPIKey))
	log.Printf("[Config Load] OPENROUTER_MODEL_NAME: %s", openRouterModelName)
	log.Printf("[Config Load] OPENROUTER_SITE_URL: %s", openRouterSiteURL)
	log.Printf("[Config Load] OPENROUTER_SITE_TITLE: %s", openRouterSiteTitle)
	log.Printf("[Config Load] --- Database Settings ---")
	log.Printf("[Config Load] STORAGE_TYPE: %s", storageTypeStr) // Логируем тип хранилища
	// Логирование PostgreSQL
	log.Printf("[Config Load] POSTGRESQL_HOST: %s", dbHost)
	log.Printf("[Config Load] POSTGRESQL_PORT: %s", dbPort)
	log.Printf("[Config Load] POSTGRESQL_USER: %s", dbUser)
	log.Printf("[Config Load] POSTGRESQL_PASSWORD: ...%s (len %d)", utils.TruncateString(dbPassword, 3), len(dbPassword))
	log.Printf("[Config Load] POSTGRESQL_DBNAME: %s", dbName)
	// Логирование MongoDB
	log.Printf("[Config Load] MONGODB_URI: %s", maskSecretURI(mongoURI))
	log.Printf("[Config Load] MONGODB_DBNAME: %s", mongoDbName)
	log.Printf("[Config Load] MONGODB_MESSAGES_COLLECTION: %s", mongoMessagesCollection)
	log.Printf("[Config Load] MONGODB_USER_PROFILES_COLLECTION: %s", mongoUserProfilesCollection)
	log.Printf("[Config Load] MONGODB_SETTINGS_COLLECTION: %s", mongoSettingsCollection) // Логируем новую коллекцию
	log.Printf("[Config Load] --- Prompts & Behavior ---")
	log.Printf("[Config Load] DEFAULT_PROMPT: %s...", utils.TruncateString(defaultPrompt, 50))
	log.Printf("[Config Load] DIRECT_PROMPT: %s...", utils.TruncateString(directPrompt, 50))
	log.Printf("[Config Load] DAILY_TAKE_PROMPT: %s...", utils.TruncateString(dailyTakePrompt, 50))
	log.Printf("[Config Load] SUMMARY_PROMPT: %s...", utils.TruncateString(summaryPrompt, 50))
	log.Printf("[Config Load] RATE_LIMIT_ERROR_MESSAGE: %s...", utils.TruncateString(rateLimitErrorMsg, 50))
	log.Printf("[Config Load] SRACH_WARNING_PROMPT: %s...", utils.TruncateString(srachWarningPrompt, 50))
	log.Printf("[Config Load] SRACH_ANALYSIS_PROMPT: %s...", utils.TruncateString(srachAnalysisPrompt, 50))
	log.Printf("[Config Load] SRACH_CONFIRM_PROMPT: %s...", utils.TruncateString(srachConfirmPrompt, 50))
	// --- Логирование новых дефолтных настроек ---
	log.Printf("[Config Load] DEFAULT_CONVERSATION_STYLE: %s", defaultConvStyle)
	log.Printf("[Config Load] DEFAULT_TEMPERATURE: %s", defaultTempStr)
	log.Printf("[Config Load] DEFAULT_MODEL (pre-provider): %s", defaultModel)
	log.Printf("[Config Load] DEFAULT_SAFETY_THRESHOLD: %s", defaultSafety)
	// --- Конец логирования дефолтных настроек ---
	log.Printf("[Config Load] --- Timing & Limits ---")
	log.Printf("[Config Load] TIME_ZONE: %s", timeZone)
	log.Printf("[Config Load] DEBUG: %s", debugStr)
	log.Printf("[Config Load] WELCOME_PROMPT: %s...", utils.TruncateString(welcomePrompt, 50))
	log.Printf("[Config Load] VOICE_FORMAT_PROMPT: %s...", utils.TruncateString(voiceFormatPrompt, 50))
	log.Printf("[Config Load] VOICE_TRANSCRIPTION_ENABLED_DEFAULT: %s", voiceTranscriptionEnabledDefaultStr)
	log.Printf("[Config Load] CLASSIFY_DIRECT_MESSAGE_PROMPT: %s...", utils.TruncateString(classifyDirectMessagePrompt, 50))
	log.Printf("[Config Load] SERIOUS_DIRECT_PROMPT: %s...", utils.TruncateString(seriousDirectPrompt, 50))
	log.Printf("[Config Load] --- Direct Reply Limit Settings ---")
	log.Printf("[Config Load] DIRECT_REPLY_LIMIT_ENABLED_DEFAULT: %s", directLimitEnabledStr)
	log.Printf("[Config Load] DIRECT_REPLY_LIMIT_COUNT_DEFAULT: %s", directLimitCountStr)
	log.Printf("[Config Load] DIRECT_REPLY_LIMIT_DURATION_MINUTES_DEFAULT: %s", directLimitDurationStr)
	log.Printf("[Config Load] DIRECT_REPLY_LIMIT_PROMPT: %s...", utils.TruncateString(directLimitPrompt, 50))
	log.Printf("[Config Load] PROMPT_ENTER_DIRECT_LIMIT_COUNT: %s...", utils.TruncateString(promptEnterDirectCount, 50))
	log.Printf("[Config Load] PROMPT_ENTER_DIRECT_LIMIT_DURATION: %s...", utils.TruncateString(promptEnterDirectDuration, 50))
	// --- Лог настроек анализа фотографий ---
	log.Printf("[Config Load] PHOTO_ANALYSIS_ENABLED: %s", photoAnalysisEnabledStr)
	log.Printf("[Config Load] PHOTO_ANALYSIS_PROMPT: %s...", utils.TruncateString(photoAnalysisPrompt, 50))
	// --- Конец логирования ---

	// --- Валидация LLM Provider ---
	var llmProvider LLMProvider
	switch strings.ToLower(llmProviderStr) {
	case string(ProviderGemini):
		llmProvider = ProviderGemini
		if defaultModel == "" {
			defaultModel = geminiModelName // Используем модель Gemini по умолчанию
		}
	case string(ProviderDeepSeek):
		llmProvider = ProviderDeepSeek
		if defaultModel == "" {
			defaultModel = deepSeekModelName // Используем модель DeepSeek по умолчанию
		}
	case string(ProviderOpenRouter):
		llmProvider = ProviderOpenRouter
		if defaultModel == "" {
			defaultModel = openRouterModelName // Используем модель OpenRouter по умолчанию
		}
	default:
		log.Printf("[Config Load WARN] Неизвестный LLM_PROVIDER '%s'. Используется '%s'.", llmProviderStr, ProviderGemini)
		llmProvider = ProviderGemini
		if defaultModel == "" {
			defaultModel = geminiModelName // Используем модель Gemini по умолчанию
		}
	}
	log.Printf("[Config Load] DEFAULT_MODEL (post-provider): %s", defaultModel) // Логируем итоговую дефолтную модель

	// --- Парсинг ключевых слов ---
	var srachKeywordsList []string
	if srachKeywordsRaw != "" {
		keywords := strings.Split(srachKeywordsRaw, ",")
		for _, kw := range keywords {
			trimmedKw := strings.TrimSpace(kw)
			if trimmedKw != "" {
				srachKeywordsList = append(srachKeywordsList, strings.ToLower(trimmedKw))
			}
		}
	}
	log.Printf("Загружено %d ключевых слов для детекции срачей.", len(srachKeywordsList))

	// --- Парсинг числовых значений ---
	dailyTakeTime, err := strconv.Atoi(dailyTakeTimeStr)
	if err != nil {
		log.Printf("Ошибка парсинга DAILY_TAKE_TIME: %v, используем 19", err)
		dailyTakeTime = 19
	}
	minMsg, err := strconv.Atoi(minMsgStr)
	if err != nil {
		log.Printf("Ошибка парсинга MIN_MESSAGES: %v, используем 10", err)
		minMsg = 10
	}
	maxMsg, err := strconv.Atoi(maxMsgStr)
	if err != nil {
		log.Printf("Ошибка парсинга MAX_MESSAGES: %v, используем 30", err)
		maxMsg = 30
	}
	contextWindow, err := strconv.Atoi(contextWindowStr)
	if err != nil {
		log.Printf("Ошибка парсинга CONTEXT_WINDOW: %v, используем 1000", err)
		contextWindow = 1000
	}
	summaryIntervalHours, err := strconv.Atoi(summaryIntervalStr)
	if err != nil {
		log.Printf("Ошибка парсинга SUMMARY_INTERVAL_HOURS: %v, используем 2", err)
		summaryIntervalHours = 2
	}
	if summaryIntervalHours < 0 {
		log.Printf("Интервал саммари не может быть отрицательным, используем 2")
		summaryIntervalHours = 2
	}
	// Парсинг новой переменной - температуры
	defaultTemp, err := strconv.ParseFloat(defaultTempStr, 64)
	if err != nil {
		log.Printf("Ошибка парсинга DEFAULT_TEMPERATURE: %v, используем 0.7", err)
		defaultTemp = 0.7
	} else if defaultTemp < 0.0 || defaultTemp > 2.0 {
		log.Printf("DEFAULT_TEMPERATURE (%f) вне диапазона [0.0, 2.0], используем 0.7", defaultTemp)
		defaultTemp = 0.7
	}

	debug := debugStr == "true" || debugStr == "1" || debugStr == "yes"

	// --- Парсинг числовых значений для лимита прямых обращений ---
	directLimitCount, err := strconv.Atoi(directLimitCountStr)
	if err != nil || directLimitCount <= 0 {
		log.Printf("Ошибка парсинга DIRECT_REPLY_LIMIT_COUNT_DEFAULT ('%s') или значение <= 0: %v, используем 2", directLimitCountStr, err)
		directLimitCount = 2
	}
	directLimitDurationStr = getEnvOrDefault("DIRECT_REPLY_LIMIT_DURATION_MINUTES_DEFAULT", "10")
	directLimitDurationMinutes, err := strconv.Atoi(directLimitDurationStr)
	if err != nil || directLimitDurationMinutes <= 0 {
		log.Printf("Ошибка парсинга DIRECT_REPLY_LIMIT_DURATION_MINUTES_DEFAULT ('%s') или значение <= 0: %v, используем 10", directLimitDurationStr, err)
		directLimitDurationMinutes = 10
	}
	directReplyLimitDurationDefault := time.Duration(directLimitDurationMinutes) * time.Minute

	// Загружаем настройки лимита прямых обращений
	directReplyLimitEnabledDefault, _ := strconv.ParseBool(getEnvOrDefault("DIRECT_REPLY_LIMIT_ENABLED_DEFAULT", "true"))
	directReplyLimitCountDefault, _ := strconv.Atoi(getEnvOrDefault("DIRECT_REPLY_LIMIT_COUNT_DEFAULT", "3"))
	directReplyLimitPrompt := getEnvOrDefault("DIRECT_REPLY_LIMIT_PROMPT", "Слишком часто обращаешься, отдохни.")
	promptEnterDirectLimitCount := getEnvOrDefault("PROMPT_ENTER_DIRECT_LIMIT_COUNT", "Введите макс. кол-во обращений за период:")
	promptEnterDirectLimitDuration := getEnvOrDefault("PROMPT_ENTER_DIRECT_LIMIT_DURATION", "Введите период лимита в минутах:")

	// Загружаем настройки долгосрочной памяти
	longTermMemoryEnabledStr := getEnvOrDefault("LONG_TERM_MEMORY_ENABLED", "false")
	longTermMemoryEnabled := longTermMemoryEnabledStr == "true" || longTermMemoryEnabledStr == "1" || longTermMemoryEnabledStr == "yes"
	geminiEmbeddingModelName := getEnvOrDefault("GEMINI_EMBEDDING_MODEL_NAME", "embedding-001")
	mongoVectorIndexName := getEnvOrDefault("MONGO_VECTOR_INDEX_NAME", "vector_index_messages")
	longTermMemoryFetchKStr := getEnvOrDefault("LONG_TERM_MEMORY_FETCH_K", "3")
	longTermMemoryFetchK, _ := strconv.Atoi(longTermMemoryFetchKStr)

	// --- Преобразование строковых значений в нужные типы ---
	backfillBatchSize, err := strconv.Atoi(backfillBatchSizeStr)
	if err != nil {
		log.Printf("Ошибка парсинга BACKFILL_BATCH_SIZE: %v, используем 200", err)
		backfillBatchSize = 200
	}
	backfillBatchDelayInt, err := strconv.Atoi(backfillBatchDelayStr)
	if err != nil {
		log.Printf("Ошибка парсинга BACKFILL_BATCH_DELAY_SECONDS: %v, используем 5", err)
		backfillBatchDelayInt = 5
	}
	backfillBatchDelay := time.Duration(backfillBatchDelayInt) * time.Second

	// --- Загрузка настроек автоочистки MongoDB ---
	mongoCleanupEnabledStr := getEnvOrDefault("MONGO_CLEANUP_ENABLED", "false")
	mongoCleanupEnabled, err := strconv.ParseBool(mongoCleanupEnabledStr)
	if err != nil {
		log.Printf("Предупреждение: Неверное значение для MONGO_CLEANUP_ENABLED ('%s'), используется false: %v", mongoCleanupEnabledStr, err)
		mongoCleanupEnabled = false
	}

	mongoCleanupSizeLimitMBStr := getEnvOrDefault("MONGO_CLEANUP_SIZE_LIMIT_MB", "500")
	mongoCleanupSizeLimitMB, err := strconv.Atoi(mongoCleanupSizeLimitMBStr)
	if err != nil || mongoCleanupSizeLimitMB <= 0 {
		log.Printf("Предупреждение: Неверное значение для MONGO_CLEANUP_SIZE_LIMIT_MB ('%s'), используется 500: %v", mongoCleanupSizeLimitMBStr, err)
	}

	mongoCleanupIntervalMinutesStr := getEnvOrDefault("MONGO_CLEANUP_INTERVAL_MINUTES", "60")
	mongoCleanupIntervalMinutes, err := strconv.Atoi(mongoCleanupIntervalMinutesStr)
	if err != nil || mongoCleanupIntervalMinutes <= 0 {
		log.Printf("Предупреждение: Неверное значение для MONGO_CLEANUP_INTERVAL_MINUTES ('%s'), используется 60: %v", mongoCleanupIntervalMinutesStr, err)
	}

	mongoCleanupChunkDurationHoursStr := getEnvOrDefault("MONGO_CLEANUP_CHUNK_DURATION_HOURS", "24")
	mongoCleanupChunkDurationHours, err := strconv.Atoi(mongoCleanupChunkDurationHoursStr)
	if err != nil || mongoCleanupChunkDurationHours <= 0 {
		log.Printf("Предупреждение: Неверное значение для MONGO_CLEANUP_CHUNK_DURATION_HOURS ('%s'), используется 24: %v", mongoCleanupChunkDurationHoursStr, err)
	}

	// --- Конец загрузки настроек автоочистки MongoDB ---

	cfg := Config{
		TelegramToken: telegramToken,
		LLMProvider:   llmProvider,
		// --- Новые поля ---
		DefaultConversationStyle: defaultConvStyle,
		DefaultTemperature:       defaultTemp,
		DefaultModel:             defaultModel,
		DefaultSafetyThreshold:   defaultSafety,
		// --- Конец новых полей ---
		GeminiAPIKey:                geminiAPIKey,
		GeminiModelName:             geminiModelName,
		DeepSeekAPIKey:              deepSeekAPIKey,
		DeepSeekModelName:           deepSeekModelName,
		DeepSeekBaseURL:             deepSeekBaseURL,
		DefaultPrompt:               defaultPrompt,
		DirectPrompt:                directPrompt,
		ClassifyDirectMessagePrompt: classifyDirectMessagePrompt,
		SeriousDirectPrompt:         seriousDirectPrompt,
		DailyTakePrompt:             dailyTakePrompt,
		SummaryPrompt:               summaryPrompt,
		RateLimitStaticText:         getEnvOrDefault("RATE_LIMIT_STATIC_TEXT", "Слишком часто! Попробуйте позже."),
		RateLimitPrompt:             getEnvOrDefault("RATE_LIMIT_PROMPT", "Скажи пользователю, что он слишком часто нажимает кнопку."),
		PromptEnterMinMessages:      promptEnterMin,
		PromptEnterMaxMessages:      promptEnterMax,
		PromptEnterDailyTime:        promptEnterDailyTime,
		PromptEnterSummaryInterval:  promptEnterSummaryInterval,
		SRACH_WARNING_PROMPT:        srachWarningPrompt,
		SRACH_ANALYSIS_PROMPT:       srachAnalysisPrompt,
		SRACH_CONFIRM_PROMPT:        srachConfirmPrompt,
		SrachKeywords:               srachKeywordsList,
		DailyTakeTime:               dailyTakeTime,
		TimeZone:                    timeZone,
		SummaryIntervalHours:        summaryIntervalHours,
		MinMessages:                 minMsg,
		MaxMessages:                 maxMsg,
		ContextWindow:               contextWindow,
		Debug:                       debug,
		// Заполняем новые поля для БД с префиксом Postgresql
		PostgresqlHost:     dbHost,
		PostgresqlPort:     dbPort,
		PostgresqlUser:     dbUser,
		PostgresqlPassword: dbPassword,
		PostgresqlDbname:   dbName,
		// Заполняем поля MongoDB
		MongoDbURI:                       mongoURI,
		MongoDbName:                      mongoDbName,
		MongoDbMessagesCollection:        mongoMessagesCollection,
		MongoDbUserProfilesCollection:    mongoUserProfilesCollection,
		MongoDbSettingsCollection:        mongoSettingsCollection, // Новое поле
		SrachAnalysisEnabled:             srachEnabledStr == "true" || srachEnabledStr == "1" || srachEnabledStr == "yes",
		WelcomePrompt:                    welcomePrompt,
		VoiceFormatPrompt:                voiceFormatPrompt,
		VoiceTranscriptionEnabledDefault: voiceTranscriptionEnabledDefaultStr == "true" || voiceTranscriptionEnabledDefaultStr == "1" || voiceTranscriptionEnabledDefaultStr == "yes",
		// --- Настройки лимита прямых обращений (дефолтные) ---
		DirectReplyLimitEnabledDefault:  directReplyLimitEnabledDefault,
		DirectReplyLimitCountDefault:    directReplyLimitCountDefault,
		DirectReplyLimitDurationDefault: directReplyLimitDurationDefault,
		DirectReplyLimitPrompt:          directReplyLimitPrompt,
		PromptEnterDirectLimitCount:     promptEnterDirectLimitCount,
		PromptEnterDirectLimitDuration:  promptEnterDirectLimitDuration,
		// --- Настройки долгосрочной памяти ---
		LongTermMemoryEnabled:    longTermMemoryEnabled,
		GeminiEmbeddingModelName: geminiEmbeddingModelName,
		MongoVectorIndexName:     mongoVectorIndexName,
		LongTermMemoryFetchK:     longTermMemoryFetchK,
		// --- Настройки бэкфилла эмбеддингов ---
		BackfillBatchSize:  backfillBatchSize,
		BackfillBatchDelay: backfillBatchDelay,
		// --- Новые поля для OpenRouter ---
		OpenRouterAPIKey:    openRouterAPIKey,
		OpenRouterModelName: openRouterModelName,
		OpenRouterSiteURL:   openRouterSiteURL,
		OpenRouterSiteTitle: openRouterSiteTitle,
		// --- Новые поля для анализа фотографий ---
		PhotoAnalysisEnabled: photoAnalysisEnabledStr == "true" || photoAnalysisEnabledStr == "1" || photoAnalysisEnabledStr == "yes",
		PhotoAnalysisPrompt:  photoAnalysisPrompt,
		// --- Инициализация полей автоочистки MongoDB значениями из парсинга (могут быть 0) ---
		MongoCleanupEnabled:            mongoCleanupEnabled,
		MongoCleanupSizeLimitMB:        mongoCleanupSizeLimitMB,        // Может быть 0 или отрицательным, если парсинг не удался
		MongoCleanupIntervalMinutes:    mongoCleanupIntervalMinutes,    // Может быть 0 или отрицательным, если парсинг не удался
		MongoCleanupChunkDurationHours: mongoCleanupChunkDurationHours, // Может быть 0 или отрицательным, если парсинг не удался
	}

	// --- Установка значений по умолчанию для MongoDB cleanup, если они не были установлены или невалидны ---
	if cfg.MongoCleanupSizeLimitMB <= 0 {
		cfg.MongoCleanupSizeLimitMB = 500 // Дефолтное значение
		log.Println("[Config Load] MONGO_CLEANUP_SIZE_LIMIT_MB не установлен или невалиден, используется значение по умолчанию: 500")
	}
	if cfg.MongoCleanupIntervalMinutes <= 0 {
		cfg.MongoCleanupIntervalMinutes = 60 // Дефолтное значение
		log.Println("[Config Load] MONGO_CLEANUP_INTERVAL_MINUTES не установлен или невалиден, используется значение по умолчанию: 60")
	}
	if cfg.MongoCleanupChunkDurationHours <= 0 {
		cfg.MongoCleanupChunkDurationHours = 24 // Дефолтное значение
		log.Println("[Config Load] MONGO_CLEANUP_CHUNK_DURATION_HOURS не установлен или невалиден, используется значение по умолчанию: 24")
	}

	// Валидация и установка StorageType
	switch StorageType(storageTypeStr) {
	case StorageTypeFile:
		cfg.StorageType = StorageTypeFile
	case StorageTypePostgres:
		cfg.StorageType = StorageTypePostgres
	case StorageTypeMongo:
		cfg.StorageType = StorageTypeMongo
	default:
		log.Printf("Предупреждение: Неизвестный STORAGE_TYPE '%s'. Используется MongoDB по умолчанию.", storageTypeStr)
		cfg.StorageType = StorageTypeMongo // Устанавливаем значение по умолчанию
	}

	// Валидация LLM провайдера и ключей
	// llmProviderStr = strings.ToLower(string(cfg.LLMProvider)) // Преобразуем к строке для switch
	// Используем уже определенную переменную llmProvider
	switch cfg.LLMProvider {
	case ProviderGemini:
		// cfg.LLMProvider = ProviderGemini // Уже установлено
		if cfg.GeminiAPIKey == "" {
			return nil, fmt.Errorf("ошибка конфигурации: LLM_PROVIDER='gemini', но GEMINI_API_KEY не установлен")
		}
		// Установка DefaultModel уже произошла выше, если он был пуст
	case ProviderDeepSeek:
		// cfg.LLMProvider = ProviderDeepSeek // Уже установлено
		if cfg.DeepSeekAPIKey == "" {
			return nil, fmt.Errorf("ошибка конфигурации: LLM_PROVIDER='deepseek', но DEEPSEEK_API_KEY не установлен")
		}
		// Установка DefaultModel уже произошла выше, если он был пуст
	case ProviderOpenRouter:
		if cfg.OpenRouterAPIKey == "" {
			return nil, fmt.Errorf("ошибка конфигурации: LLM_PROVIDER='openrouter', но OPENROUTER_API_KEY не установлен")
		}
		// Установка DefaultModel уже произошла выше, если он был пуст
	default: // Этот кейс не должен достигаться из-за валидации выше, но на всякий случай
		log.Printf("[Config Load CRITICAL] Неожиданное значение LLMProvider '%s'.", cfg.LLMProvider)
		// Возвращаем ошибку, т.к. состояние неопределенное
		return nil, fmt.Errorf("неожиданная ошибка конфигурации LLM провайдера")
	}

	// Валидация интервалов
	if dailyTakeTime < 0 || dailyTakeTime > 23 {
		log.Printf("Интервал для темы дня должен быть в диапазоне 0-23, используем 19")
		cfg.DailyTakeTime = 19 // Обновляем значение в cfg
	}
	if minMsg < 1 || minMsg > 100 {
		log.Printf("Минимальное количество сообщений должно быть в диапазоне 1-100, используем 10")
		cfg.MinMessages = 10 // Обновляем значение в cfg
	}
	if maxMsg < 1 || maxMsg > 100 {
		log.Printf("Максимальное количество сообщений должно быть в диапазоне 1-100, используем 30")
		cfg.MaxMessages = 30 // Обновляем значение в cfg
	}
	if contextWindow < 1 { // Убираем верхний предел для окна контекста, т.к. Gemini может больше
		log.Printf("Контекстное окно должно быть > 0, используем 1000")
		cfg.ContextWindow = 1000 // Обновляем значение в cfg
	}
	if summaryIntervalHours < 0 || summaryIntervalHours > 24 {
		log.Printf("Интервал авто-саммари должен быть в диапазоне 0-24, используем 2")
		cfg.SummaryIntervalHours = 2 // Обновляем значение в cfg
	}

	// Валидация настроек хранилища
	switch cfg.StorageType {
	case StorageTypePostgres:
		if cfg.PostgresqlHost == "" || cfg.PostgresqlUser == "" || cfg.PostgresqlDbname == "" {
			return nil, fmt.Errorf("ошибка конфигурации: STORAGE_TYPE='postgres', но не все POSTGRESQL_* переменные установлены (HOST, USER, DBNAME)")
		}
	case StorageTypeMongo:
		if cfg.MongoDbURI == "" {
			return nil, fmt.Errorf("ошибка конфигурации: STORAGE_TYPE='mongo', но MONGODB_URI не установлен")
		}
		if cfg.MongoDbName == "" {
			return nil, fmt.Errorf("ошибка конфигурации: STORAGE_TYPE='mongo', но MONGODB_DBNAME не установлен")
		}
		if cfg.MongoDbMessagesCollection == "" {
			return nil, fmt.Errorf("ошибка конфигурации: STORAGE_TYPE='mongo', но MONGODB_MESSAGES_COLLECTION не установлен")
		}
		if cfg.MongoDbUserProfilesCollection == "" {
			return nil, fmt.Errorf("ошибка конфигурации: STORAGE_TYPE='mongo', но MONGODB_USER_PROFILES_COLLECTION не установлен")
		}
		if cfg.MongoDbSettingsCollection == "" { // Проверяем новую коллекцию
			return nil, fmt.Errorf("ошибка конфигурации: STORAGE_TYPE='mongo', но MONGODB_SETTINGS_COLLECTION не установлен")
		}
	}

	// Администраторы
	cfg.AdminUsernames = strings.Split(adminUsernamesStr, ",")
	// Очистка пробелов и пустых строк
	cleanedAdmins := make([]string, 0, len(cfg.AdminUsernames))
	for _, admin := range cfg.AdminUsernames {
		trimmedAdmin := strings.TrimSpace(admin)
		if trimmedAdmin != "" {
			cleanedAdmins = append(cleanedAdmins, trimmedAdmin)
		}
	}
	cfg.AdminUsernames = cleanedAdmins
	// Убедимся, что хотя бы один админ есть (lightnight по умолчанию)
	if len(cfg.AdminUsernames) == 0 {
		cfg.AdminUsernames = []string{"lightnight"}
	}

	// Логирование загруженной конфигурации (без секретов)
	logLoadedConfig(&cfg)

	return &cfg, nil
}

// getEnvOrDefault читает переменную окружения или возвращает значение по умолчанию
func getEnvOrDefault(key, defaultValue string) string {
	if value, exists := os.LookupEnv(key); exists {
		return value
	}
	return defaultValue
}

// maskSecret заменяет большую часть строки звездочками для безопасного логирования
func maskSecret(s string) string {
	if len(s) < 4 {
		return "****"
	}
	visiblePart := 2 // Сколько символов оставить видимыми с каждого конца
	if len(s) < visiblePart*2 {
		visiblePart = 1
	}
	if len(s) < visiblePart*2 {
		return "****"
	}
	return s[:visiblePart] + "****" + s[len(s)-visiblePart:]
}

// maskSecretURI маскирует пароль в URI
func maskSecretURI(uri string) string {
	u, err := url.Parse(uri)
	if err != nil {
		return maskSecret(uri) // Если парсинг не удался, маскируем как обычную строку
	}
	if u.User != nil {
		username := u.User.Username()
		// Пароль маскируем полностью
		u.User = url.UserPassword(username, "********")
		return u.String()
	}
	return uri // Возвращаем как есть, если нет UserInfo
}
