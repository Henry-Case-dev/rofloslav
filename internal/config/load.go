package config

import (
	"log"
	"net/url" // Нужен для maskSecretURI
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/Henry-Case-dev/rofloslav/internal/utils"
	"github.com/joho/godotenv"
)

// markdownInstructions содержит инструкции по форматированию Markdown для LLM.
// Скопировано из llm/client.go и обновлено для стандартного Markdown.
const markdownInstructions = `\n\nИнструкции по форматированию ответа (Стандартный Markdown):\n- Используй *жирный текст* для выделения важных слов или фраз (одинарные звездочки).\n- Используй _курсив_ для акцентов или названий (одинарные подчеркивания).\n- Используй 'моноширинный текст' для кода, команд или технических терминов (одинарные кавычки).\n- НЕ используй зачеркивание (~~текст~~).\n- НЕ используй спойлеры (||текст||).\n- НЕ используй подчеркивание (__текст__).\n- Ссылки оформляй как [текст ссылки](URL).\n- Блоки кода оформляй тремя обратными кавычками:\n'''\nкод\n'''\nили\n'''go\nкод\n'''\n- Нумерованные списки начинай с \"1. \", \"2. \" и т.д.\n- Маркированные списки начинай с \"- \" или \"* \".\n- Для цитат используй \"> \".\n- Не нужно экранировать символы вроде '.', '-', '!', '(', ')', '+', '#'. Стандартный Markdown менее строгий.\n- Используй ТОЛЬКО указанный Markdown. Не используй HTML.\n`

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
	summaryPrompt := getEnvOrDefault("SUMMARY_PROMPT", "Создай краткое саммари следующего диалога:")
	rateLimitErrorMsg := getEnvOrDefault("RATE_LIMIT_ERROR_MESSAGE", "Слишком часто! Попробуйте позже.")
	timeZone := getEnvOrDefault("TIME_ZONE", "UTC")
	dailyTakeTimeStr := getEnvOrDefault("DAILY_TAKE_TIME", "19")
	minMsgStr := getEnvOrDefault("MIN_MESSAGES", "10")
	maxMsgStr := getEnvOrDefault("MAX_MESSAGES", "30")
	contextWindowStr := getEnvOrDefault("CONTEXT_WINDOW", "1000")
	debugStr := getEnvOrDefault("DEBUG", "false")

	// --- Загрузка новых переменных для настроек по умолчанию ---
	defaultConvStyle := getEnvOrDefault("DEFAULT_CONVERSATION_STYLE", "default") // default|creative|precise
	defaultTempStr := getEnvOrDefault("DEFAULT_TEMPERATURE", "0.7")              // 0.0 - 2.0
	defaultModel := getEnvOrDefault("DEFAULT_MODEL", "")                         // По умолчанию пусто - берется из провайдера
	defaultSafety := getEnvOrDefault("DEFAULT_SAFETY_THRESHOLD", "BLOCK_NONE")   // BLOCK_NONE|BLOCK_LOW|BLOCK_MEDIUM|BLOCK_HIGH

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

	// --- Загрузка переменных для донатов ---
	donatePrompt := getEnvOrDefault("DONATE_PROMPT", "Расскажи, что для продолжения работы бота требуются донаты. Сделай мотивирующий призыв с юмором пожертвовать средства разработчику. Упомяни, что платежи принимаются через https://www.donationalerts.com/r/lightnight")
	donateTimeHoursStr := getEnvOrDefault("DONATE_TIME_HOURS", "24") // По умолчанию раз в сутки
	donateTimeHours, err := strconv.Atoi(donateTimeHoursStr)
	if err != nil || donateTimeHours < 0 {
		log.Printf("Ошибка парсинга DONATE_TIME_HOURS: %v, используем 24", err)
		donateTimeHours = 24
	}

	// --- Загрузка промптов для настроек и срачей ---
	classifyDirectMessagePrompt := getEnvOrDefault("CLASSIFY_DIRECT_MESSAGE_PROMPT", "Это обращение требует серьезного ответа? Ответь только yes или no. Серьезное обращение: запрос совета, запрос информации, или человек явно расстроен.")
	seriousDirectPrompt := getEnvOrDefault("SERIOUS_DIRECT_PROMPT", "Ответь на вопрос человека, убедись что ответ содержит полезную информацию и серьезный по тону.")
	promptEnterMin := getEnvOrDefault("PROMPT_ENTER_MIN_MESSAGES", "Введите минимальное количество сообщений:")
	promptEnterMax := getEnvOrDefault("PROMPT_ENTER_MAX_MESSAGES", "Введите максимальное количество сообщений:")
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
	voiceFormatPrompt := getEnvOrDefault("VOICE_FORMAT_PROMPT", "Отформатируй следующий распознанный текст голосового сообщения, исправив пунктуацию и заглавные буквы, но сохранив оригинальный смысл и стиль:")
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
		// --- Настройки донатов ---
		DonatePrompt:    donatePrompt,
		DonateTimeHours: donateTimeHours,
		// --- Конец настроек донатов ---
		PromptEnterMinMessages:     promptEnterMin,
		PromptEnterMaxMessages:     promptEnterMax,
		PromptEnterDailyTime:       promptEnterDailyTime,
		PromptEnterSummaryInterval: promptEnterSummaryInterval,
		SRACH_WARNING_PROMPT:       srachWarningPrompt,
		SRACH_ANALYSIS_PROMPT:      srachAnalysisPrompt,
		SRACH_CONFIRM_PROMPT:       srachConfirmPrompt,
		SrachKeywords:              srachKeywordsList,
		DailyTakeTime:              dailyTakeTime,
		TimeZone:                   timeZone,
		SummaryIntervalHours:       summaryIntervalHours,
		MinMessages:                minMsg,
		MaxMessages:                maxMsg,
		ContextWindow:              contextWindow,
		Debug:                      debug,
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
		StorageType:                      StorageType(storageTypeStr),
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

	// Загрузка списка администраторов
	adminUsernamesStr = getEnvOrDefault("ADMIN_USERNAMES", "lightnight")
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
	if len(cfg.AdminUsernames) == 0 {
		cfg.AdminUsernames = []string{"lightnight"} // Гарантируем хотя бы одного админа
	}

	// --- Загрузка настроек Auto Bio ---
	cfg.AutoBioEnabled = parseBoolOrDefault(getEnvOrDefault("AUTO_BIO_ENABLED", "true"), true)
	cfg.AutoBioIntervalHours = parseIntOrDefault(getEnvOrDefault("AUTO_BIO_INTERVAL_HOURS", "6"), 6)
	cfg.AutoBioInitialAnalysisPrompt = getEnvOrDefault("AUTO_BIO_INITIAL_ANALYSIS_PROMPT", "Проанализируй следующие сообщения пользователя ([%s]). Составь краткое (1-2 предложения) резюме его стиля общения, основных тем, тона и возможных интересов, основываясь *только* на тексте сообщений. Не делай медицинских или психологических диагнозов. Сообщения:\n---\n%s")
	cfg.AutoBioUpdatePrompt = getEnvOrDefault("AUTO_BIO_UPDATE_PROMPT", "Вот текущее резюме стиля общения пользователя ([%s]):\n%s\n\nВот его *новые* сообщения с момента последнего обновления:\n---\n%s\n---\nОбнови или дополни резюме, основываясь *только* на новых сообщениях. Если стиль не изменился, просто отметь новые темы или подтверди старые наблюдения. Сохраняй краткость (1-3 предложения).")
	cfg.AutoBioMessagesLookbackDays = parseIntOrDefault(getEnvOrDefault("AUTO_BIO_MESSAGES_LOOKBACK_DAYS", "30"), 30)
	cfg.AutoBioMinMessagesForAnalysis = parseIntOrDefault(getEnvOrDefault("AUTO_BIO_MIN_MESSAGES_FOR_ANALYSIS", "10"), 10)
	cfg.AutoBioMaxMessagesForAnalysis = parseIntOrDefault(getEnvOrDefault("AUTO_BIO_MAX_MESSAGES_FOR_ANALYSIS", "2500"), 2500)
	// --- Конец загрузки настроек Auto Bio ---

	// --- Добавляем инструкции Markdown к нужным промптам ---
	if cfg.SummaryPrompt != "" {
		cfg.SummaryPrompt += markdownInstructions
		log.Println("[Config Load] Добавлены инструкции Markdown к SummaryPrompt.")
	}
	// --- Конец добавления инструкций ---

	logLoadedConfig(&cfg) // Выводим лог после загрузки всех переменных

	if err := ValidateConfig(&cfg); err != nil {
		return nil, err
	}

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

// parseBoolOrDefault возвращает значение bool по умолчанию, если переменная не установлена
func parseBoolOrDefault(value string, defaultValue bool) bool {
	if parsed, err := strconv.ParseBool(value); err == nil {
		return parsed
	}
	return defaultValue
}

// parseIntOrDefault возвращает значение int по умолчанию, если переменная не установлена
func parseIntOrDefault(value string, defaultValue int) int {
	if parsed, err := strconv.Atoi(value); err == nil {
		return parsed
	}
	return defaultValue
}
