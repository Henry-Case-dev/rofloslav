package config

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"
)

// --- Настройки генерации Gemini ---

// GenerationSettings содержит параметры для стандартной генерации контента.
// Используем указатели для различения неустановленного значения и нуля.
type GenerationSettings struct {
	Temperature     *float32 `json:"temperature,omitempty"`
	TopP            *float32 `json:"top_p,omitempty"`
	TopK            *int     `json:"top_k,omitempty"`
	MaxOutputTokens *int     `json:"max_output_tokens,omitempty"`
	StopSequences   []string `json:"stop_sequences,omitempty"`
}

// ArbitraryGenerationSettings содержит параметры для генерации произвольного контента.
// Используем указатели для различения неустановленного значения и нуля.
type ArbitraryGenerationSettings struct {
	Temperature     *float32 `json:"temperature,omitempty"`
	TopP            *float32 `json:"top_p,omitempty"`
	TopK            *int     `json:"top_k,omitempty"`
	MaxOutputTokens *int     `json:"max_output_tokens,omitempty"`
	StopSequences   []string `json:"stop_sequences,omitempty"`
}

// --- Вспомогательные функции для создания указателей ---
func float32Ptr(v float32) *float32 {
	return &v
}

func intPtr(v int) *int {
	return &v
}

// --- Конец вспомогательных функций ---

// --- Конец настроек генерации ---

