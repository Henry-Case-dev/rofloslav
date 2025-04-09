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
	// Настройки Gemini
	GeminiAPIKey    string
	GeminiModelName string
	// Настройки DeepSeek
	DeepSeekAPIKey    string
	DeepSeekModelName string
	DeepSeekBaseURL   string // Опционально, для кастомного URL
	// Настройки поведения бота
	RateLimitErrorMessage string
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
	MongoDbURI        string // Строка подключения MongoDB
	MongoDbName       string // Имя базы данных MongoDB
	MongoDbCollection string // Имя коллекции MongoDB

	// Тип хранилища ("file", "postgres" или "mongo")
	StorageType StorageType
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

	// --- Загрузка переменных Gemini ---
	geminiAPIKey := getEnvOrDefault("GEMINI_API_KEY", "")
	geminiModelName := getEnvOrDefault("GEMINI_MODEL_NAME", "gemini-2.0-flash")

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

	// --- Загрузка переменных PostgreSQL ---
	dbHost := getEnvOrDefault("POSTGRESQL_HOST", "")         // Используем POSTGRESQL_
	dbPort := getEnvOrDefault("POSTGRESQL_PORT", "5432")     // Используем POSTGRESQL_
	dbUser := getEnvOrDefault("POSTGRESQL_USER", "")         // Используем POSTGRESQL_
	dbPassword := getEnvOrDefault("POSTGRESQL_PASSWORD", "") // Используем POSTGRESQL_
	dbName := getEnvOrDefault("POSTGRESQL_DBNAME", "")       // Используем POSTGRESQL_DBNAME

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
	log.Printf("[Config Load] POSTGRESQL_HOST: %s", dbHost)
	log.Printf("[Config Load] POSTGRESQL_PORT: %s", dbPort)
	log.Printf("[Config Load] POSTGRESQL_USER: %s", dbUser)
	log.Printf("[Config Load] POSTGRESQL_PASSWORD: ...%s (len %d)", truncateStringEnd(dbPassword, 3), len(dbPassword))
	log.Printf("[Config Load] POSTGRESQL_DBNAME: %s", dbName)
	log.Printf("[Config Load] --- Prompts & Behavior ---")
	log.Printf("[Config Load] DEFAULT_PROMPT: %s...", truncateString(defaultPrompt, 50))
	log.Printf("[Config Load] DIRECT_PROMPT: %s...", truncateString(directPrompt, 50))
	log.Printf("[Config Load] DAILY_TAKE_PROMPT: %s...", truncateString(dailyTakePrompt, 50))
	log.Printf("[Config Load] SUMMARY_PROMPT: %s...", truncateString(summaryPrompt, 50))
	log.Printf("[Config Load] RATE_LIMIT_ERROR_MESSAGE: %s...", truncateString(rateLimitErrorMsg, 50))
	log.Printf("[Config Load] SRACH_WARNING_PROMPT: %s...", truncateString(srachWarningPrompt, 50))
	log.Printf("[Config Load] SRACH_ANALYSIS_PROMPT: %s...", truncateString(srachAnalysisPrompt, 50))
	log.Printf("[Config Load] SRACH_CONFIRM_PROMPT: %s...", truncateString(srachConfirmPrompt, 50))
	log.Printf("[Config Load] --- Timing & Limits ---")
	log.Printf("[Config Load] TIME_ZONE: %s", timeZone)
	log.Printf("[Config Load] DEBUG: %s", debugStr)
	// --- Конец логирования ---

	// --- Валидация LLM Provider ---
	var llmProvider LLMProvider
	switch strings.ToLower(llmProviderStr) {
	case string(ProviderGemini):
		llmProvider = ProviderGemini
	case string(ProviderDeepSeek):
		llmProvider = ProviderDeepSeek
	default:
		log.Printf("[Config Load WARN] Неизвестный LLM_PROVIDER '%s'. Используется '%s'.", llmProviderStr, ProviderGemini)
		llmProvider = ProviderGemini
	}

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

	debug := debugStr == "true" || debugStr == "1" || debugStr == "yes"

	cfg := Config{
		TelegramToken:              telegramToken,
		LLMProvider:                llmProvider,
		GeminiAPIKey:               geminiAPIKey,
		GeminiModelName:            geminiModelName,
		DeepSeekAPIKey:             deepSeekAPIKey,
		DeepSeekModelName:          deepSeekModelName,
		DeepSeekBaseURL:            deepSeekBaseURL,
		DefaultPrompt:              defaultPrompt,
		DirectPrompt:               directPrompt,
		DailyTakePrompt:            dailyTakePrompt,
		SummaryPrompt:              summaryPrompt,
		RateLimitErrorMessage:      rateLimitErrorMsg,
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
		MongoDbURI:         getEnvOrDefault("MONGODB_URI", ""),
		MongoDbName:        getEnvOrDefault("MONGODB_DBNAME", "rofloslav_history"),
		MongoDbCollection:  getEnvOrDefault("MONGODB_COLLECTION", "chat_messages"),
	}

	// Валидация и установка StorageType
	storageTypeStr := strings.ToLower(getEnvOrDefault("STORAGE_TYPE", string(StorageTypePostgres))) // По умолчанию PostgreSQL
	switch StorageType(storageTypeStr) {
	case StorageTypeFile:
		cfg.StorageType = StorageTypeFile
	case StorageTypePostgres:
		cfg.StorageType = StorageTypePostgres
	case StorageTypeMongo:
		cfg.StorageType = StorageTypeMongo
	default:
		log.Printf("Предупреждение: Неизвестный STORAGE_TYPE '%s'. Используется PostgreSQL по умолчанию.", storageTypeStr)
		cfg.StorageType = StorageTypePostgres // Устанавливаем значение по умолчанию
	}

	// Валидация LLM провайдера
	llmProviderStr = strings.ToLower(string(cfg.LLMProvider)) // Преобразуем к строке для switch
	switch LLMProvider(llmProviderStr) {                      // Сравниваем как LLMProvider
	case ProviderGemini:
		cfg.LLMProvider = ProviderGemini
		if cfg.GeminiAPIKey == "" {
			return nil, fmt.Errorf("ошибка конфигурации: LLM_PROVIDER='gemini', но GEMINI_API_KEY не установлен")
		}
	case ProviderDeepSeek:
		cfg.LLMProvider = ProviderDeepSeek
		if cfg.DeepSeekAPIKey == "" {
			return nil, fmt.Errorf("ошибка конфигурации: LLM_PROVIDER='deepseek', но DEEPSEEK_API_KEY не установлен")
		}
	default:
		log.Printf("Предупреждение: Неизвестный LLM_PROVIDER '%s'. Используется Gemini по умолчанию.", llmProviderStr)
		cfg.LLMProvider = ProviderGemini // Устанавливаем значение по умолчанию
		if cfg.GeminiAPIKey == "" {
			return nil, fmt.Errorf("ошибка конфигурации: Используется Gemini по умолчанию, но GEMINI_API_KEY не установлен")
		}
	}

	// Валидация интервалов
	if dailyTakeTime < 0 || dailyTakeTime > 23 {
		log.Printf("Интервал для темы дня должен быть в диапазоне 0-23, используем 19")
		dailyTakeTime = 19
	}
	if minMsg < 1 || minMsg > 100 {
		log.Printf("Минимальное количество сообщений должно быть в диапазоне 1-100, используем 10")
		minMsg = 10
	}
	if maxMsg < 1 || maxMsg > 100 {
		log.Printf("Максимальное количество сообщений должно быть в диапазоне 1-100, используем 30")
		maxMsg = 30
	}
	if contextWindow < 1 || contextWindow > 1000 {
		log.Printf("Контекстное окно должно быть в диапазоне 1-1000, используем 1000")
		contextWindow = 1000
	}
	if summaryIntervalHours < 0 || summaryIntervalHours > 24 {
		log.Printf("Интервал авто-саммари должен быть в диапазоне 0-24, используем 2")
		summaryIntervalHours = 2
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
		if cfg.MongoDbCollection == "" {
			return nil, fmt.Errorf("ошибка конфигурации: STORAGE_TYPE='mongo', но MONGODB_COLLECTION не установлен")
		}
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

	switch cfg.LLMProvider {
	case ProviderGemini:
		log.Printf("  Gemini API Key: %s", maskSecret(cfg.GeminiAPIKey))
		log.Printf("  Gemini Model: %s", cfg.GeminiModelName)
	case ProviderDeepSeek:
		log.Printf("  DeepSeek API Key: %s", maskSecret(cfg.DeepSeekAPIKey))
		log.Printf("  DeepSeek Model: %s", cfg.DeepSeekModelName)
		log.Printf("  DeepSeek Base URL: %s", cfg.DeepSeekBaseURL)
	}

	log.Printf("Rate Limit Message: %s", cfg.RateLimitErrorMessage)
	log.Printf("Prompt Min Messages: %s", cfg.PromptEnterMinMessages)
	log.Printf("Prompt Max Messages: %s", cfg.PromptEnterMaxMessages)
	log.Printf("Prompt Daily Time: %s", cfg.PromptEnterDailyTime)
	log.Printf("Prompt Summary Interval: %s", cfg.PromptEnterSummaryInterval)
	log.Printf("Srach Warning Prompt: %s...", truncateStringEnd(cfg.SRACH_WARNING_PROMPT, 80))
	log.Printf("Srach Analysis Prompt: %s...", truncateStringEnd(cfg.SRACH_ANALYSIS_PROMPT, 80))
	log.Printf("Srach Confirm Prompt: %s...", truncateStringEnd(cfg.SRACH_CONFIRM_PROMPT, 80))
	log.Printf("Srach Keywords: %d слов", len(cfg.SrachKeywords))
	log.Printf("Daily Take Time: %d", cfg.DailyTakeTime)
	log.Printf("Time Zone: %s", cfg.TimeZone)
	log.Printf("Summary Interval: %d hours", cfg.SummaryIntervalHours)
	log.Printf("Messages Interval: %d-%d", cfg.MinMessages, cfg.MaxMessages)
	log.Printf("Context Window: %d", cfg.ContextWindow)
	log.Printf("Debug Mode: %t", cfg.Debug)
	log.Printf("Storage Type: %s", cfg.StorageType)

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
		log.Printf("  MongoDB Collection: %s", cfg.MongoDbCollection)
	case StorageTypeFile:
		log.Printf("  File Storage Path: /data/chat_*.json")
	}
	log.Println("---------------------------------")
}

// getEnvOrDefault возвращает значение переменной окружения или значение по умолчанию
func getEnvOrDefault(key, defaultValue string) string {
	if value, exists := os.LookupEnv(key); exists {
		return value
	}
	if defaultValue != "" { // Логируем только если значение не пустое
		// Уменьшил уровень детализации для значений по умолчанию БД
		if !strings.HasPrefix(key, "POSTGRESQL_") || key == "POSTGRESQL_PORT" {
			log.Printf("Переменная окружения %s не установлена, используется значение по умолчанию: %s", key, defaultValue)
		} else if key == "POSTGRESQL_PASSWORD" {
			log.Printf("Переменная окружения %s не установлена.", key) // Не логируем пароль по умолчанию
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

// maskSecretURI маскирует строку секретного URI
func maskSecretURI(s string) string {
	if len(s) > 4 {
		return "****" + s[len(s)-4:]
	}
	return "****"
}
