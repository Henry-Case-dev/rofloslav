package storage

import (
	// Добавим для sql.NullString в Postgres

	// Keep for Postgres potentially

	// Keep for Postgres potentially

	// Added for checking nil pointer inside EnsureIndexes

	"context"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	_ "github.com/lib/pq"                        // Postgres driver, keep if postgres is still an option
	"go.mongodb.org/mongo-driver/bson/primitive" // Добавим для MongoDB ID
)

// UserProfile содержит информацию о пользователе чата.
type UserProfile struct {
	ID       primitive.ObjectID `bson:"_id,omitempty"`            // ID для MongoDB
	ChatID   int64              `bson:"chat_id" db:"chat_id"`     // ID чата, к которому привязан профиль
	UserID   int64              `bson:"user_id" db:"user_id"`     // ID пользователя Telegram
	Username string             `bson:"username" db:"username"`   // Никнейм Telegram (@username)
	Alias    string             `bson:"alias" db:"alias"`         // Прозвище / Короткое имя (ранее FirstName)
	Gender   string             `bson:"gender" db:"gender"`       // Пол (ранее LastName)
	RealName string             `bson:"real_name" db:"real_name"` // Реальное имя (если известно)
	Bio      string             `bson:"bio" db:"bio"`             // Редактируемое описание/бэкграунд
	LastSeen time.Time          `bson:"last_seen" db:"last_seen"` // Время последнего сообщения (для актуальности)
	// Можно добавить другие поля по необходимости
	CreatedAt time.Time `bson:"created_at" db:"created_at"` // Время создания записи
	UpdatedAt time.Time `bson:"updated_at" db:"updated_at"` // Время последнего обновления
	// --- Новые поля для Auto Bio ---
	AutoBio           string    `bson:"auto_bio,omitempty" db:"auto_bio"`
	LastAutoBioUpdate time.Time `bson:"last_auto_bio_update,omitempty" db:"last_auto_bio_update"`
	// --- Конец новых полей ---
}

// ChatHistoryStorage определяет интерфейс для работы с историей сообщений и профилями.
type ChatHistoryStorage interface {
	// === Методы для истории сообщений ===
	AddMessage(chatID int64, message *tgbotapi.Message)
	// GetMessages извлекает последние N сообщений для указанного чата, где N = limit.
	GetMessages(chatID int64, limit int) ([]*tgbotapi.Message, error)
	// GetMessagesSince извлекает сообщения из указанного чата, для указанного пользователя,
	// начиная с определенного времени и с ограничением по количеству.
	GetMessagesSince(ctx context.Context, chatID int64, userID int64, since time.Time, limit int) ([]*tgbotapi.Message, error)
	LoadChatHistory(chatID int64) ([]*tgbotapi.Message, error)
	SaveChatHistory(chatID int64) error
	ClearChatHistory(chatID int64) error
	AddMessagesToContext(chatID int64, messages []*tgbotapi.Message)
	GetAllChatIDs() ([]int64, error)

	// === Методы для профилей пользователей ===
	// GetUserProfile возвращает профиль пользователя для конкретного чата.
	// Возвращает nil, nil если профиль не найден.
	GetUserProfile(chatID int64, userID int64) (*UserProfile, error)

	// SetUserProfile создает или обновляет профиль пользователя для конкретного чата.
	SetUserProfile(profile *UserProfile) error

	// GetAllUserProfiles возвращает все профили пользователей для указанного чата.
	GetAllUserProfiles(chatID int64) ([]*UserProfile, error)

	// === Методы для настроек чатов ===
	// GetChatSettings возвращает настройки для указанного чата.
	GetChatSettings(chatID int64) (*ChatSettings, error)
	// SetChatSettings сохраняет настройки для указанного чата.
	SetChatSettings(settings *ChatSettings) error

	// --- Методы для обновления отдельных настроек лимитов ---
	UpdateDirectLimitEnabled(chatID int64, enabled bool) error
	UpdateDirectLimitCount(chatID int64, count int) error
	UpdateDirectLimitDuration(chatID int64, duration time.Duration) error
	UpdateVoiceTranscriptionEnabled(chatID int64, enabled bool) error
	UpdateSrachAnalysisEnabled(chatID int64, enabled bool) error

	// === Общие методы ===
	// Close закрывает соединение с хранилищем.
	Close() error

	// GetStatus возвращает строку с текущим статусом хранилища (тип, подключение, кол-во сообщений и т.д.)
	GetStatus(chatID int64) string

	// === Методы для работы с эмбеддингами (специфичны для MongoDB) ---

	// GetTotalMessagesCount возвращает общее количество сообщений в чате.
	GetTotalMessagesCount(chatID int64) (int64, error)

	// FindMessagesWithoutEmbedding ищет сообщения без эмбеддингов, исключая указанные ID.
	FindMessagesWithoutEmbedding(chatID int64, limit int, skipMessageIDs []int) ([]MongoMessage, error)

	// UpdateMessageEmbedding обновляет или добавляет эмбеддинг для сообщения.
	UpdateMessageEmbedding(chatID int64, messageID int, vector []float32) error

	// === Методы для долгосрочной памяти ===
	// SearchRelevantMessages ищет сообщения, семантически близкие к queryText, используя векторный поиск.
	// Возвращает до k наиболее релевантных сообщений.
	SearchRelevantMessages(chatID int64, queryText string, k int) ([]*tgbotapi.Message, error)

	// === НОВЫЙ МЕТОД для получения ветки ответов ===
	// GetReplyChain извлекает цепочку сообщений, на которые отвечали, начиная с messageID.
	// Возвращает сообщения в хронологическом порядке (старые -> новые).
	GetReplyChain(ctx context.Context, chatID int64, messageID int, maxDepth int) ([]*tgbotapi.Message, error)

	// === НОВЫЙ МЕТОД для сброса времени AutoBio ===
	// ResetAutoBioTimestamps сбрасывает LastAutoBioUpdate для всех пользователей в чате.
	ResetAutoBioTimestamps(chatID int64) error
}