// Config содержит все настройки приложения.
type Config struct {
	TelegramToken string  `env:"TELEGRAM_BOT_TOKEN,required"`
	AdminUserIDs  []int64 // Список ID администраторов
	// --- Gemini Settings ---
	GeminiAPIKey             string `env:"GEMINI_API_KEY,required"`
	GeminiModelName          string `env:"GEMINI_MODEL_NAME,required"`
	GeminiEmbeddingModelName string `env:"GEMINI_EMBEDDING_MODEL_NAME,required"`

	// --- Qdrant Settings ---
	QdrantEndpoint        string `env:"QDRANT_ENDPOINT,required"`
	QdrantAPIKey          string `env:"QDRANT_API_KEY"` // Может быть пустым
	QdrantCollection      string `env:"QDRANT_COLLECTION,default=Rofloslav"`
	QdrantTimeoutSec      int    `env:"QDRANT_TIMEOUT_SEC,default=60"`
	QdrantOnDisk          bool   `env:"QDRANT_ON_DISK,default=false"`
	QdrantQuantizationOn  bool   `env:"QDRANT_QUANTIZATION_ON,default=false"`
	QdrantQuantizationRam bool   `env:"QDRANT_QUANTIZATION_RAM,default=false"`

	// --- Bot Settings ---
	ResponseTimeoutSec         int           `env:"RESPONSE_TIMEOUT_SEC,default=120"` // Таймаут для ответов Gemini
	Debug                      bool          `env:"DEBUG,default=false"`
	ActivateNewChats           bool          `env:"ACTIVATE_NEW_CHATS,default=true"`
	RandomReplyEnabled         bool          `env:"RANDOM_REPLY_ENABLED,default=false"`
	ReplyChance                float32       `env:"REPLY_CHANCE,default=0.1"`
	MaxMessagesForContext      int           `env:"MAX_MESSAGES_FOR_CONTEXT,default=20"`
	MaxMessagesForSummary      int           `env:"MAX_MESSAGES_FOR_SUMMARY,default=100"`
	RelevantMessagesCount      int           `env:"RELEVANT_MESSAGES_COUNT,default=5"`
	SrachResultCount           int           `env:"SRACH_RESULT_COUNT,default=10"`
	SummaryCooldown            time.Duration `env:"SUMMARY_COOLDOWN,default=5m"`
	DirectReplyLimitCount      int           `env:"DIRECT_REPLY_LIMIT_COUNT,default=3"`
	DirectReplyWindow          time.Duration `env:"DIRECT_REPLY_WINDOW,default=10m"`
	ContextWindow              int           `env:"CONTEXT_WINDOW,default=50"`     // Для LocalStorage
	ImportChunkSize            int           `env:"IMPORT_CHUNK_SIZE,default=256"` // Для Qdrant импорта
	MinMessages                int           `env:"MIN_MESSAGES,default=5"`
	MaxMessages                int           `env:"MAX_MESSAGES,default=15"`
	DailyTakeTime              int           `env:"DAILY_TAKE_TIME,default=19"` // Час по UTC по умолчанию
	SummaryIntervalHours       int           `env:"SUMMARY_INTERVAL_HOURS,default=24"`
	SrachKeywordsFile          string        `env:"SRACH_KEYWORDS_FILE,default=srach_keywords.txt"`
	TimeZone                   string        `env:"TIMEZONE,default=UTC"`
	DirectReplyRateLimitWindow time.Duration `env:"DIRECT_REPLY_RATE_LIMIT_WINDOW,default=10m"`

	// --- Default Generation Settings ---
	DefaultGenerationSettings          *GenerationSettings
	DefaultArbitraryGenerationSettings *ArbitraryGenerationSettings

	// --- Prompt Templates ---
	HelpMessage                  string `env:"HELP_MESSAGE"`
	BaseSystemPrompt             string `env:"BASE_SYSTEM_PROMPT"`
	DirectReplyPrompt            string `env:"DIRECT_REPLY_PROMPT"`
	DirectReplyLimitPrompt       string `env:"DIRECT_REPLY_LIMIT_PROMPT"`
	SummaryPrompt                string `env:"SUMMARY_PROMPT"`
	DailyTakePrompt              string `env:"DAILY_TAKE_PROMPT"`
	SummaryRateLimitInsultPrompt string `env:"SUMMARY_RATE_LIMIT_INSULT_PROMPT"`
	SummaryRateLimitStaticPrefix string `env:"SUMMARY_RATE_LIMIT_STATIC_PREFIX"`
	SummaryRateLimitStaticSuffix string `env:"SUMMARY_RATE_LIMIT_STATIC_SUFFIX"`
	SrachWarningPrompt           string `env:"SRACH_WARNING_PROMPT"`
	SrachAnalysisPrompt          string `env:"SRACH_ANALYSIS_PROMPT"`
	SrachConfirmPrompt           string `env:"SRACH_CONFIRM_PROMPT"`
	SrachStartNotificationPrompt string `env:"SRACH_START_NOTIFICATION_PROMPT"`
	PromptEnterMinMessages       string `env:"PROMPT_ENTER_MIN_MESSAGES"`
	PromptEnterMaxMessages       string `env:"PROMPT_ENTER_MAX_MESSAGES"`
	PromptEnterDailyTime         string `env:"PROMPT_ENTER_DAILY_TIME"`
	PromptEnterSummaryInterval   string `env:"PROMPT_ENTER_SUMMARY_INTERVAL"`
	DefaultPrompt                string `env:"DEFAULT_PROMPT"`
	DirectPrompt                 string `env:"DIRECT_PROMPT"`
	RateLimitDirectReplyPrompt   string `env:"RATE_LIMIT_DIRECT_REPLY_PROMPT"`

	// --- Внутренние переменные --- (не из env)
	SrachKeywords []string
	Version       string // Версия приложения (например, из git)
}

