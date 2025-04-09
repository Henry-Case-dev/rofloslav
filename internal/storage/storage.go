package storage

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// ChatHistoryStorage определяет общий интерфейс для хранения истории сообщений.
// Это позволяет использовать разные бэкенды (файлы, PostgreSQL).
type ChatHistoryStorage interface {
	// AddMessage добавляет одно сообщение в историю чата.
	AddMessage(chatID int64, message *tgbotapi.Message)

	// GetMessages возвращает все сообщения из истории чата (в пределах окна контекста).
	GetMessages(chatID int64) []*tgbotapi.Message

	// GetMessagesSince возвращает сообщения из истории чата, начиная с указанного времени.
	GetMessagesSince(chatID int64, since time.Time) []*tgbotapi.Message

	// LoadChatHistory загружает историю сообщений для чата из постоянного хранилища.
	// Возвращает nil, nil если история не найдена.
	LoadChatHistory(chatID int64) ([]*tgbotapi.Message, error)

	// SaveChatHistory сохраняет текущую историю сообщений чата в постоянное хранилище.
	SaveChatHistory(chatID int64) error

	// ClearChatHistory очищает историю сообщений для чата как в памяти, так и в постоянном хранилище (если применимо).
	ClearChatHistory(chatID int64) error

	// AddMessagesToContext добавляет массив сообщений в контекст чата (в память).
	AddMessagesToContext(chatID int64, messages []*tgbotapi.Message)

	// GetAllChatIDs возвращает список ID всех чатов, для которых есть история.
	// Необходимо для периодического сохранения.
	GetAllChatIDs() ([]int64, error)

	// Close закрывает соединение с хранилищем (например, с БД), если это необходимо.
	Close() error
}

// FileStorage реализует ChatHistoryStorage с использованием файлов.
// Переименовано из Storage.
type FileStorage struct {
	messages      map[int64][]*tgbotapi.Message
	contextWindow int
	mutex         sync.RWMutex
	autoSave      bool
}

// Убедимся, что FileStorage реализует интерфейс ChatHistoryStorage.
var _ ChatHistoryStorage = (*FileStorage)(nil)

// NewFileStorage создает новое файловое хранилище.
// Переименовано из New.
func NewFileStorage(contextWindow int, autoSave bool) *FileStorage {
	return &FileStorage{
		messages:      make(map[int64][]*tgbotapi.Message),
		contextWindow: contextWindow,
		mutex:         sync.RWMutex{},
		autoSave:      autoSave,
	}
}

// AddMessage добавляет сообщение в файловое хранилище.
func (fs *FileStorage) AddMessage(chatID int64, message *tgbotapi.Message) {
	fs.mutex.Lock()
	defer fs.mutex.Unlock()

	// Создаем историю для чата, если ее еще нет
	if _, exists := fs.messages[chatID]; !exists {
		fs.messages[chatID] = make([]*tgbotapi.Message, 0)
	}

	// Добавляем сообщение
	fs.messages[chatID] = append(fs.messages[chatID], message)

	// Удаляем старые сообщения, если превышен контекстный лимит
	if len(fs.messages[chatID]) > fs.contextWindow {
		fs.messages[chatID] = fs.messages[chatID][len(fs.messages[chatID])-fs.contextWindow:]
	}

	// Убрали автосохранение на каждое сообщение
	// Сохранение будет происходить периодически или при выходе
}

// GetMessages возвращает историю сообщений для чата из файлового хранилища.
func (fs *FileStorage) GetMessages(chatID int64) []*tgbotapi.Message {
	fs.mutex.RLock()
	defer fs.mutex.RUnlock()

	if messages, exists := fs.messages[chatID]; exists {
		// Возвращаем копию среза, чтобы избежать гонок при модификации вне мьютекса
		// (хотя GetMessages обычно только читает, но для безопасности)
		result := make([]*tgbotapi.Message, len(messages))
		copy(result, messages)
		return result
	}

	return []*tgbotapi.Message{}
}

