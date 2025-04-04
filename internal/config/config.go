package config

import (
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"
)

// Config содержит все параметры конфигурации бота
type Config struct {
	TelegramToken         string
	GeminiAPIKey          string
	GeminiModelName       string
	DefaultPrompt         string
	DirectPrompt          string
	DailyTakePrompt       string
	SummaryPrompt         string
	RateLimitErrorMessage string
	// Новые промпты для ввода
	PromptEnterMinMessages     string
	PromptEnterMaxMessages     string
	PromptEnterDailyTime       string
	PromptEnterSummaryInterval string
	// Новые промпты для анализа срачей
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
	// Новые поля для S3
	UseS3Storage      bool
	S3Endpoint        string
	S3Region          string
	S3AccessKeyID     string
	S3SecretAccessKey string
	S3BucketName      string
	S3UseSSL          bool
	// Интервал сохранения истории
	HistorySaveInterval time.Duration
	// Новые поля для лимита прямых ответов
	RateLimitDirectReplyPrompt string
	DirectReplyRateLimitCount  int
	DirectReplyRateLimitWindow time.Duration
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
	geminiAPIKey := getEnvOrDefault("GEMINI_API_KEY", "")
	geminiModelName := getEnvOrDefault("GEMINI_MODEL_NAME", "gemini-1.5-flash-latest")
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

	// --- Загрузка НОВЫХ переменных ---
	summaryIntervalStr := getEnvOrDefault("SUMMARY_INTERVAL_HOURS", "2") // По умолчанию 2 часа
	promptEnterMin := getEnvOrDefault("PROMPT_ENTER_MIN_MESSAGES", "Введите минимальный интервал:")
	promptEnterMax := getEnvOrDefault("PROMPT_ENTER_MAX_MESSAGES", "Введите максимальный интервал:")
	promptEnterDailyTime := getEnvOrDefault("PROMPT_ENTER_DAILY_TIME", "Введите час для темы дня (0-23):")
	promptEnterSummaryInterval := getEnvOrDefault("PROMPT_ENTER_SUMMARY_INTERVAL", "Введите интервал авто-саммари (в часах, 0=выкл):")
	srachWarningPrompt := getEnvOrDefault("SRACH_WARNING_PROMPT", "Внимание, срач!")
	srachAnalysisPrompt := getEnvOrDefault("SRACH_ANALYSIS_PROMPT", "Анализирую срач...")
	srachConfirmPrompt := getEnvOrDefault("SRACH_CONFIRM_PROMPT", "Это сообщение - часть срача? Ответь true или false:")
	srachKeywordsRaw := getEnvOrDefault("SRACH_KEYWORDS", "")

	// --- Загрузка переменных S3 ---
	s3Endpoint := getEnvOrDefault("S3_ENDPOINT", "")
	s3Region := getEnvOrDefault("S3_REGION", "us-east-1") // Стандартный регион как fallback
	s3AccessKeyID := getEnvOrDefault("S3_ACCESS_KEY_ID", "")
	s3SecretAccessKey := getEnvOrDefault("S3_SECRET_ACCESS_KEY", "")
	s3BucketName := getEnvOrDefault("S3_BUCKET_NAME", "")
	s3UseSSLStr := getEnvOrDefault("S3_USE_SSL", "true")
	s3UseSSL := s3UseSSLStr == "true"
	useS3StorageStr := getEnvOrDefault("USE_S3_STORAGE", "false")
	useS3Storage := useS3StorageStr == "true"
	historySaveIntervalStr := getEnvOrDefault("HISTORY_SAVE_INTERVAL", "5m")
	// --- Загрузка переменных лимита прямых ответов ---
	rateLimitPrompt := getEnvOrDefault("RATE_LIMIT_DIRECT_REPLY_PROMPT", "Я устал, отдохни.") // Простое значение по умолчанию
	rateLimitCountStr := getEnvOrDefault("DIRECT_REPLY_RATE_LIMIT_COUNT", "3")
	rateLimitWindowStr := getEnvOrDefault("DIRECT_REPLY_RATE_LIMIT_WINDOW", "10m")
	// --- Конец загрузки S3 ---

	// --- Логирование загруженных значений (до парсинга чисел) ---
	log.Printf("[Config Load] TELEGRAM_TOKEN: ...%s (len %d)", truncateStringEnd(telegramToken, 5), len(telegramToken))
	log.Printf("[Config Load] GEMINI_API_KEY: ...%s (len %d)", truncateStringEnd(geminiAPIKey, 5), len(geminiAPIKey))
	log.Printf("[Config Load] GEMINI_MODEL_NAME: %s", geminiModelName)
	log.Printf("[Config Load] DEFAULT_PROMPT: %s...", truncateString(defaultPrompt, 50))
	log.Printf("[Config Load] DIRECT_PROMPT: %s...", truncateString(directPrompt, 50))
	log.Printf("[Config Load] DAILY_TAKE_PROMPT: %s...", truncateString(dailyTakePrompt, 50))
	log.Printf("[Config Load] SUMMARY_PROMPT: %s...", truncateString(summaryPrompt, 50))
	log.Printf("[Config Load] RATE_LIMIT_ERROR_MESSAGE: %s...", truncateString(rateLimitErrorMsg, 50))
	log.Printf("[Config Load] TIME_ZONE: %s", timeZone)
	log.Printf("[Config Load] SRACH_WARNING_PROMPT: %s...", truncateString(srachWarningPrompt, 50))
	log.Printf("[Config Load] SRACH_ANALYSIS_PROMPT: %s...", truncateString(srachAnalysisPrompt, 50))
	log.Printf("[Config Load] SRACH_CONFIRM_PROMPT: %s...", truncateString(srachConfirmPrompt, 50))
	log.Printf("[Config Load] DEBUG: %s", debugStr)
	log.Printf("[Config Load] S3_ENDPOINT: %s", s3Endpoint)
	log.Printf("[Config Load] S3_REGION: %s", s3Region)
	log.Printf("[Config Load] S3_ACCESS_KEY_ID: %s...", truncateString(s3AccessKeyID, 5))
	log.Printf("[Config Load] S3_SECRET_ACCESS_KEY: *** (set)")
	log.Printf("[Config Load] S3_BUCKET_NAME: %s", s3BucketName)
	log.Printf("[Config Load] S3_USE_SSL: %t", s3UseSSL)
	log.Printf("[Config Load] USE_S3_STORAGE: %t", useS3Storage)
	log.Printf("[Config Load] HISTORY_SAVE_INTERVAL: %s", historySaveIntervalStr)
	log.Printf("[Config Load] RATE_LIMIT_DIRECT_REPLY_PROMPT: %s...", truncateString(rateLimitPrompt, 50))
	log.Printf("[Config Load] DIRECT_REPLY_RATE_LIMIT_COUNT: %s", rateLimitCountStr)
	log.Printf("[Config Load] DIRECT_REPLY_RATE_LIMIT_WINDOW: %s", rateLimitWindowStr)
	// --- Конец логирования ---

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

	// Парсинг интервала сохранения истории
	historySaveInterval, err := time.ParseDuration(historySaveIntervalStr)
	if err != nil {
		log.Printf("Ошибка парсинга HISTORY_SAVE_INTERVAL: %v, используем 5m", err)
		historySaveInterval = 5 * time.Minute
	}

	// --- Парсинг значений лимита прямых ответов ---
	rateLimitCount, err := strconv.Atoi(rateLimitCountStr)
	if err != nil || rateLimitCount < 1 {
		log.Printf("Ошибка парсинга DIRECT_REPLY_RATE_LIMIT_COUNT ('%s'): %v, используем 3", rateLimitCountStr, err)
		rateLimitCount = 3
	}
	rateLimitWindow, err := time.ParseDuration(rateLimitWindowStr)
	if err != nil {
		log.Printf("Ошибка парсинга DIRECT_REPLY_RATE_LIMIT_WINDOW ('%s'): %v, используем 10m", rateLimitWindowStr, err)
		rateLimitWindow = 10 * time.Minute
	}

	return &Config{
		TelegramToken:              telegramToken,
		GeminiAPIKey:               geminiAPIKey,
		GeminiModelName:            geminiModelName,
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
		UseS3Storage:               useS3Storage,
		S3Endpoint:                 s3Endpoint,
		S3Region:                   s3Region,
		S3AccessKeyID:              s3AccessKeyID,
		S3SecretAccessKey:          s3SecretAccessKey,
		S3BucketName:               s3BucketName,
		S3UseSSL:                   s3UseSSL,
		HistorySaveInterval:        historySaveInterval,
		RateLimitDirectReplyPrompt: rateLimitPrompt,
		DirectReplyRateLimitCount:  rateLimitCount,
		DirectReplyRateLimitWindow: rateLimitWindow,
	}, nil
}

// getEnvOrDefault возвращает значение переменной окружения или значение по умолчанию
func getEnvOrDefault(key, defaultValue string) string {
	if value, exists := os.LookupEnv(key); exists {
		return value
	}
	if defaultValue != "" { // Логируем только если значение не пустое
		log.Printf("Переменная окружения %s не установлена, используется значение по умолчанию: %s", key, defaultValue)
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