// LoadConfig загружает конфигурацию из переменных окружения и файлов.
func LoadConfig() (*Config, error) {
	// 1. Читаем .env.secrets и .env с помощью godotenv.Read()
	envSecrets, errSecrets := godotenv.Read(".env.secrets")
	if errSecrets != nil {
		log.Println("Предупреждение: .env.secrets файл не найден или ошибка чтения.")
	} else {
		log.Println(".env.secrets файл успешно прочитан.")
		// Устанавливаем переменные из .env.secrets
		for key, value := range envSecrets {
			if err := os.Setenv(key, value); err != nil {
				log.Printf("Предупреждение: Не удалось установить переменную окружения из .env.secrets: %s (%v)", key, err)
			}
		}
		log.Printf("[DEBUG Config] После .env.secrets, os.Getenv(\"TELEGRAM_BOT_TOKEN\") = '%s'", os.Getenv("TELEGRAM_BOT_TOKEN"))
	}

	envCommon, errCommon := godotenv.Read(".env") // Читаем .env
	if errCommon != nil {
		log.Println("Предупреждение: .env файл не найден или ошибка чтения.")
	} else {
		log.Println(".env файл успешно прочитан.")
		// Устанавливаем переменные из .env, НЕ перезаписывая установленные из .env.secrets
		for key, value := range envCommon {
			// Проверяем, не была ли переменная уже установлена из .env.secrets
			if _, exists := envSecrets[key]; !exists {
				if err := os.Setenv(key, value); err != nil {
					log.Printf("Предупреждение: Не удалось установить переменную окружения из .env: %s (%v)", key, err)
				}
			} else {
				// log.Printf("[DEBUG] Переменная %s из .env пропущена (уже установлена из .env.secrets)", key)
			}
		}
	}

	cfg := &Config{}

	// 2. Загрузка обязательных переменных (теперь они должны быть установлены через os.Setenv)
	log.Printf("[DEBUG Config] Перед финальной проверкой, os.Getenv(\"TELEGRAM_BOT_TOKEN\") = '%s'", os.Getenv("TELEGRAM_BOT_TOKEN"))
	cfg.TelegramToken = os.Getenv("TELEGRAM_BOT_TOKEN")
	if cfg.TelegramToken == "" {
		return nil, fmt.Errorf("переменная окружения TELEGRAM_BOT_TOKEN обязательна")
	}
	cfg.GeminiAPIKey = os.Getenv("GEMINI_API_KEY")
	if cfg.GeminiAPIKey == "" {
		return nil, fmt.Errorf("переменная окружения GEMINI_API_KEY обязательна")
	}
	cfg.QdrantEndpoint = os.Getenv("QDRANT_ENDPOINT")
	if cfg.QdrantEndpoint == "" {
		return nil, fmt.Errorf("переменная окружения QDRANT_ENDPOINT обязательна")
	}

	// 3. Загрузка остальных переменных с использованием getEnv*
	cfg.GeminiModelName = getEnv("GEMINI_MODEL_NAME", "gemini-1.5-flash-latest")
	cfg.GeminiEmbeddingModelName = getEnv("GEMINI_EMBEDDING_MODEL_NAME", "embedding-001")
	cfg.QdrantAPIKey = os.Getenv("QDRANT_API_KEY") // Может быть пустым
	cfg.QdrantCollection = getEnv("QDRANT_COLLECTION", "Rofloslav")
	cfg.QdrantTimeoutSec = getEnvAsInt("QDRANT_TIMEOUT_SEC", 60)
	cfg.QdrantOnDisk = getEnvAsBool("QDRANT_ON_DISK", false)
	cfg.QdrantQuantizationOn = getEnvAsBool("QDRANT_QUANTIZATION_ON", false)
	cfg.QdrantQuantizationRam = getEnvAsBool("QDRANT_QUANTIZATION_RAM", false)

	cfg.ResponseTimeoutSec = getEnvAsInt("RESPONSE_TIMEOUT_SEC", 120)
	cfg.Debug = getEnvAsBool("DEBUG", false)
	cfg.ActivateNewChats = getEnvAsBool("ACTIVATE_NEW_CHATS", true)
	cfg.RandomReplyEnabled = getEnvAsBool("RANDOM_REPLY_ENABLED", false)
	cfg.ReplyChance = getEnvAsFloat32("REPLY_CHANCE", 0.1)
	cfg.MaxMessagesForContext = getEnvAsInt("MAX_MESSAGES_FOR_CONTEXT", 20)
	cfg.MaxMessagesForSummary = getEnvAsInt("MAX_MESSAGES_FOR_SUMMARY", 100)
	cfg.RelevantMessagesCount = getEnvAsInt("RELEVANT_MESSAGES_COUNT", 5)
	cfg.SrachResultCount = getEnvAsInt("SRACH_RESULT_COUNT", 10)
	cfg.SummaryCooldown = getEnvAsDuration("SUMMARY_COOLDOWN", 5*time.Minute)
	cfg.DirectReplyLimitCount = getEnvAsInt("DIRECT_REPLY_LIMIT_COUNT", 3)
	cfg.DirectReplyWindow = getEnvAsDuration("DIRECT_REPLY_WINDOW", 10*time.Minute)

	// Загрузка устаревших переменных (для информации или плавного перехода)
	cfg.ContextWindow = getEnvAsInt("CONTEXT_WINDOW", 50)
	cfg.ImportChunkSize = getEnvAsInt("IMPORT_CHUNK_SIZE", 256)
	cfg.MinMessages = getEnvAsInt("MIN_MESSAGES", 5)
	cfg.MaxMessages = getEnvAsInt("MAX_MESSAGES", 15)
	cfg.DailyTakeTime = getEnvAsInt("DAILY_TAKE_TIME", 19)
	cfg.SummaryIntervalHours = getEnvAsInt("SUMMARY_INTERVAL_HOURS", 24)
	cfg.SrachKeywordsFile = getEnv("SRACH_KEYWORDS_FILE", "srach_keywords.txt")
	cfg.TimeZone = getEnv("TIMEZONE", "UTC")

	// Загрузка списка Admin User IDs
	adminIDsStr := os.Getenv("ADMIN_USER_IDS")
	if adminIDsStr != "" {
		ids := strings.Split(adminIDsStr, ",")
		for _, idStr := range ids {
			idStr = strings.TrimSpace(idStr)
			if id, err := strconv.ParseInt(idStr, 10, 64); err == nil {
				cfg.AdminUserIDs = append(cfg.AdminUserIDs, id)
			} else {
				log.Printf("Предупреждение: Неверный формат Admin User ID: %s", idStr)
			}
		}
	} else {
		// Проверяем старый одиночный ADMIN_USER_ID для совместимости
		adminIDStr := os.Getenv("ADMIN_USER_ID")
		if adminIDStr != "" {
			if id, err := strconv.ParseInt(adminIDStr, 10, 64); err == nil {
				cfg.AdminUserIDs = append(cfg.AdminUserIDs, id)
			} else {
				log.Printf("Предупреждение: Неверный формат Admin User ID: %s", adminIDStr)
			}
		}
	}
	if len(cfg.AdminUserIDs) == 0 {
		log.Println("Предупреждение: Список ADMIN_USER_IDS пуст. Некоторые команды могут быть недоступны.")
	}

	// 4. Инициализация настроек генерации по умолчанию
	cfg.DefaultGenerationSettings = &GenerationSettings{
		Temperature:     float32Ptr(0.7),
		TopP:            float32Ptr(0.9),
		TopK:            intPtr(40),
		MaxOutputTokens: intPtr(1024),
		StopSequences:   []string{},
	}
	cfg.DefaultArbitraryGenerationSettings = &ArbitraryGenerationSettings{
		Temperature:     float32Ptr(0.7),
		TopP:            float32Ptr(0.9),
		TopK:            intPtr(40),
		MaxOutputTokens: intPtr(1024),
		StopSequences:   []string{},
	}

	// 5. Загрузка Prompt Templates
	cfg.HelpMessage = getEnv("HELP_MESSAGE", "Привет! Я бот для чата. Команды: /activate, /deactivate, /status, /summarize, /srach [запрос]")
	cfg.BaseSystemPrompt = getEnv("BASE_SYSTEM_PROMPT", "Ты - участник группового чата.")
	cfg.DirectReplyPrompt = getEnv("DIRECT_REPLY_PROMPT", "Тебе адресовали сообщение:")
	cfg.DirectReplyLimitPrompt = getEnv("DIRECT_REPLY_LIMIT_PROMPT", "Вы слишком часто пишете мне.")
	cfg.SummaryPrompt = getEnv("SUMMARY_PROMPT", "Подведи итог этого диалога кратко:")
	cfg.DailyTakePrompt = os.Getenv("DAILY_TAKE_PROMPT")
	cfg.SummaryRateLimitInsultPrompt = os.Getenv("SUMMARY_RATE_LIMIT_INSULT_PROMPT")
	cfg.SummaryRateLimitStaticPrefix = os.Getenv("SUMMARY_RATE_LIMIT_STATIC_PREFIX")
	cfg.SummaryRateLimitStaticSuffix = os.Getenv("SUMMARY_RATE_LIMIT_STATIC_SUFFIX")
	cfg.SrachWarningPrompt = os.Getenv("SRACH_WARNING_PROMPT")
	cfg.SrachAnalysisPrompt = os.Getenv("SRACH_ANALYSIS_PROMPT")
	cfg.SrachConfirmPrompt = os.Getenv("SRACH_CONFIRM_PROMPT")
	cfg.SrachStartNotificationPrompt = os.Getenv("SRACH_START_NOTIFICATION_PROMPT")
	cfg.PromptEnterMinMessages = os.Getenv("PROMPT_ENTER_MIN_MESSAGES")
	cfg.PromptEnterMaxMessages = os.Getenv("PROMPT_ENTER_MAX_MESSAGES")
	cfg.PromptEnterDailyTime = os.Getenv("PROMPT_ENTER_DAILY_TIME")
	cfg.PromptEnterSummaryInterval = os.Getenv("PROMPT_ENTER_SUMMARY_INTERVAL")

	// 6. Загрузка ключевых слов для срачей
	err := cfg.loadSrachKeywords()
	if err != nil {
		log.Printf("Предупреждение: Ошибка загрузки ключевых слов для срачей: %v", err)
	}

	// 7. Установка версии
	cfg.Version = "dev" // Placeholder

	log.Println("--- Configuration Loaded ---")
	logConfig(cfg)
	return cfg, nil
}

