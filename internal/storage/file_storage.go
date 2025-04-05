// internal/storage/file_storage.go
package storage

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	// Добавляем импорт types
	"github.com/Henry-Case-dev/rofloslav/internal/types"
	// config не нужен здесь, т.к. contextWindow передается при создании
)

// --- LocalStorage ---

// LocalStorage реализует HistoryStorage с использованием локальной файловой системы.
type LocalStorage struct {
	messages      map[int64][]*tgbotapi.Message
	contextWindow int
	dataDir       string // Путь к директории для сохранения файлов
	mutex         sync.RWMutex
}

// NewLocalStorage создает новый экземпляр LocalStorage.
func NewLocalStorage(contextWindow int) (*LocalStorage, error) {
	// Определяем директорию для данных. В Docker это будет /data
	dataDir := os.Getenv("DATA_DIR")
	if dataDir == "" {
		dataDir = "data" // Используем локальную папку data, если переменная не задана
	}

	log.Printf("[LocalStorage] Инициализация с dataDir: %s", dataDir)

	// Убедимся, что директория существует
	if err := ensureDataDir(dataDir); err != nil {
		return nil, fmt.Errorf("ошибка создания директории %s: %w", dataDir, err)
	}

	ls := &LocalStorage{
		messages:      make(map[int64][]*tgbotapi.Message),
		contextWindow: contextWindow,
		dataDir:       dataDir,
		mutex:         sync.RWMutex{},
	}

	// Загружаем существующие истории при старте
	log.Printf("[LocalStorage] Загрузка существующих историй из %s...", dataDir)
	err := ls.loadAllChatHistories()
	if err != nil {
		// Не фатальная ошибка, просто логируем
		log.Printf("[LocalStorage WARN] Не удалось загрузить все истории при старте: %v", err)
	} else {
		log.Printf("[LocalStorage] Загрузка историй из файлов завершена.")
	}

	return ls, nil
}

// ensureDataDir проверяет и при необходимости создает директорию для данных.
func ensureDataDir(dirPath string) error {
	err := os.MkdirAll(dirPath, 0755) // 0755 - стандартные права доступа
	if err != nil && !os.IsExist(err) {
		log.Printf("[LocalStorage ERROR] Не удалось создать директорию %s: %v", dirPath, err)
		return err
	}
	log.Printf("[LocalStorage] Директория %s готова.", dirPath)
	return nil
}

// --- Реализация интерфейса HistoryStorage ---

// AddMessage добавляет сообщение в память и обрезает историю.
func (ls *LocalStorage) AddMessage(chatID int64, message *tgbotapi.Message) {
	ls.mutex.Lock()
	defer ls.mutex.Unlock()

	if _, exists := ls.messages[chatID]; !exists {
		ls.messages[chatID] = make([]*tgbotapi.Message, 0)
	}
	ls.messages[chatID] = append(ls.messages[chatID], message)
	if len(ls.messages[chatID]) > ls.contextWindow {
		ls.messages[chatID] = ls.messages[chatID][len(ls.messages[chatID])-ls.contextWindow:]
	}
}

// AddMessagesToContext добавляет несколько сообщений в память.
func (ls *LocalStorage) AddMessagesToContext(chatID int64, messages []*tgbotapi.Message) {
	ls.mutex.Lock()
	defer ls.mutex.Unlock()

	if _, exists := ls.messages[chatID]; !exists {
		ls.messages[chatID] = make([]*tgbotapi.Message, 0)
	}
	ls.messages[chatID] = append(ls.messages[chatID], messages...)
	if len(ls.messages[chatID]) > ls.contextWindow {
		ls.messages[chatID] = ls.messages[chatID][len(ls.messages[chatID])-ls.contextWindow:]
	}
}

// GetMessages возвращает сообщения из памяти.
func (ls *LocalStorage) GetMessages(chatID int64) []*tgbotapi.Message {
	ls.mutex.RLock()
	defer ls.mutex.RUnlock()
	if messages, exists := ls.messages[chatID]; exists {
		// Возвращаем копию, чтобы избежать гонки данных при модификации вне хранилища
		msgsCopy := make([]*tgbotapi.Message, len(messages))
		copy(msgsCopy, messages)
		return msgsCopy
	}
	return []*tgbotapi.Message{} // Возвращаем пустой срез, а не nil
}

// GetMessagesSince возвращает сообщения из памяти с указанного времени.
func (ls *LocalStorage) GetMessagesSince(chatID int64, since time.Time) []*tgbotapi.Message {
	ls.mutex.RLock()
	defer ls.mutex.RUnlock()
	result := make([]*tgbotapi.Message, 0)
	if messages, exists := ls.messages[chatID]; exists {
		for _, msg := range messages {
			if time.Unix(int64(msg.Date), 0).After(since) {
				result = append(result, msg)
			}
		}
	}
	return result
}