// FileStorage реализует ChatHistoryStorage с использованием файлов.
// Переименовано из Storage.

// ChatSettings содержит настройки, специфичные для чата, которые сохраняются в БД.
type ChatSettings struct {
	ChatID                    int64    `bson:"chat_id" db:"chat_id"`
	ConversationStyle         string   `bson:"conversation_style,omitempty" db:"conversation_style"`
	Temperature               *float64 `bson:"temperature,omitempty" db:"temperature"` // Указатель, чтобы отличить 0 от отсутствия значения
	Model                     string   `bson:"model,omitempty" db:"model"`
	GeminiSafetyThreshold     string   `bson:"gemini_safety_threshold,omitempty" db:"gemini_safety_threshold"`
	VoiceTranscriptionEnabled *bool    `bson:"voice_transcription_enabled,omitempty" db:"voice_transcription_enabled"` // Включена ли транскрипция ГС
	// --- Новые поля для лимита прямых ответов ---
	DirectReplyLimitEnabled  *bool `bson:"direct_reply_limit_enabled,omitempty" db:"direct_reply_limit_enabled"`                   // Включен ли лимит
	DirectReplyLimitCount    *int  `bson:"direct_reply_limit_count,omitempty" db:"direct_reply_limit_count"`                       // Макс. кол-во обращений
	DirectReplyLimitDuration *int  `bson:"direct_reply_limit_duration_minutes,omitempty" db:"direct_reply_limit_duration_minutes"` // Длительность периода (в минутах)
	// --- Настройка анализа срачей ---
	SrachAnalysisEnabled *bool `bson:"srach_analysis_enabled,omitempty" db:"srach_analysis_enabled"` // Включен ли анализ срачей для чата
	// --- Настройка анализа фотографий ---
	PhotoAnalysisEnabled *bool `bson:"photo_analysis_enabled,omitempty" db:"photo_analysis_enabled"` // Включен ли анализ фотографий для чата
	// Другие настройки чата можно добавить сюда
}

// MongoMessage - структура для хранения сообщений в MongoDB.
// (Перенесена сюда из file_storage.go для централизации общих структур)
type MongoMessage struct {
	ID               primitive.ObjectID       `bson:"_id,omitempty"`
	ChatID           int64                    `bson:"chat_id"`
	MessageID        int                      `bson:"message_id"`
	UserID           int64                    `bson:"user_id,omitempty"`
	Username         string                   `bson:"username,omitempty"`
	FirstName        string                   `bson:"first_name,omitempty"`
	LastName         string                   `bson:"last_name,omitempty"`
	IsBot            bool                     `bson:"is_bot,omitempty"`
	Date             time.Time                `bson:"date"` // Используем time.Time для сортировки
	Text             string                   `bson:"text,omitempty"`
	ReplyToMessageID int                      `bson:"reply_to_message_id,omitempty"`
	Entities         []tgbotapi.MessageEntity `bson:"entities,omitempty"`
	// Новые поля:
	Caption         string                   `bson:"caption,omitempty"`          // Текст подписи к медиа
	CaptionEntities []tgbotapi.MessageEntity `bson:"caption_entities,omitempty"` // Форматирование подписи
	HasMedia        bool                     `bson:"has_media,omitempty"`        // Флаг наличия медиа
	IsVoice         bool                     `bson:"is_voice,omitempty"`         // Флаг, что сообщение из аудио
	MessageVector   []float32                `bson:"message_vector,omitempty"`   // Векторное представление сообщения
	// --- Добавляем поля для информации о пересылке ---
	IsForward              bool      `bson:"is_forward,omitempty"`
	ForwardedFromUserID    int64     `bson:"forwarded_from_user_id,omitempty"`
	ForwardedFromChatID    int64     `bson:"forwarded_from_chat_id,omitempty"` // Если переслано из канала
	ForwardedFromMessageID int       `bson:"forwarded_from_message_id,omitempty"`
	ForwardedDate          time.Time `bson:"forwarded_date,omitempty"`
}

// Убедимся, что все типы хранилищ реализуют интерфейс ChatHistoryStorage.
// Примечание: FileStorage объявляется в file_storage.go, PostgresStorage в postgres_storage.go и т.д.
// Эта проверка вызовет ошибку компиляции, если какой-то тип перестанет соответствовать интерфейсу.
var _ ChatHistoryStorage = (*FileStorage)(nil)
var _ ChatHistoryStorage = (*PostgresStorage)(nil)
var _ ChatHistoryStorage = (*MongoStorage)(nil)