// --- Вспомогательные функции для загрузки переменных окружения ---

func getEnv(key, fallback string) string {
	if value, exists := os.LookupEnv(key); exists {
		return value
	}
	return fallback
}

func getEnvAsInt(key string, fallback int) int {
	if valueStr, exists := os.LookupEnv(key); exists {
		if value, err := strconv.Atoi(valueStr); err == nil {
			return value
		}
		log.Printf("Предупреждение: Неверный формат числа для %s: %s. Используется %d.", key, valueStr, fallback)
	}
	return fallback
}

func getEnvAsBool(key string, fallback bool) bool {
	if valueStr, exists := os.LookupEnv(key); exists {
		valueStr = strings.ToLower(strings.TrimSpace(valueStr))
		if valueStr == "true" || valueStr == "1" || valueStr == "yes" {
			return true
		} else if valueStr == "false" || valueStr == "0" || valueStr == "no" {
			return false
		}
		log.Printf("Предупреждение: Неверный формат boolean для %s: %s. Используется %t.", key, valueStr, fallback)
	}
	return fallback
}

func getEnvAsFloat32(key string, fallback float32) float32 {
	if valueStr, exists := os.LookupEnv(key); exists {
		if value, err := strconv.ParseFloat(valueStr, 32); err == nil {
			return float32(value)
		}
		log.Printf("Предупреждение: Неверный формат float32 для %s: %s. Используется %f.", key, valueStr, fallback)
	}
	return fallback
}

