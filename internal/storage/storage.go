package storage

import (
	"log"
	"sync"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
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

// Storage предоставляет хранилище для истории сообщений (ТЕПЕРЬ ИСПОЛЬЗУЕТСЯ ДЛЯ ОБЩЕЙ ЛОГИКИ В ПАМЯТИ)
type Storage struct {
	messages      map[int64][]*tgbotapi.Message
	contextWindow int
	mutex         sync.RWMutex
	autoSave      bool           // Нужен ли вызов SaveChatHistory после изменений
	storageImpl   HistoryStorage // Ссылка на себя или S3/Local реализацию для вызова Save/Load
}

// New создает новое хранилище
func New(contextWindow int, autoSave bool) *Storage {
	return &Storage{
		messages:      make(map[int64][]*tgbotapi.Message),
		contextWindow: contextWindow,
		mutex:         sync.RWMutex{},
		autoSave:      autoSave,
	}
}

// AddMessage добавляет сообщение в хранилище и инициирует автосохранение
func (s *Storage) AddMessage(chatID int64, message *tgbotapi.Message) {
	s.mutex.Lock()

	// Создаем историю для чата, если ее еще нет
	if _, exists := s.messages[chatID]; !exists {
		s.messages[chatID] = make([]*tgbotapi.Message, 0)
	}

	// Добавляем сообщение
	s.messages[chatID] = append(s.messages[chatID], message)

	// Удаляем старые сообщения, если превышен контекстный лимит
	if len(s.messages[chatID]) > s.contextWindow {
		s.messages[chatID] = s.messages[chatID][len(s.messages[chatID])-s.contextWindow:]
	}

	s.mutex.Unlock() // Разблокируем перед возможным запуском горутины сохранения

	// --- ОПТИМИЗАЦИЯ: Отключаем сохранение на каждое сообщение ---
	// Это сохранение избыточно, т.к. есть периодическое сохранение
	// и сохранение при выходе. Оставляем только для явного вызова
	// или периодического сохранения.
	/*
		// Автосохранение истории чата в файл
		if s.autoSave {
			// Логируем попытку запуска сохранения
			log.Printf("[History Save] Чат %d: Инициирую сохранение истории (autoSave=true)", chatID)
			go func(cid int64) {
				if err := s.SaveChatHistory(cid); err != nil {
					log.Printf("[History Save ERROR] Чат %d: Ошибка автосохранения: %v", cid, err)
				} else {
					// Логируем успешное завершение горутины сохранения
					log.Printf("[History Save OK] Чат %d: Горутина автосохранения завершена успешно.", cid)
				}
			}(chatID)
		} else {
			log.Printf("[History Save] Чат %d: Автосохранение отключено (autoSave=false)", chatID)
		}
	*/
}

// GetMessages возвращает историю сообщений для чата
func (s *Storage) GetMessages(chatID int64) []*tgbotapi.Message {
	s.mutex.RLock()
	defer s.mutex.RUnlock()

	if messages, exists := s.messages[chatID]; exists {
		return messages
	}

	return []*tgbotapi.Message{}
}

// GetMessagesSince возвращает сообщения начиная с указанного времени
func (s *Storage) GetMessagesSince(chatID int64, since time.Time) []*tgbotapi.Message {
	s.mutex.RLock()
	defer s.mutex.RUnlock()

	if messages, exists := s.messages[chatID]; exists {
		result := make([]*tgbotapi.Message, 0)
		for _, msg := range messages {
			if msg.Time().After(since) {
				result = append(result, msg)
			}
		}
		return result
	}

	return []*tgbotapi.Message{}
}

// AddMessagesToContext добавляет массив сообщений в контекст чата
func (s *Storage) AddMessagesToContext(chatID int64, messages []*tgbotapi.Message) {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	// Создаем историю для чата, если ее еще нет
	if _, exists := s.messages[chatID]; !exists {
		s.messages[chatID] = make([]*tgbotapi.Message, 0)
	}

	// Добавляем сообщения
	s.messages[chatID] = append(s.messages[chatID], messages...)

	// Удаляем старые сообщения, если превышен контекстный лимит
	if len(s.messages[chatID]) > s.contextWindow {
		s.messages[chatID] = s.messages[chatID][len(s.messages[chatID])-s.contextWindow:]
	}

	// Автосохранение истории чата в файл
	// Это может вызываться при загрузке истории.
	if s.autoSave {
		go func(cid int64) {
			if err := s.SaveChatHistory(cid); err != nil {
				log.Printf("Ошибка автосохранения истории чата %d: %v", cid, err)
			}
		}(chatID)
	}
}

// ClearChatHistory очищает историю сообщений для указанного чата в памяти
func (s *Storage) ClearChatHistory(chatID int64) {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	delete(s.messages, chatID)
	log.Printf("[ClearChatHistory] Чат %d: История в памяти очищена.", chatID)
}
