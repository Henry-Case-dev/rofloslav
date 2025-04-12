package config

import (
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"

	"github.com/joho/godotenv"
)

// LLMProvider определяет тип используемого LLM провайдера
type LLMProvider string

const (
	ProviderGemini   LLMProvider = "gemini"
	ProviderDeepSeek LLMProvider = "deepseek"
)

// StorageType определяет тип используемого хранилища
type StorageType string

const (
	StorageTypeFile     StorageType = "file"
	StorageTypePostgres StorageType = "postgres"
	StorageTypeMongo    StorageType = "mongo"
)

// Config содержит все параметры конфигурации бота
type Config struct {
	TelegramToken string
	// Общие настройки LLM
	LLMProvider     LLMProvider
	DefaultPrompt   string
	DirectPrompt    string
	DailyTakePrompt string
	SummaryPrompt   string
	// --- Новые поля для настроек по умолчанию ---
	DefaultConversationStyle string  // Стиль общения по умолчанию
	DefaultTemperature       float64 // Температура по умолчанию
	DefaultModel             string  // Модель LLM по умолчанию
	DefaultSafetyThreshold   string  // Уровень безопасности Gemini по умолчанию
	// --- Конец новых полей ---
	// Настройки Gemini
	GeminiAPIKey    string
	GeminiModelName string
	// Настройки DeepSeek
	DeepSeekAPIKey    string
	DeepSeekModelName string
	DeepSeekBaseURL   string // Опционально, для кастомного URL
	// Настройки поведения бота
	RateLimitStaticText string // Статический текст для сообщения о лимите
	RateLimitPrompt     string // Промпт для LLM для сообщения о лимите
	// Промпты для ввода настроек
	PromptEnterMinMessages     string
	PromptEnterMaxMessages     string
	PromptEnterDailyTime       string
	PromptEnterSummaryInterval string
	// Промпты для анализа срачей
	SRACH_WARNING_PROMPT  string
	SRACH_ANALYSIS_PROMPT string
	SRACH_CONFIRM_PROMPT  string
	SrachKeywords         []string
	SrachAnalysisEnabled  bool // Значение по умолчанию из .env
	// Настройки времени и интервалов
	DailyTakeTime        int
	TimeZone             string
	SummaryIntervalHours int
	MinMessages          int
	MaxMessages          int
	ContextWindow        int
	Debug                bool
	// Настройки базы данных PostgreSQL - ИСПОЛЬЗУЕМ ПРЕФИКС POSTGRESQL_
	PostgresqlHost     string
	PostgresqlPort     string
	PostgresqlUser     string
	PostgresqlPassword string
	PostgresqlDbname   string
	// Настройки MongoDB
	MongoDbURI                    string // Строка подключения MongoDB
	MongoDbName                   string // Имя базы данных MongoDB
	MongoDbMessagesCollection     string // Имя коллекции для сообщений MongoDB
	MongoDbUserProfilesCollection string // Имя коллекции для профилей MongoDB
	MongoDbSettingsCollection     string // Имя коллекции для настроек чатов MongoDB
	// Тип хранилища ("file", "postgres" или "mongo")
	StorageType StorageType
	// Список администраторов бота (через запятую)
	AdminUsernames []string
	// Промпт для приветствия
	WelcomePrompt string
	// Промпт для форматирования голоса
	VoiceFormatPrompt string
	// Включена ли авто-транскрипция голоса по умолчанию
	VoiceTranscriptionEnabledDefault bool
}

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

	// --- Загрузка промптов для настроек и срачей ---
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

	// --- Логирование загруженных значений (до парсинга чисел) ---
	log.Printf("[Config Load] TELEGRAM_TOKEN: ...%s (len %d)", truncateStringEnd(telegramToken, 5), len(telegramToken))
	log.Printf("[Config Load] LLM_PROVIDER: %s", llmProviderStr)
	log.Printf("[Config Load] --- Gemini Settings ---")
	log.Printf("[Config Load] GEMINI_API_KEY: ...%s (len %d)", truncateStringEnd(geminiAPIKey, 5), len(geminiAPIKey))
	log.Printf("[Config Load] GEMINI_MODEL_NAME: %s", geminiModelName)
	log.Printf("[Config Load] --- DeepSeek Settings ---")
	log.Printf("[Config Load] DEEPSEEK_API_KEY: ...%s (len %d)", truncateStringEnd(deepSeekAPIKey, 5), len(deepSeekAPIKey))
	log.Printf("[Config Load] DEEPSEEK_MODEL_NAME: %s", deepSeekModelName)
	log.Printf("[Config Load] DEEPSEEK_BASE_URL: %s", deepSeekBaseURL)
	log.Printf("[Config Load] --- Database Settings ---")
	log.Printf("[Config Load] STORAGE_TYPE: %s", storageTypeStr) // Логируем тип хранилища
	// Логирование PostgreSQL
	log.Printf("[Config Load] POSTGRESQL_HOST: %s", dbHost)
	log.Printf("[Config Load] POSTGRESQL_PORT: %s", dbPort)
	log.Printf("[Config Load] POSTGRESQL_USER: %s", dbUser)
	log.Printf("[Config Load] POSTGRESQL_PASSWORD: ...%s (len %d)", truncateStringEnd(dbPassword, 3), len(dbPassword))
	log.Printf("[Config Load] POSTGRESQL_DBNAME: %s", dbName)
	// Логирование MongoDB
	log.Printf("[Config Load] MONGODB_URI: %s", maskSecretURI(mongoURI))
	log.Printf("[Config Load] MONGODB_DBNAME: %s", mongoDbName)
	log.Printf("[Config Load] MONGODB_MESSAGES_COLLECTION: %s", mongoMessagesCollection)
	log.Printf("[Config Load] MONGODB_USER_PROFILES_COLLECTION: %s", mongoUserProfilesCollection)
	log.Printf("[Config Load] MONGODB_SETTINGS_COLLECTION: %s", mongoSettingsCollection) // Логируем новую коллекцию
	log.Printf("[Config Load] --- Prompts & Behavior ---")
	log.Printf("[Config Load] DEFAULT_PROMPT: %s...", truncateString(defaultPrompt, 50))
	log.Printf("[Config Load] DIRECT_PROMPT: %s...", truncateString(directPrompt, 50))
	log.Printf("[Config Load] DAILY_TAKE_PROMPT: %s...", truncateString(dailyTakePrompt, 50))
	log.Printf("[Config Load] SUMMARY_PROMPT: %s...", truncateString(summaryPrompt, 50))
	log.Printf("[Config Load] RATE_LIMIT_ERROR_MESSAGE: %s...", truncateString(rateLimitErrorMsg, 50))
	log.Printf("[Config Load] SRACH_WARNING_PROMPT: %s...", truncateString(srachWarningPrompt, 50))
	log.Printf("[Config Load] SRACH_ANALYSIS_PROMPT: %s...", truncateString(srachAnalysisPrompt, 50))
	log.Printf("[Config Load] SRACH_CONFIRM_PROMPT: %s...", truncateString(srachConfirmPrompt, 50))
	// --- Логирование новых дефолтных настроек ---
	log.Printf("[Config Load] DEFAULT_CONVERSATION_STYLE: %s", defaultConvStyle)
	log.Printf("[Config Load] DEFAULT_TEMPERATURE: %s", defaultTempStr)
	log.Printf("[Config Load] DEFAULT_MODEL (pre-provider): %s", defaultModel)
	log.Printf("[Config Load] DEFAULT_SAFETY_THRESHOLD: %s", defaultSafety)
	// --- Конец логирования дефолтных настроек ---
	log.Printf("[Config Load] --- Timing & Limits ---")
	log.Printf("[Config Load] TIME_ZONE: %s", timeZone)
	log.Printf("[Config Load] DEBUG: %s", debugStr)
	log.Printf("[Config Load] WELCOME_PROMPT: %s...", truncateString(welcomePrompt, 50))
	log.Printf("[Config Load] VOICE_FORMAT_PROMPT: %s...", truncateString(voiceFormatPrompt, 50))
	log.Printf("[Config Load] VOICE_TRANSCRIPTION_ENABLED_DEFAULT: %s", voiceTranscriptionEnabledDefaultStr)
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

	cfg := Config{
		TelegramToken: telegramToken,
		LLMProvider:   llmProvider,
		// --- Новые поля ---
		DefaultConversationStyle: defaultConvStyle,
		DefaultTemperature:       defaultTemp,
		DefaultModel:             defaultModel,
		DefaultSafetyThreshold:   defaultSafety,
		// --- Конец новых полей ---
		GeminiAPIKey:               geminiAPIKey,
		GeminiModelName:            geminiModelName,
		DeepSeekAPIKey:             deepSeekAPIKey,
		DeepSeekModelName:          deepSeekModelName,
		DeepSeekBaseURL:            deepSeekBaseURL,
		DefaultPrompt:              defaultPrompt,
		DirectPrompt:               directPrompt,
		DailyTakePrompt:            dailyTakePrompt,
		SummaryPrompt:              summaryPrompt,
		RateLimitStaticText:        getEnvOrDefault("RATE_LIMIT_STATIC_TEXT", "Слишком часто! Попробуйте позже."),
		RateLimitPrompt:            getEnvOrDefault("RATE_LIMIT_PROMPT", "Скажи пользователю, что он слишком часто нажимает кнопку."),
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
		SrachAnalysisEnabled:             srachEnabledStr == "true" || srachEnabledStr == "1" || srachEnabledStr == "yes",
		WelcomePrompt:                    welcomePrompt,
		VoiceFormatPrompt:                voiceFormatPrompt,
		VoiceTranscriptionEnabledDefault: voiceTranscriptionEnabledDefaultStr == "true" || voiceTranscriptionEnabledDefaultStr == "1" || voiceTranscriptionEnabledDefaultStr == "yes",
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

// logLoadedConfig выводит загруженную конфигурацию в лог (маскируя секреты)
func logLoadedConfig(cfg *Config) {
	log.Println("--- Загруженная конфигурация ---")
	log.Printf("Telegram Token: %s", maskSecret(cfg.TelegramToken))
	log.Printf("LLM Provider: %s", cfg.LLMProvider)
	log.Printf("  Default Prompt: %s...", truncateStringEnd(cfg.DefaultPrompt, 80))
	log.Printf("  Direct Prompt: %s...", truncateStringEnd(cfg.DirectPrompt, 80))
	log.Printf("  Daily Take Prompt: %s...", truncateStringEnd(cfg.DailyTakePrompt, 80))
	log.Printf("  Summary Prompt: %s...", truncateStringEnd(cfg.SummaryPrompt, 80))
	// --- Логирование новых дефолтных настроек ---
	log.Printf("  Default Conversation Style: %s", cfg.DefaultConversationStyle)
	log.Printf("  Default Temperature: %.2f", cfg.DefaultTemperature)
	log.Printf("  Default Model: %s", cfg.DefaultModel)
	log.Printf("  Default Safety Threshold: %s", cfg.DefaultSafetyThreshold)
	// --- Конец логирования новых дефолтных настроек ---

	switch cfg.LLMProvider {
	case ProviderGemini:
		log.Printf("  Gemini API Key: %s", maskSecret(cfg.GeminiAPIKey))
		log.Printf("  Gemini Model: %s", cfg.GeminiModelName)
	case ProviderDeepSeek:
		log.Printf("  DeepSeek API Key: %s", maskSecret(cfg.DeepSeekAPIKey))
		log.Printf("  DeepSeek Model: %s", cfg.DeepSeekModelName)
		log.Printf("  DeepSeek Base URL: %s", cfg.DeepSeekBaseURL)
	}

	log.Printf("Rate Limit Static Text: %s", cfg.RateLimitStaticText)
	log.Printf("Rate Limit Prompt: %s...", truncateStringEnd(cfg.RateLimitPrompt, 80))
	log.Printf("Prompt Min Messages: %s", cfg.PromptEnterMinMessages)
	log.Printf("Prompt Max Messages: %s", cfg.PromptEnterMaxMessages)
	log.Printf("Prompt Daily Time: %s", cfg.PromptEnterDailyTime)
	log.Printf("Prompt Summary Interval: %s", cfg.PromptEnterSummaryInterval)
	log.Printf("Srach Warning Prompt: %s...", truncateStringEnd(cfg.SRACH_WARNING_PROMPT, 80))
	log.Printf("Srach Analysis Prompt: %s...", truncateStringEnd(cfg.SRACH_ANALYSIS_PROMPT, 80))
	log.Printf("Srach Confirm Prompt: %s...", truncateStringEnd(cfg.SRACH_CONFIRM_PROMPT, 80))
	log.Printf("Srach Keywords: %d слов", len(cfg.SrachKeywords))
	log.Printf("Srach Analysis Enabled by default: %t", cfg.SrachAnalysisEnabled)
	log.Printf("Welcome Prompt: %s...", truncateStringEnd(cfg.WelcomePrompt, 80))
	log.Printf("Daily Take Time: %d", cfg.DailyTakeTime)
	log.Printf("Time Zone: %s", cfg.TimeZone)
	log.Printf("Summary Interval: %d hours", cfg.SummaryIntervalHours)
	log.Printf("Messages Interval: %d-%d", cfg.MinMessages, cfg.MaxMessages)
	log.Printf("Context Window: %d", cfg.ContextWindow)
	log.Printf("Debug Mode: %t", cfg.Debug)
	log.Printf("Storage Type: %s", cfg.StorageType)
	log.Printf(" - Voice Format Prompt: %s", truncateStringEnd(cfg.VoiceFormatPrompt, 100))
	log.Printf(" - Voice Transcription Default: %t", cfg.VoiceTranscriptionEnabledDefault)

	switch cfg.StorageType {
	case StorageTypePostgres:
		log.Printf("  PostgreSQL Host: %s", cfg.PostgresqlHost)
		log.Printf("  PostgreSQL Port: %s", cfg.PostgresqlPort)
		log.Printf("  PostgreSQL User: %s", cfg.PostgresqlUser)
		log.Printf("  PostgreSQL Password: %s", maskSecret(cfg.PostgresqlPassword))
		log.Printf("  PostgreSQL DB Name: %s", cfg.PostgresqlDbname)
	case StorageTypeMongo:
		log.Printf("  MongoDB URI: %s", maskSecretURI(cfg.MongoDbURI))
		log.Printf("  MongoDB DB Name: %s", cfg.MongoDbName)
		log.Printf("  MongoDB Messages Collection: %s", cfg.MongoDbMessagesCollection)
		log.Printf("  MongoDB User Profiles Collection: %s", cfg.MongoDbUserProfilesCollection)
		log.Printf("  MongoDB Settings Collection: %s", cfg.MongoDbSettingsCollection) // Логируем новую коллекцию
	case StorageTypeFile:
		log.Printf("  File Storage Path: /data/chat_*.json")
	}
	log.Printf("Admin Usernames: %v", cfg.AdminUsernames) // Логируем администраторов
	log.Println("---------------------------------")
}

// getEnvOrDefault возвращает значение переменной окружения или значение по умолчанию
func getEnvOrDefault(key, defaultValue string) string {
	if value, exists := os.LookupEnv(key); exists {
		return value
	}
	if defaultValue != "" { // Логируем только если значение не пустое
		// Уменьшил уровень детализации для значений по умолчанию БД и секретов
		if !strings.HasPrefix(key, "POSTGRESQL_") && !strings.HasPrefix(key, "MONGODB_") && key != "GEMINI_API_KEY" && key != "DEEPSEEK_API_KEY" {
			log.Printf("Переменная окружения %s не установлена, используется значение по умолчанию: %s", key, defaultValue)
		} else if key == "POSTGRESQL_PASSWORD" || key == "GEMINI_API_KEY" || key == "DEEPSEEK_API_KEY" || key == "MONGODB_URI" {
			log.Printf("Переменная окружения %s не установлена.", key) // Не логируем секретные значения по умолчанию
		} else {
			// Не логируем хост, юзера, имя БД если они пустые по умолчанию
		}
	}
	return defaultValue
}

// Вспомогательные функции для логирования
func truncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen]
}