func getEnvAsDuration(key string, fallback time.Duration) time.Duration {
	if valueStr, exists := os.LookupEnv(key); exists {
		if duration, err := time.ParseDuration(valueStr); err == nil {
			return duration
		} else {
			log.Printf("Предупреждение: Неверный формат duration для %s: %v. Используется %v.", key, err, fallback)
		}
	}
	return fallback
}

// loadSrachKeywords загружает ключевые слова из файла.
func (c *Config) loadSrachKeywords() error {
	filePath := c.SrachKeywordsFile
	if filePath == "" {
		log.Println("Имя файла с ключевыми словами для срачей не указано.")
		return nil // Не ошибка, просто нет файла
	}

	// Проверяем существование файла
	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		log.Printf("Файл с ключевыми словами '%s' не найден.", filePath)
		return nil // Не ошибка
	}

	content, err := ioutil.ReadFile(filePath)
	if err != nil {
		return fmt.Errorf("ошибка чтения файла '%s': %w", filePath, err)
	}

	lines := strings.Split(string(content), "\n")
	c.SrachKeywords = make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		// Игнорируем пустые строки и комментарии
		if line != "" && !strings.HasPrefix(line, "#") {
			c.SrachKeywords = append(c.SrachKeywords, line)
		}
	}
	log.Printf("Загружено %d ключевых слов для детекции срачей из '%s'.", len(c.SrachKeywords), filePath)
	return nil
}

