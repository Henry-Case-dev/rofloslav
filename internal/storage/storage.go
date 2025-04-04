package storage

import (
	"fmt"
	"log"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"github.com/Henry-Case-dev/rofloslav/internal/config"
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
func NewHistoryStorage(cfg *config.Config) (HistoryStorage, error) {
	if cfg.UseS3Storage {
		log.Println("[Storage Factory] Используется конфигурация S3 хранилища.")
		// Проверяем наличие обязательных S3 параметров
		if cfg.S3Endpoint == "" || cfg.S3BucketName == "" || cfg.S3AccessKeyID == "" || cfg.S3SecretAccessKey == "" {
			log.Println("[Storage Factory ERROR] Не заданы все необходимые параметры S3 (Endpoint, BucketName, AccessKeyID, SecretAccessKey). Используется LocalStorage.")
			// Возвращаем LocalStorage как запасной вариант
			// ИСПРАВЛЕНО: Обрабатываем ошибку от NewLocalStorage
			localStorage, err := NewLocalStorage(cfg.ContextWindow)
			if err != nil {
				log.Printf("[Storage Factory ERROR] Ошибка инициализации запасного LocalStorage: %v", err)
				// Возвращаем ошибку, т.к. не смогли создать даже запасной вариант
				return nil, fmt.Errorf("ошибка инициализации запасного LocalStorage: %w", err)
			}
			return localStorage, nil // Возвращаем созданный LocalStorage
		}

		s3Store, err := NewS3Storage(cfg, cfg.ContextWindow)
		if err != nil {
			log.Printf("[Storage Factory ERROR] Ошибка инициализации S3 хранилища: %v. Используется LocalStorage.", err)
			// Возвращаем LocalStorage как запасной вариант
			// ИСПРАВЛЕНО: Обрабатываем ошибку от NewLocalStorage
			localStorage, localErr := NewLocalStorage(cfg.ContextWindow)
			if localErr != nil {
				log.Printf("[Storage Factory ERROR] Ошибка инициализации запасного LocalStorage после ошибки S3: %v", localErr)
				// Возвращаем исходную ошибку S3 + ошибку запасного варианта
				return nil, fmt.Errorf("ошибка инициализации S3 (%v) и запасного LocalStorage (%w)", err, localErr)
			}
			return localStorage, nil // Возвращаем созданный LocalStorage
		}
		log.Println("[Storage Factory] S3 хранилище успешно инициализировано.")
		return s3Store, nil
	} else {
		log.Println("[Storage Factory] Используется конфигурация LocalStorage.")
		// Возвращаем LocalStorage
		// ИСПРАВЛЕНО: Обрабатываем ошибку от NewLocalStorage
		localStorage, err := NewLocalStorage(cfg.ContextWindow)
		if err != nil {
			log.Printf("[Storage Factory ERROR] Ошибка инициализации LocalStorage: %v", err)
			return nil, fmt.Errorf("ошибка инициализации LocalStorage: %w", err)
		}
		return localStorage, nil
	}
}
