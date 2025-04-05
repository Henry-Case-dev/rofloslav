package storage

import (
	"fmt"
	"log"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"github.com/Henry-Case-dev/rofloslav/internal/config"
	"github.com/Henry-Case-dev/rofloslav/internal/gemini"
	"github.com/Henry-Case-dev/rofloslav/internal/types"
)

// --- Интерфейс Хранилища ---

// HistoryStorage определяет методы для работы с хранилищем истории чатов.
// Это позволяет использовать разные реализации (локальный файл, S3 и т.д.).
type HistoryStorage interface {
	// AddMessage добавляет одно сообщение в историю чата (в память).
	AddMessage(chatID int64, message *tgbotapi.Message)

	// AddMessagesToContext добавляет несколько сообщений в историю чата (в память).
	AddMessagesToContext(chatID int64, messages []*tgbotapi.Message)

	// GetMessages возвращает текущую историю сообщений для чата из памяти.
	GetMessages(chatID int64) []*tgbotapi.Message

	// GetMessagesSince возвращает сообщения из памяти, начиная с указанного времени.
	GetMessagesSince(chatID int64, since time.Time) []*tgbotapi.Message

	// LoadChatHistory загружает историю для указанного чата из персистентного хранилища (файл/S3).
	// Возвращает nil, nil если история не найдена.
	LoadChatHistory(chatID int64) ([]*tgbotapi.Message, error)

	// SaveChatHistory сохраняет текущую историю из памяти в персистентное хранилище (файл/S3).
	SaveChatHistory(chatID int64) error

	// ClearChatHistory очищает историю для чата из памяти.
	ClearChatHistory(chatID int64)

	// SaveAllChatHistories сохраняет историю всех чатов из памяти в персистентное хранилище.
	SaveAllChatHistories() error

	// LoadAllChatHistories (Опционально, может понадобиться для S3)
	// Загружает историю для всех известных чатов (например, при старте).
	// LoadAllChatHistories() error

	// ImportMessagesFromJSONFile импортирует сообщения из JSON-файла в хранилище.
	// Должен быть идемпотентным (пропускать уже существующие сообщения).
	// Возвращает количество импортированных и пропущенных сообщений.
	ImportMessagesFromJSONFile(chatID int64, filePath string) (importedCount int, skippedCount int, err error)

	// FindRelevantMessages ищет сообщения в истории чата, релевантные заданному тексту.
	// Возвращает до `limit` наиболее релевантных сообщений.
	FindRelevantMessages(chatID int64, queryText string, limit int) ([]types.Message, error)
}

// --- Конец Интерфейса ---

// --- УДАЛЕНА СТАРАЯ СТРУКТУРА Storage и ЕЕ МЕТОДЫ ---
/*
type Storage struct {
	messages      map[int64][]*tgbotapi.Message
	contextWindow int
	autoSave      bool
	storageImpl   HistoryStorage // Ссылка на себя или S3/Local реализацию для вызова Save/Load
}

func New(contextWindow int, autoSave bool) *Storage { ... }
func (s *Storage) AddMessage(...) { ... }
func (s *Storage) GetMessages(...) []*tgbotapi.Message { ... }
func (s *Storage) GetMessagesSince(...) []*tgbotapi.Message { ... }
func (s *Storage) AddMessagesToContext(...) { ... }
func (s *Storage) ClearChatHistory(...) { ... }
*/
// --- КОНЕЦ УДАЛЕННОГО КОДА ---

// --- Структуры для конвертации ---

type StoredMessage struct {
	MessageID      int                      `json:"message_id"`
	FromID         int64                    `json:"from_id"`
	FromIsBot      bool                     `json:"from_is_bot"`
	FromFirstName  string                   `json:"from_first_name"`
	FromLastName   string                   `json:"from_last_name"`
	FromUserName   string                   `json:"from_username"`
	Date           int                      `json:"date"`
	Text           string                   `json:"text"`
	ReplyToMessage *StoredMessage           `json:"reply_to_message,omitempty"`
	Entities       []tgbotapi.MessageEntity `json:"entities,omitempty"`
}

func ConvertToStoredMessage(msg *tgbotapi.Message) *StoredMessage {
	if msg == nil {
		return nil
	}
	stored := &StoredMessage{
		MessageID: msg.MessageID,
		Date:      msg.Date,
		Text:      msg.Text,
		Entities:  msg.Entities,
	}
	if msg.From != nil {
		stored.FromID = msg.From.ID
		stored.FromIsBot = msg.From.IsBot
		stored.FromFirstName = msg.From.FirstName
		stored.FromLastName = msg.From.LastName
		stored.FromUserName = msg.From.UserName
	}
	if msg.ReplyToMessage != nil {
		stored.ReplyToMessage = ConvertToStoredMessage(msg.ReplyToMessage)
	}
	return stored
}

func ConvertToAPIMessage(stored *StoredMessage) *tgbotapi.Message {
	if stored == nil {
		return nil
	}
	msg := &tgbotapi.Message{
		MessageID: stored.MessageID,
		From: &tgbotapi.User{
			ID:        stored.FromID,
			IsBot:     stored.FromIsBot,
			FirstName: stored.FromFirstName,
			LastName:  stored.FromLastName,
			UserName:  stored.FromUserName,
		},
		Date:     stored.Date,
		Text:     stored.Text,
		Entities: stored.Entities,
	}
	if stored.ReplyToMessage != nil {
		msg.ReplyToMessage = ConvertToAPIMessage(stored.ReplyToMessage)
	}
	return msg
}

// --- Конец структур ---

// --- Фабричная функция ---

// NewHistoryStorage создает и возвращает подходящую реализацию HistoryStorage
// на основе конфигурации.
func NewHistoryStorage(cfg *config.Config, geminiClient *gemini.Client) (HistoryStorage, error) {
	// Пока что принудительно используем QdrantStorage
	// В будущем можно добавить флаг в конфиг для выбора
	log.Println("[Storage Factory] Попытка инициализации Qdrant хранилища.")
	qdrantStorage, err := NewQdrantStorage(cfg, geminiClient)
	if err != nil {
		log.Printf("[Storage Factory ERROR] Ошибка инициализации QdrantStorage: %v", err)
		// Можно добавить откат на LocalStorage, если Qdrant недоступен
		log.Printf("[Storage Factory WARN] Ошибка Qdrant, откат на LocalStorage (ДЛЯ ОТЛАДКИ).")
		localStorage, localErr := NewLocalStorage(cfg.ContextWindow)
		if localErr != nil {
			log.Printf("[Storage Factory ERROR] Ошибка инициализации ЗАПАСНОГО LocalStorage: %v", localErr)
			return nil, fmt.Errorf("ошибка инициализации Qdrant (%v) и запасного LocalStorage (%w)", err, localErr)
		}
		log.Println("[Storage Factory] Успешно создан ЗАПАСНОЙ LocalStorage.")
		return localStorage, nil
		// В продакшене, возможно, стоит возвращать ошибку Qdrant:
		// return nil, fmt.Errorf("ошибка инициализации QdrantStorage: %w", err)
	}
	log.Println("[Storage Factory] Qdrant хранилище успешно инициализировано.")
	return qdrantStorage, nil
}
