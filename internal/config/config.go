package config

import (
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"
)

// Config содержит все конфигурационные параметры приложения
type Config struct {
	TelegramToken   string
	GeminiAPIKey    string
	GeminiModelName string
	Debug           bool

	ContextWindow                int
	ContextRelevantMessagesCount int
	MinMessages                  int
	MaxMessages                  int

	DailyTakePrompt string
	DailyTakeTime   int
	TimeZone        string

	DefaultPrompt string
	DirectPrompt  string

	SummaryPrompt string
	// Старые поля для лимита саммари - УДАЛЕНЫ
	// RateLimitErrorMessage string
	// Добавляем новые поля для составного сообщения
	SummaryRateLimitStaticPrefix string
	SummaryRateLimitInsultPrompt string
	SummaryRateLimitStaticSuffix string

	SummaryIntervalHours int // Интервал автоматического саммари в часах (0 - выключено)

	// Настройки анализа срачей
	SrachKeywords         []string `env:"SRACH_KEYWORDS" envSeparator:","`
	SRACH_WARNING_PROMPT  string   `env:"SRACH_WARNING_PROMPT"`
	SRACH_CONFIRM_PROMPT  string   `env:"SRACH_CONFIRM_PROMPT"`
	SRACH_ANALYSIS_PROMPT string   `env:"SRACH_ANALYSIS_PROMPT"`

	// Настройки лимита прямых ответов
	RateLimitDirectReplyPrompt string
	DirectReplyRateLimitCount  int
	DirectReplyRateLimitWindow time.Duration

	// Настройки промптов для ввода настроек
	PromptEnterMinMessages     string `env:"PROMPT_ENTER_MIN_MESSAGES"`
	PromptEnterMaxMessages     string `env:"PROMPT_ENTER_MAX_MESSAGES"`
	PromptEnterDailyTime       string `env:"PROMPT_ENTER_DAILY_TIME"`
	PromptEnterSummaryInterval string `env:"PROMPT_ENTER_SUMMARY_INTERVAL"`

	// --- Настройки хранилища ---
	UseS3Storage bool `env:"USE_S3_STORAGE"`
	// --- S3 поля удалены ---

	// --- НОВЫЕ Настройки Qdrant ---
	QdrantEndpoint   string `env:"QDRANT_ENDPOINT"`
	QdrantAPIKey     string `env:"QDRANT_API_KEY"` // Опционально, если используется
	QdrantCollection string `env:"QDRANT_COLLECTION"`
	QdrantTimeoutSec int    `env:"QDRANT_TIMEOUT_SEC" envDefault:"30"` // Таймаут для операций Qdrant
	// --- НОВЫЕ Параметры оптимизации Qdrant ---
	QdrantOnDisk          bool `env:"QDRANT_ON_DISK" envDefault:"false"`          // Хранить ли основные векторы на диске
	QdrantQuantizationOn  bool `env:"QDRANT_QUANTIZATION_ON" envDefault:"false"`  // Включить ли скалярное квантование int8
	QdrantQuantizationRam bool `env:"QDRANT_QUANTIZATION_RAM" envDefault:"false"` // Держать ли квантованные векторы в RAM

	// --- Настройки импорта старых данных ---
	OldDataDir           string `env:"OLD_DATA_DIR" envDefault:"data/old"`          // Директория со старыми JSON-логами для импорта
	ImportOldDataOnStart bool   `env:"IMPORT_OLD_DATA_ON_START" envDefault:"false"` // Импортировать ли старые данные при старте
	ImportChunkSize      int    `env:"IMPORT_CHUNK_SIZE" envDefault:"256"`          // Размер чанка для обработки при импорте

	// --- Хранилище --- // NEW:
	StorageType string `env:"STORAGE_TYPE" envDefault:"qdrant"` // Тип хранилища: "qdrant" или "local"
	// ContextWindow        int    `env:"CONTEXT_WINDOW" envDefault:"500"`       // Максимальное количество сообщений в истории для LocalStorage - УДАЛЕНО ДУБЛИРОВАНИЕ

	// НОВОЕ ПОЛЕ: Директория для данных
	DataDir string `env:"DATA_DIR" envDefault:"data"`

	// --- Настройки Gemini API ---
	// GeminiAPIKey string `env:"GEMINI_API_KEY,required"` // <-- УДАЛЯЕМ ДУБЛИКАТ
}