// GetMessagesSince возвращает сообщения начиная с указанного времени из файлового хранилища.
func (fs *FileStorage) GetMessagesSince(chatID int64, since time.Time) []*tgbotapi.Message {
	fs.mutex.RLock()
	defer fs.mutex.RUnlock()

	if messages, exists := fs.messages[chatID]; exists {
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

// AddMessagesToContext добавляет массив сообщений в контекст чата файлового хранилища.
func (fs *FileStorage) AddMessagesToContext(chatID int64, messages []*tgbotapi.Message) {
	fs.mutex.Lock()
	defer fs.mutex.Unlock()

	// Создаем историю для чата, если ее еще нет
	if _, exists := fs.messages[chatID]; !exists {
		fs.messages[chatID] = make([]*tgbotapi.Message, 0)
	}

	// Добавляем сообщения
	fs.messages[chatID] = append(fs.messages[chatID], messages...)

	// Удаляем старые сообщения, если превышен контекстный лимит
	if len(fs.messages[chatID]) > fs.contextWindow {
		fs.messages[chatID] = fs.messages[chatID][len(fs.messages[chatID])-fs.contextWindow:]
	}

	// Автосохранение здесь убрано, т.к. вызывается только при загрузке истории
	// Периодическое сохранение позаботится об этом
}

// ClearChatHistory очищает историю сообщений для указанного чата в файловом хранилище (память и файл).
func (fs *FileStorage) ClearChatHistory(chatID int64) error {
	fs.mutex.Lock()
	delete(fs.messages, chatID)
	fs.mutex.Unlock()
	log.Printf("[FileStorage Clear] Чат %d: История в памяти очищена.", chatID)

	// Также удаляем соответствующий файл
	err := fs.deleteChatHistoryFile(chatID)
	if err != nil {
		// Логируем ошибку, но не считаем это фатальным
		log.Printf("[FileStorage Clear WARN] Чат %d: Ошибка при удалении файла истории: %v", chatID, err)
	}
	return nil // Интерфейс требует возвращать ошибку, но в данном случае удаление файла не критично
}

// Close для FileStorage (ничего не делает)
func (fs *FileStorage) Close() error {
	return nil
}

// getChatHistoryFilename возвращает имя файла для хранения истории чата.
func (fs *FileStorage) getChatHistoryFilename(chatID int64) string {
	// Используем директорию /data, создавая ее при необходимости
	historyDir := "/data"
	if err := os.MkdirAll(historyDir, 0755); err != nil {
		// В случае ошибки создания директории, логируем и возвращаем путь внутри нее
		// Функции записи/чтения обработают ошибку доступа, если она критична
		log.Printf("[getChatHistoryFilename WARN] Чат %d: Ошибка создания/доступа к директории %s: %v", chatID, historyDir, err)
	}
	return filepath.Join(historyDir, fmt.Sprintf("chat_%d.json", chatID))
}

// deleteChatHistoryFile удаляет файл истории для указанного чата.
func (fs *FileStorage) deleteChatHistoryFile(chatID int64) error {
	filePath := fs.getChatHistoryFilename(chatID) // Теперь использует /data
	err := os.Remove(filePath)
	if err != nil {
		// Если файл не найден, это не ошибка в контексте очистки
		if os.IsNotExist(err) {
			log.Printf("[FileStorage DeleteFile] Чат %d: Файл истории %s не найден, удалять нечего.", chatID, filePath)
			return nil
		}
		log.Printf("[FileStorage DeleteFile ERROR] Чат %d: Не удалось удалить файл истории %s: %v", chatID, filePath, err)
		return fmt.Errorf("ошибка удаления файла истории чата %d: %w", chatID, err)
	}
	log.Printf("[FileStorage DeleteFile] Чат %d: Файл истории %s успешно удален.", chatID, filePath)
	return nil
}

// GetAllChatIDs возвращает список ID всех чатов, для которых есть история в памяти.
func (fs *FileStorage) GetAllChatIDs() ([]int64, error) {
	fs.mutex.RLock()
	defer fs.mutex.RUnlock()
	ids := make([]int64, 0, len(fs.messages))
	for id := range fs.messages {
		ids = append(ids, id)
	}
	return ids, nil
}