// logConfig выводит важные параметры конфигурации в лог (кроме секретов).
func logConfig(cfg *Config) {
	log.Printf("[Config Load] Debug: %t", cfg.Debug)
	log.Printf("[Config Load] Activate New Chats: %t", cfg.ActivateNewChats)
	log.Printf("[Config Load] Random Reply Enabled: %t (Chance: %.2f)", cfg.RandomReplyEnabled, cfg.ReplyChance)
	log.Printf("[Config Load] Gemini Model: %s", cfg.GeminiModelName)
	log.Printf("[Config Load] Gemini Embedding Model: %s", cfg.GeminiEmbeddingModelName)
	log.Printf("[Config Load] Qdrant Endpoint: %s", cfg.QdrantEndpoint)
	log.Printf("[Config Load] Qdrant Collection: %s", cfg.QdrantCollection)
	log.Printf("[Config Load] Qdrant Timeout (sec): %d", cfg.QdrantTimeoutSec)
	log.Printf("[Config Load] Qdrant OnDisk: %t, Quantization: %t (RAM: %t)", cfg.QdrantOnDisk, cfg.QdrantQuantizationOn, cfg.QdrantQuantizationRam)
	log.Printf("[Config Load] Response Timeout (sec): %d", cfg.ResponseTimeoutSec)
	log.Printf("[Config Load] Max Messages for Context: %d", cfg.MaxMessagesForContext)
	log.Printf("[Config Load] Max Messages for Summary: %d", cfg.MaxMessagesForSummary)
	log.Printf("[Config Load] Relevant Messages Count (Search): %d", cfg.RelevantMessagesCount)
	log.Printf("[Config Load] Srach Result Count (Search): %d", cfg.SrachResultCount)
	log.Printf("[Config Load] Summary Cooldown: %v", cfg.SummaryCooldown)
	log.Printf("[Config Load] Daily Take Time: %d:00 (%s)", cfg.DailyTakeTime, cfg.TimeZone)
	log.Printf("[Config Load] Summary Interval (hours): %d", cfg.SummaryIntervalHours)
	log.Printf("[Config Load] Srach Keywords File: %s (loaded: %d)", cfg.SrachKeywordsFile, len(cfg.SrachKeywords))
	log.Printf("[Config Load] Direct Reply Limit: %d requests per %v", cfg.DirectReplyLimitCount, cfg.DirectReplyWindow)
	log.Printf("[Config Load] Admin IDs: %v", cfg.AdminUserIDs)
	log.Printf("[Config Load] Help Message Loaded: %t", cfg.HelpMessage != "")
	// Логирование устаревших полей для информации
	log.Printf("[Config Load] (Legacy) Context Window: %d", cfg.ContextWindow)
	log.Printf("[Config Load] (Legacy) Import Chunk Size: %d", cfg.ImportChunkSize)
	log.Printf("[Config Load] (Legacy) Min/Max Messages: %d/%d", cfg.MinMessages, cfg.MaxMessages)
	log.Printf("[Config Load] (Legacy) DirectReplyRateLimitWindow: %v", cfg.DirectReplyRateLimitWindow)
}

// --- Вспомогательные функции для создания указателей ---
// func float32Ptr(v float32) *float32 { // Перенесены выше
// 	return &v
// }
// func intPtr(v int) *int { // Перенесены выше
// 	return &v
// }
// --- Конец вспомогательных функций ---

// SaveConfigToFile сохраняет текущую конфигурацию в JSON файл.
// Может быть полезно для отладки или создания шаблона.
func SaveConfigToFile(c *Config, filePath string) error {
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Errorf("ошибка маршалинга конфигурации: %w", err)
	}

	// Убедимся, что директория существует
	dir := filepath.Dir(filePath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("ошибка создания директории %s: %w", dir, err)
	}

	err = ioutil.WriteFile(filePath, data, 0644)
	if err != nil {
		return fmt.Errorf("ошибка записи конфигурации в файл %s: %w", filePath, err)
	}
	log.Printf("Конфигурация сохранена в %s", filePath)
	return nil
}