// ClearChatHistory очищает историю чата в памяти и удаляет файл.
func (ls *LocalStorage) ClearChatHistory(chatID int64) {
	ls.mutex.Lock()
	delete(ls.messages, chatID)
	ls.mutex.Unlock() // Разблокируем перед удалением файла

	filePath := ls.getFilePath(chatID)
	err := os.Remove(filePath)
	if err != nil && !os.IsNotExist(err) {
		log.Printf("[LocalStorage WARN] Чат %d: Не удалось удалить файл истории %s: %v", chatID, filePath, err)
	} else if err == nil {
		log.Printf("[LocalStorage] Чат %d: История в памяти очищена и файл %s удален.", chatID, filePath)
	} else {
		log.Printf("[LocalStorage] Чат %d: История в памяти очищена (файл %s не найден).", chatID, filePath)
	}
}

// --- Функции Load/Save для файлов ---

func (ls *LocalStorage) getFilePath(chatID int64) string {
	return filepath.Join(ls.dataDir, fmt.Sprintf("chat_%d.json", chatID))
}

// LoadChatHistory загружает историю из файла.
func (ls *LocalStorage) LoadChatHistory(chatID int64) ([]*tgbotapi.Message, error) {
	filePath := ls.getFilePath(chatID)
	// log.Printf("[LocalStorage] Загружаю историю для чата %d из файла: %s", chatID, filePath)

	data, err := ioutil.ReadFile(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			// log.Printf("[LocalStorage] Файл истории %s не найден.", filePath)
			return nil, nil // Не ошибка, просто нет истории
		}
		log.Printf("[LocalStorage ERROR] Ошибка чтения файла %s: %v", filePath, err)
		return nil, fmt.Errorf("ошибка чтения файла истории: %w", err)
	}

	if len(data) == 0 || string(data) == "null" {
		// log.Printf("[LocalStorage] Файл %s пуст или содержит null.", filePath)
		ls.ClearChatHistory(chatID) // Очищаем память, если файл пуст
		return []*tgbotapi.Message{}, nil
	}

	var storedMessages []*StoredMessage
	if err := json.Unmarshal(data, &storedMessages); err != nil {
		log.Printf("[LocalStorage ERROR] Ошибка десериализации JSON из файла %s: %v", filePath, err)
		// Попытаемся переименовать поврежденный файл
		backupPath := filePath + ".corrupted." + time.Now().Format("20060102150405")
		if renameErr := os.Rename(filePath, backupPath); renameErr == nil {
			log.Printf("[LocalStorage INFO] Поврежденный файл %s переименован в %s", filePath, backupPath)
		} else {
			log.Printf("[LocalStorage ERROR] Не удалось переименовать поврежденный файл %s: %v", filePath, renameErr)
		}
		return nil, fmt.Errorf("ошибка десериализации истории: %w", err)
	}

	var messages []*tgbotapi.Message
	for _, stored := range storedMessages {
		apiMsg := ConvertToAPIMessage(stored) // Используем конвертер из storage.go
		if apiMsg != nil {
			messages = append(messages, apiMsg)
		} else {
			log.Printf("[LocalStorage WARN] Чат %d: Не удалось конвертировать StoredMessage ID %d из файла %s", chatID, stored.MessageID, filePath)
		}
	}
	log.Printf("[LocalStorage OK] Чат %d: Успешно загружено %d сообщений из %s.", chatID, len(messages), filePath)

	// Обновляем кеш в памяти
	ls.mutex.Lock()
	ls.messages[chatID] = messages
	ls.mutex.Unlock()

	return messages, nil
}

// SaveChatHistory сохраняет историю чата (из памяти) в файл.
func (ls *LocalStorage) SaveChatHistory(chatID int64) error {
	ls.mutex.RLock()
	messages, exists := ls.messages[chatID]
	ls.mutex.RUnlock()

	if !exists || len(messages) == 0 {
		// log.Printf("[LocalStorage] Чат %d: Нет сообщений в памяти для сохранения.", chatID)
		// Если сообщений нет, можно удалить файл, чтобы не хранить пустые.
		// Но LoadChatHistory уже обрабатывает пустые файлы, так что можно и не удалять.
		// Пока просто выходим.
		return nil
	}

	filePath := ls.getFilePath(chatID)
	// log.Printf("[LocalStorage] Сохраняю историю для чата %d в файл: %s (%d сообщений)", chatID, filePath, len(messages))

	var storedMessages []*StoredMessage
	for _, msg := range messages {
		stored := ConvertToStoredMessage(msg) // Используем конвертер из storage.go
		if stored != nil {
			storedMessages = append(storedMessages, stored)
		}
	}

	data, err := json.MarshalIndent(storedMessages, "", "  ")
	if err != nil {
		log.Printf("[LocalStorage ERROR] Чат %d: Ошибка маршалинга JSON: %v", chatID, err)
		return fmt.Errorf("ошибка маршалинга истории: %w", err)
	}

	// Атомарная запись: сначала пишем во временный файл, потом переименовываем
	tempFilePath := filePath + ".tmp"
	err = ioutil.WriteFile(tempFilePath, data, 0644) // 0644 - стандартные права
	if err != nil {
		log.Printf("[LocalStorage ERROR] Чат %d: Ошибка записи во временный файл %s: %v", chatID, tempFilePath, err)
		return fmt.Errorf("ошибка записи временного файла истории: %w", err)
	}

	// Переименовываем временный файл в основной
	err = os.Rename(tempFilePath, filePath)
	if err != nil {
		log.Printf("[LocalStorage ERROR] Чат %d: Ошибка переименования файла %s -> %s: %v", chatID, tempFilePath, filePath, err)
		// Попытаемся удалить временный файл, если переименование не удалось
		_ = os.Remove(tempFilePath)
		return fmt.Errorf("ошибка переименования файла истории: %w", err)
	}

	// log.Printf("[LocalStorage OK] Чат %d: История (%d сообщ.) записана в %s.", chatID, len(storedMessages), filePath)
	return nil
}