func truncateStringEnd(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[len(s)-maxLen:]
}

// maskSecret маскирует строку секрета
func maskSecret(s string) string {
	if len(s) > 4 {
		return "****" + s[len(s)-4:]
	}
	return "****"
}

// maskSecretURI маскирует строку секретного URI, скрывая пароль и хост
func maskSecretURI(uri string) string {
	// Простой вариант: если строка содержит "@", скрыть часть до "@"
	if idx := strings.Index(uri, "@"); idx != -1 {
		if startIdx := strings.Index(uri, "://"); startIdx != -1 {
			return uri[:startIdx+3] + "****" + uri[idx:]
		}
	}
	// Если "@" нет, но строка длинная, скрыть часть
	if len(uri) > 15 {
		return uri[:8] + "****" + uri[len(uri)-4:]
	}
	// Иначе вернуть как есть или просто "****"
	if len(uri) > 0 {
		return "****"
	}
	return ""
}

// ValidateConfig проверяет корректность загруженной конфигурации
func ValidateConfig(cfg *Config) error {
	// Валидация LLM Provider и ключей
	switch cfg.LLMProvider {
	case ProviderGemini:
		if cfg.GeminiAPIKey == "" {
			return fmt.Errorf("ошибка конфигурации: LLM_PROVIDER='gemini', но GEMINI_API_KEY не установлен")
		}
	case ProviderDeepSeek:
		if cfg.DeepSeekAPIKey == "" {
			return fmt.Errorf("ошибка конфигурации: LLM_PROVIDER='deepseek', но DEEPSEEK_API_KEY не установлен")
		}
	default:
		return fmt.Errorf("ошибка конфигурации: неизвестный LLM_PROVIDER '%s'", cfg.LLMProvider)
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
	if cfg.SummaryIntervalHours < 0 || cfg.SummaryIntervalHours > 24 {
		return fmt.Errorf("ошибка конфигурации: SUMMARY_INTERVAL_HOURS (%d) должен быть в диапазоне 0-24", cfg.SummaryIntervalHours)
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
	default:
		return fmt.Errorf("ошибка конфигурации: неизвестный STORAGE_TYPE '%s'", cfg.StorageType)
	}

	// Валидация Администраторов
	if len(cfg.AdminUsernames) == 0 {
		return fmt.Errorf("ошибка конфигурации: список ADMIN_USERNAMES не должен быть пустым")
	}

	// Валидация температуры
	if cfg.DefaultTemperature < 0.0 || cfg.DefaultTemperature > 2.0 {
		return fmt.Errorf("ошибка конфигурации: DEFAULT_TEMPERATURE (%.2f) должен быть в диапазоне [0.0, 2.0]", cfg.DefaultTemperature)
	}

	return nil
}
