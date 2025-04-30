package config

import "time"

// LLMProvider определяет тип для выбора LLM провайдера
type LLMProvider string

const (
	// Константы для типов LLM провайдеров
	ProviderGemini     LLMProvider = "gemini"
	ProviderDeepSeek   LLMProvider = "deepseek"
	ProviderOpenRouter LLMProvider = "openrouter"
)

// StorageType определяет тип используемого хранилища.
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
	LLMProvider   LLMProvider
	DefaultPrompt string
	DirectPrompt  string
	// --- Новые промпты для классификации и серьезного ответа ---
	ClassifyDirectMessagePrompt string
	SeriousDirectPrompt         string
	// --- Конец новых промптов ---
	DailyTakePrompt string
	SummaryPrompt   string
	// --- Новые поля для настроек по умолчанию ---
	DefaultConversationStyle string  // Стиль общения по умолчанию
	DefaultTemperature       float64 // Температура по умолчанию
	DefaultModel             string  // Модель LLM по умолчанию // ПРИМЕЧАНИЕ: Это поле больше не используется напрямую для выбора модели LLM! Используются специфичные для провайдера поля ниже.
	DefaultSafetyThreshold   string  // Уровень безопасности Gemini по умолчанию
	// --- Конец новых полей ---
	// Настройки Gemini
	GeminiAPIKey    string
	GeminiModelName string
	// --- Настройки резервного ключа Gemini ---
	GeminiAPIKeyReserve        string    // Резервный ключ API Gemini
	GeminiUsingReserveKey      bool      // Флаг использования резервного ключа
	GeminiKeyRotationTimeHours int       // Время в часах, через которое пробовать вернуться к основному ключу
	GeminiLastKeyRotationTime  time.Time // Время последнего переключения ключа
	// --- Конец настроек резервного ключа ---
	// Настройки DeepSeek
	DeepSeekAPIKey    string
	DeepSeekModelName string
	DeepSeekBaseURL   string // Опционально, для кастомного URL
	// --- НОВЫЕ Настройки OpenRouter ---
	OpenRouterAPIKey    string
	OpenRouterModelName string
	OpenRouterSiteURL   string // Optional HTTP-Referer
	OpenRouterSiteTitle string // Optional X-Title
	// --- КОНЕЦ Настроек OpenRouter ---
	// Настройки поведения бота
	RateLimitStaticText string // Статический текст для сообщения о лимите
	RateLimitPrompt     string // Промпт для LLM для сообщения о лимите
	// --- Настройки донатов ---
	DonatePrompt    string // Промпт для генерации сообщения о донате
	DonateTimeHours int    // Интервал отправки сообщений о донате в часах
	// --- Конец настроек донатов ---
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
	// --- Настройки автоудаления сообщений об ошибках ---
	ErrorMessageAutoDeleteSeconds int // Время в секундах до автоудаления сообщений об ошибках
	// --- Конец настроек автоудаления ---
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
	// --- Настройки лимита прямых обращений (дефолтные) ---
	DirectReplyLimitEnabledDefault  bool
	DirectReplyLimitCountDefault    int
	DirectReplyLimitDurationDefault time.Duration // Храним сразу как Duration
	DirectReplyLimitPrompt          string
	PromptEnterDirectLimitCount     string
	PromptEnterDirectLimitDuration  string
	// --- Настройки долгосрочной памяти ---
	LongTermMemoryEnabled    bool   // Включить/выключить долгосрочную память
	GeminiEmbeddingModelName string // Модель Gemini для создания эмбеддингов
	MongoVectorIndexName     string // Имя векторного индекса в MongoDB Atlas
	LongTermMemoryFetchK     int    // Сколько релевантных сообщений извлекать
	// --- Настройки бэкфилла эмбеддингов ---
	BackfillBatchSize  int           // Размер пакета для бэкфилла
	BackfillBatchDelay time.Duration // Задержка между пакетами бэкфилла
	// --- Настройки для обработки фотографий ---
	PhotoAnalysisEnabled bool   // Включить/выключить анализ фотографий с помощью Gemini
	PhotoAnalysisPrompt  string // Промпт для анализа изображений через Gemini
	// --- Настройки автоочистки MongoDB ---
	MongoCleanupEnabled            bool // Включить/выключить автоочистку MongoDB
	MongoCleanupSizeLimitMB        int  // Максимальный размер коллекции в МБ перед очисткой
	MongoCleanupIntervalMinutes    int  // Интервал проверки коллекций в минутах
	MongoCleanupChunkDurationHours int  // Длительность удаляемого "куска" старых сообщений в часах
	// --- НОВЫЕ Настройки Auto Bio ---
	AutoBioEnabled                bool   // Включен ли автоматический анализ профилей
	AutoBioIntervalHours          int    // Интервал анализа в часах
	AutoBioInitialAnalysisPrompt  string // Промпт для первого анализа
	AutoBioUpdatePrompt           string // Промпт для обновления существующего био
	AutoBioMessagesLookbackDays   int    // На сколько дней назад смотреть сообщения при первом анализе
	AutoBioMinMessagesForAnalysis int    // Мин. кол-во сообщений пользователя для анализа
	AutoBioMaxMessagesForAnalysis int    // Макс. кол-во сообщений пользователя для анализа (для LLM)
	// --- КОНЕЦ Настроек Auto Bio ---
}