// SaveAllChatHistories сохраняет все чаты из памяти в файлы.
func (ls *LocalStorage) SaveAllChatHistories() error {
	ls.mutex.RLock()
	chatIDs := make([]int64, 0, len(ls.messages))
	for id := range ls.messages {
		chatIDs = append(chatIDs, id)
	}
	ls.mutex.RUnlock()

	if len(chatIDs) == 0 {
		log.Printf("[LocalStorage] Нет чатов в памяти для сохранения.")
		return nil
	}

	log.Printf("[LocalStorage] Начинаю сохранение истории для %d чатов...", len(chatIDs))
	var wg sync.WaitGroup
	var firstError error
	var errMutex sync.Mutex

	for _, id := range chatIDs {
		wg.Add(1)
		go func(cid int64) {
			defer wg.Done()
			if err := ls.SaveChatHistory(cid); err != nil {
				log.Printf("[LocalStorage SaveAll ERROR] Ошибка сохранения истории для чата %d: %v", cid, err)
				errMutex.Lock()
				if firstError == nil {
					firstError = err // Запоминаем первую ошибку
				}
				errMutex.Unlock()
			}
		}(id)
	}
	wg.Wait()
	log.Printf("[LocalStorage] Сохранение истории для всех чатов завершено.")
	return firstError // Возвращаем первую возникшую ошибку (если была)
}

// loadAllChatHistories загружает все истории из файлов в директории dataDir.
func (ls *LocalStorage) loadAllChatHistories() error {
	files, err := ioutil.ReadDir(ls.dataDir)
	if err != nil {
		log.Printf("[LocalStorage LoadAll ERROR] Ошибка чтения директории %s: %v", ls.dataDir, err)
		return fmt.Errorf("ошибка чтения директории истории: %w", err)
	}

	loadedCount := 0
	for _, file := range files {
		if !file.IsDir() && filepath.Ext(file.Name()) == ".json" && strings.HasPrefix(file.Name(), "chat_") {
			// Пытаемся извлечь chatID из имени файла
			var chatID int64
			baseName := strings.TrimSuffix(file.Name(), ".json")
			baseName = strings.TrimPrefix(baseName, "chat_")
			if _, err := fmt.Sscan(baseName, &chatID); err == nil && chatID != 0 {
				// log.Printf("[LocalStorage LoadAll] Найден файл: %s, пытаюсь загрузить для chatID: %d", file.Name(), chatID)
				_, loadErr := ls.LoadChatHistory(chatID) // LoadChatHistory обновит кеш
				if loadErr != nil {
					log.Printf("[LocalStorage LoadAll WARN] Ошибка загрузки истории для чата %d из файла %s: %v", chatID, file.Name(), loadErr)
					// Не прерываем цикл, продолжаем загружать остальные
				} else {
					loadedCount++
				}
			} else {
				log.Printf("[LocalStorage LoadAll WARN] Не удалось извлечь chatID из имени файла: %s", file.Name())
			}
		}
	}
	log.Printf("[LocalStorage LoadAll] Завершено сканирование директории '%s'. Загружено историй: %d.", ls.dataDir, loadedCount)
	return nil
}

// --- Конец файла ---

// ImportMessagesFromJSONFile - Заглушка для LocalStorage.
func (ls *LocalStorage) ImportMessagesFromJSONFile(chatID int64, filePath string) (int, int, error) {
	log.Printf("[LocalStorage WARN] ImportMessagesFromJSONFile не поддерживается для LocalStorage. Файл '%s' для чата %d проигнорирован.", filePath, chatID)
	return 0, 0, nil // Возвращаем 0 импортированных, 0 пропущенных
}

// FindRelevantMessages - Заглушка для LocalStorage.
// Всегда возвращает пустой срез и nil ошибку.
// Используем types.Message
func (ls *LocalStorage) FindRelevantMessages(chatID int64, queryText string, limit int) ([]types.Message, error) {
	log.Printf("[LocalStorage WARN] FindRelevantMessages не реализован для LocalStorage (чат %d). Всегда возвращает пустой результат.", chatID)
	return nil, nil // Возвращаем пустой срез и nil ошибку
}

// EOF