// Load загружает конфигурацию из .env файла
func Load() (*Config, error) {
	// Загружаем основной .env файл
	if err := godotenv.Load(); err != nil {
		log.Println("Предупреждение: Не удалось загрузить .env файл:", err)
	} else {
		log.Println(".env файл успешно загружен.") // Добавим лог успеха
	}

	// Загружаем секретный .env.secrets файл (перезапишет основной, если есть совпадения)
	// Ошибку здесь игнорируем, так как файл может отсутствовать
	if err := godotenv.Load(".env.secrets"); err != nil {
		log.Println("Файл .env.secrets не найден, секреты будут загружены из системных переменных или .env")
	} else {
		log.Println(".env.secrets файл успешно загружен.")
	}

	cfg := &Config{
		// Значения по умолчанию
		GeminiModelName:              "gemini-1.5-flash-latest", // Используем Flash по умолчанию
		Debug:                        false,
		ContextWindow:                1000, // Увеличено окно для LocalStorage по умолчанию
		ContextRelevantMessagesCount: 10,   // Искать 10 релевантных сообщений по умолчанию
		MinMessages:                  15,   // Значения Min/Max из вашего .env
		MaxMessages:                  30,
		DailyTakeTime:                19,                   // 9 утра по умолчанию // ИСПРАВЛЕНО: 19 из вашего .env
		TimeZone:                     "Asia/Yekaterinburg", // Ваш TimeZone
		SummaryIntervalHours:         0,                    // Авто-саммари выключено по умолчанию
		DefaultPrompt:                "Ты простой русскоязычный собеседник в чате.",
		DirectPrompt:                 "Тебя упомянули или ответили на твое сообщение. Ответь коротко и саркастично.",
		DailyTakePrompt:              "Придумай короткую тему дня для обсуждения в чате.",
		SummaryPrompt:                "Сделай краткое саммари последних сообщений в чате:",
		// Старые RateLimitErrorMessage - УДАЛЕНЫ
		SummaryRateLimitStaticPrefix: "Слишком часто запрашиваешь саммари.",
		SummaryRateLimitInsultPrompt: "Придумай короткое безобидное оскорбление для нетерпеливого пользователя.",
		SummaryRateLimitStaticSuffix: "Подожди еще %s.",

		// Настройки анализа срачей по умолчанию
		SrachKeywords:         []string{"ты кто", "бот тупой", "иди нахуй", "заткнись", "слово1", "слово2"}, // Пример ключевых слов
		SRACH_WARNING_PROMPT:  "🚨 Внимание! Обнаружен потенциальный срач!",
		SRACH_CONFIRM_PROMPT:  "Ответь 'true' если следующее сообщение похоже на начало или продолжение конфликта/срача, иначе ответь 'false'. Сообщение:",
		SRACH_ANALYSIS_PROMPT: "Проанализируй следующий диалог на предмет конфликта. Кратко опиши суть конфликта, основных участников и возможные причины. Дай рекомендации по деэскалации.",

		// Настройки лимита прямых ответов по умолчанию
		RateLimitDirectReplyPrompt: "Хватит мне писать так часто. Отдохни.",
		DirectReplyRateLimitCount:  3,
		DirectReplyRateLimitWindow: 10 * time.Minute,

		// Настройки промптов для ввода настроек
		PromptEnterMinMessages:     "Введите новое минимальное количество сообщений для ответа (число > 0):",
		PromptEnterMaxMessages:     "Введите новое максимальное количество сообщений для ответа (число >= минимального):",
		PromptEnterDailyTime:       "Введите новый час для ежедневного тейка (0-23) по времени %s:",
		PromptEnterSummaryInterval: "Введите новый интервал для авто-саммари в часах (0 - выключить):",

		// Настройки хранилища по умолчанию
		UseS3Storage: false, // S3 выключен по умолчанию
		// --- S3 значения по умолчанию удалены ---

		// Настройки Qdrant по умолчанию
		QdrantEndpoint:   "http://localhost:6333", // Стандартный эндпоинт Qdrant
		QdrantAPIKey:     "",                      // API ключ не используется по умолчанию
		QdrantCollection: "chat_history",          // Имя коллекции по умолчанию
		QdrantTimeoutSec: 15,                      // Таймаут по умолчанию
		// --- НОВЫЕ Настройки оптимизации Qdrant по умолчанию ---
		QdrantOnDisk:          false, // По умолчанию векторы в RAM
		QdrantQuantizationOn:  false, // Квантование выключено по умолчанию
		QdrantQuantizationRam: true,  // Если квантование включено, держать в RAM

		// Настройки импорта по умолчанию
		ImportOldDataOnStart: false,       // Импорт выключен по умолчанию
		OldDataDir:           "/data/old", // Папка для старых данных по умолчанию
		ImportChunkSize:      256,         // Размер чанка для импорта

		// --- Хранилище --- // NEW:
		StorageType: "qdrant", // Тип хранилища: "qdrant" или "local"
		// ContextWindow: 500,      // Максимальное количество сообщений в истории для LocalStorage - УДАЛЕНО ДУБЛИРОВАНИЕ

		// НОВОЕ ПОЛЕ: Директория для данных
		DataDir: "data",

		// --- Настройки Gemini API ---
		// GeminiAPIKey: "", // <-- УДАЛЯЕМ ДУБЛИКАТ
	}

	// Загрузка строковых значений
	cfg.TelegramToken = os.Getenv("TELEGRAM_TOKEN")
	cfg.GeminiAPIKey = os.Getenv("GEMINI_API_KEY")
	if geminiModel := os.Getenv("GEMINI_MODEL_NAME"); geminiModel != "" {
		cfg.GeminiModelName = geminiModel
	}
	if defaultPrompt := os.Getenv("DEFAULT_PROMPT"); defaultPrompt != "" {
		cfg.DefaultPrompt = defaultPrompt
	}
	if directPrompt := os.Getenv("DIRECT_PROMPT"); directPrompt != "" {
		cfg.DirectPrompt = directPrompt
	}
	if dailyTakePrompt := os.Getenv("DAILY_TAKE_PROMPT"); dailyTakePrompt != "" {
		cfg.DailyTakePrompt = dailyTakePrompt
	}
	if tz := os.Getenv("TIME_ZONE"); tz != "" {
		cfg.TimeZone = tz
	}
	if summaryPrompt := os.Getenv("SUMMARY_PROMPT"); summaryPrompt != "" {
		cfg.SummaryPrompt = summaryPrompt
	}
	// --- Загрузка новых полей для RateLimitErrorMessage ---
	if prefix := os.Getenv("SUMMARY_RATE_LIMIT_STATIC_PREFIX"); prefix != "" {
		cfg.SummaryRateLimitStaticPrefix = prefix
	}
	if insult := os.Getenv("SUMMARY_RATE_LIMIT_INSULT_PROMPT"); insult != "" {
		cfg.SummaryRateLimitInsultPrompt = insult
	}
	if suffix := os.Getenv("SUMMARY_RATE_LIMIT_STATIC_SUFFIX"); suffix != "" {
		cfg.SummaryRateLimitStaticSuffix = suffix
	}
	// --- Конец загрузки ---
	if srachWarn := os.Getenv("SRACH_WARNING_PROMPT"); srachWarn != "" {
		cfg.SRACH_WARNING_PROMPT = srachWarn
	}
	if srachConfirm := os.Getenv("SRACH_CONFIRM_PROMPT"); srachConfirm != "" {
		cfg.SRACH_CONFIRM_PROMPT = srachConfirm
	}
	if srachAnalysis := os.Getenv("SRACH_ANALYSIS_PROMPT"); srachAnalysis != "" {
		cfg.SRACH_ANALYSIS_PROMPT = srachAnalysis
	}
	if keywords := os.Getenv("SRACH_KEYWORDS"); keywords != "" {
		cfg.SrachKeywords = strings.Split(keywords, ",")
		// Очищаем пробелы по краям у каждого слова
		for i, w := range cfg.SrachKeywords {
			cfg.SrachKeywords[i] = strings.TrimSpace(w)
		}
	}
	if rateLimitPrompt := os.Getenv("RATE_LIMIT_DIRECT_REPLY_PROMPT"); rateLimitPrompt != "" {
		cfg.RateLimitDirectReplyPrompt = rateLimitPrompt
	}
	// --- Загрузка промптов для настроек ---
	if prompt := os.Getenv("PROMPT_ENTER_MIN_MESSAGES"); prompt != "" {
		cfg.PromptEnterMinMessages = prompt
	}
	if prompt := os.Getenv("PROMPT_ENTER_MAX_MESSAGES"); prompt != "" {
		cfg.PromptEnterMaxMessages = prompt
	}
	if prompt := os.Getenv("PROMPT_ENTER_DAILY_TIME"); prompt != "" {
		cfg.PromptEnterDailyTime = prompt
	}
	if prompt := os.Getenv("PROMPT_ENTER_SUMMARY_INTERVAL"); prompt != "" {
		cfg.PromptEnterSummaryInterval = prompt
	}
	// --- Конец загрузки промптов ---
	// --- Загрузка настроек хранилища ---
	if useS3Str := os.Getenv("USE_S3_STORAGE"); useS3Str != "" {
		cfg.UseS3Storage, _ = strconv.ParseBool(useS3Str) // Игнорируем ошибку, останется false
	}
	// --- Загрузка S3 настроек удалена ---

	// --- Загрузка НОВЫХ настроек Qdrant ---
	if qdrantEndpoint := os.Getenv("QDRANT_ENDPOINT"); qdrantEndpoint != "" {
		cfg.QdrantEndpoint = qdrantEndpoint
	}
	cfg.QdrantAPIKey = os.Getenv("QDRANT_API_KEY") // Может быть пустым
	if qdrantCollection := os.Getenv("QDRANT_COLLECTION"); qdrantCollection != "" {
		cfg.QdrantCollection = qdrantCollection
	}
	// --- Конец загрузки ---

	// --- НОВЫЕ --- Загрузка настроек импорта
	if oldDataDir := os.Getenv("OLD_DATA_DIR"); oldDataDir != "" {
		cfg.OldDataDir = oldDataDir
	}
	// --- Конец загрузки ---

	// Загрузка числовых значений
	if contextWindowStr := os.Getenv("CONTEXT_WINDOW"); contextWindowStr != "" {
		if val, err := strconv.Atoi(contextWindowStr); err == nil && val > 0 {
			cfg.ContextWindow = val
		}
	}
	if relevantCountStr := os.Getenv("CONTEXT_RELEVANT_MESSAGES_COUNT"); relevantCountStr != "" {
		if val, err := strconv.Atoi(relevantCountStr); err == nil && val > 0 {
			cfg.ContextRelevantMessagesCount = val
		}
	}
	if minMsgStr := os.Getenv("MIN_MESSAGES"); minMsgStr != "" {
		if val, err := strconv.Atoi(minMsgStr); err == nil && val > 0 {
			cfg.MinMessages = val
		}
	}
	if maxMsgStr := os.Getenv("MAX_MESSAGES"); maxMsgStr != "" {
		if val, err := strconv.Atoi(maxMsgStr); err == nil && val >= cfg.MinMessages {
			cfg.MaxMessages = val
		}
	}
	if dailyTimeStr := os.Getenv("DAILY_TAKE_TIME"); dailyTimeStr != "" {
		if val, err := strconv.Atoi(dailyTimeStr); err == nil && val >= 0 && val <= 23 {
			cfg.DailyTakeTime = val
		}
	}
	if debugStr := os.Getenv("DEBUG"); debugStr != "" {
		cfg.Debug, _ = strconv.ParseBool(debugStr) // Игнорируем ошибку, останется false
	}
	if summaryIntervalStr := os.Getenv("SUMMARY_INTERVAL_HOURS"); summaryIntervalStr != "" {
		if val, err := strconv.Atoi(summaryIntervalStr); err == nil && val >= 0 {
			cfg.SummaryIntervalHours = val
		}
	}
	if rateLimitCountStr := os.Getenv("DIRECT_REPLY_RATE_LIMIT_COUNT"); rateLimitCountStr != "" {
		if val, err := strconv.Atoi(rateLimitCountStr); err == nil && val >= 0 {
			cfg.DirectReplyRateLimitCount = val
		}
	}
	if rateLimitWindowStr := os.Getenv("DIRECT_REPLY_RATE_LIMIT_WINDOW"); rateLimitWindowStr != "" {
		if duration, err := time.ParseDuration(rateLimitWindowStr); err == nil {
			cfg.DirectReplyRateLimitWindow = duration
		} else {
			log.Printf("Предупреждение: Неверный формат DIRECT_REPLY_RATE_LIMIT_WINDOW: %v. Используется значение по умолчанию.", err)
		}
	}
	if qdrantTimeoutStr := os.Getenv("QDRANT_TIMEOUT_SEC"); qdrantTimeoutStr != "" {
		if val, err := strconv.Atoi(qdrantTimeoutStr); err == nil && val > 0 {
			cfg.QdrantTimeoutSec = val
		}
	}
	// --- НОВЫЕ --- Загрузка флагов оптимизации Qdrant
	if onDiskStr := os.Getenv("QDRANT_ON_DISK"); onDiskStr != "" {
		cfg.QdrantOnDisk, _ = strconv.ParseBool(onDiskStr)
	}
	if quantOnStr := os.Getenv("QDRANT_QUANTIZATION_ON"); quantOnStr != "" {
		cfg.QdrantQuantizationOn, _ = strconv.ParseBool(quantOnStr)
	}
	if quantRamStr := os.Getenv("QDRANT_QUANTIZATION_RAM"); quantRamStr != "" {
		cfg.QdrantQuantizationRam, _ = strconv.ParseBool(quantRamStr)
	}

	// --- НОВЫЕ --- Загрузка флага импорта и размера чанка
	if importOldStr := os.Getenv("IMPORT_OLD_DATA_ON_START"); importOldStr != "" {
		cfg.ImportOldDataOnStart, _ = strconv.ParseBool(importOldStr)
	}
	if importChunkSizeStr := os.Getenv("IMPORT_CHUNK_SIZE"); importChunkSizeStr != "" {
		if val, err := strconv.Atoi(importChunkSizeStr); err == nil && val > 0 {
			cfg.ImportChunkSize = val
		}
	}

	// Проверка обязательных полей
	if cfg.TelegramToken == "" {
		return nil, fmt.Errorf("переменная окружения TELEGRAM_TOKEN не установлена")
	}
	if cfg.GeminiAPIKey == "" {
		return nil, fmt.Errorf("переменная окружения GEMINI_API_KEY не установлена")
	}

	return cfg, nil
}
