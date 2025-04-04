// internal/storage/s3_storage.go
package storage

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"strings"
	"sync"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"

	"github.com/Henry-Case-dev/rofloslav/internal/config" // Импортируем конфиг
)

// --- S3Storage ---

// S3Storage реализует HistoryStorage с использованием S3-совместимого хранилища.
type S3Storage struct {
	client        *minio.Client
	bucketName    string
	messages      map[int64][]*tgbotapi.Message // Локальный кеш для быстрого доступа
	contextWindow int
	mutex         sync.RWMutex
}

// NewS3Storage создает новый экземпляр S3Storage.
func NewS3Storage(cfg *config.Config, contextWindow int) (*S3Storage, error) {
	log.Printf("[S3Storage] Инициализация S3 клиента для эндпоинта: %s, бакет: %s, SSL: %t, Region: %s", cfg.S3Endpoint, cfg.S3BucketName, cfg.S3UseSSL, cfg.S3Region)

	// Инициализация клиента MinIO
	minioClient, err := minio.New(cfg.S3Endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(cfg.S3AccessKeyID, cfg.S3SecretAccessKey, ""),
		Secure: cfg.S3UseSSL, // Использовать SSL или нет
		Region: cfg.S3Region, // Опционально, может быть пустым
	})
	if err != nil {
		log.Printf("[S3Storage ERROR] Не удалось инициализировать S3 клиент: %v", err)
		return nil, fmt.Errorf("ошибка инициализации S3 клиента: %w", err)
	}

	// Проверка существования бакета
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second) // Таймаут для проверки
	defer cancel()
	exists, err := minioClient.BucketExists(ctx, cfg.S3BucketName)
	if err != nil {
		log.Printf("[S3Storage ERROR] Ошибка проверки существования бакета '%s': %v", cfg.S3BucketName, err)
		return nil, fmt.Errorf("ошибка проверки бакета '%s': %w", cfg.S3BucketName, err)
	}
	if exists {
		log.Printf("[S3Storage] Проверка бакета '%s': УСПЕШНО (существует).", cfg.S3BucketName)
	} else {
		log.Printf("[S3Storage ERROR] Проверка бакета '%s': НЕ НАЙДЕН!", cfg.S3BucketName)
		return nil, fmt.Errorf("бакет '%s' не найден", cfg.S3BucketName)
	}
	log.Printf("[S3Storage] S3 клиент успешно инициализирован и бакет '%s' найден.", cfg.S3BucketName)

	s3Store := &S3Storage{
		client:        minioClient,
		bucketName:    cfg.S3BucketName,
		messages:      make(map[int64][]*tgbotapi.Message), // Инициализируем кеш
		contextWindow: contextWindow,
		mutex:         sync.RWMutex{},
	}

	// Попытка загрузить все истории при старте (опционально, но полезно)
	log.Printf("[S3Storage] Загрузка всех историй из S3 при старте...")
	err = s3Store.loadAllChatHistoriesFromS3()
	if err != nil {
		// Не фатальная ошибка, просто логируем
		log.Printf("[S3Storage WARN] Не удалось загрузить все истории из S3 при старте: %v", err)
	} else {
		log.Printf("[S3Storage] Загрузка историй из S3 при старте завершена.")
	}

	return s3Store, nil
}

// --- Методы управления кешем в памяти (аналогичны LocalStorage) ---

// AddMessage добавляет сообщение в кеш памяти. Сохранение в S3 происходит отдельно.
func (s3 *S3Storage) AddMessage(chatID int64, message *tgbotapi.Message) {
	s3.mutex.Lock()
	defer s3.mutex.Unlock()

	if _, exists := s3.messages[chatID]; !exists {
		s3.messages[chatID] = make([]*tgbotapi.Message, 0)
	}
	s3.messages[chatID] = append(s3.messages[chatID], message)
	if len(s3.messages[chatID]) > s3.contextWindow {
		s3.messages[chatID] = s3.messages[chatID][len(s3.messages[chatID])-s3.contextWindow:]
	}
}

// AddMessagesToContext добавляет несколько сообщений в кеш памяти.
func (s3 *S3Storage) AddMessagesToContext(chatID int64, messages []*tgbotapi.Message) {
	s3.mutex.Lock()
	defer s3.mutex.Unlock()

	if _, exists := s3.messages[chatID]; !exists {
		s3.messages[chatID] = make([]*tgbotapi.Message, 0)
	}
	s3.messages[chatID] = append(s3.messages[chatID], messages...)
	if len(s3.messages[chatID]) > s3.contextWindow {
		s3.messages[chatID] = s3.messages[chatID][len(s3.messages[chatID])-s3.contextWindow:]
	}
}

// GetMessages возвращает сообщения из кеша памяти.
func (s3 *S3Storage) GetMessages(chatID int64) []*tgbotapi.Message {
	s3.mutex.RLock()
	defer s3.mutex.RUnlock()
	if messages, exists := s3.messages[chatID]; exists {
		msgsCopy := make([]*tgbotapi.Message, len(messages))
		copy(msgsCopy, messages)
		return msgsCopy
	}
	return []*tgbotapi.Message{}
}

// GetMessagesSince возвращает сообщения из кеша памяти с указанного времени.
func (s3 *S3Storage) GetMessagesSince(chatID int64, since time.Time) []*tgbotapi.Message {
	s3.mutex.RLock()
	defer s3.mutex.RUnlock()
	result := make([]*tgbotapi.Message, 0)
	if messages, exists := s3.messages[chatID]; exists {
		for _, msg := range messages {
			if time.Unix(int64(msg.Date), 0).After(since) {
				result = append(result, msg)
			}
		}
	}
	return result
}

// ClearChatHistory очищает историю из кеша памяти.
func (s3 *S3Storage) ClearChatHistory(chatID int64) {
	s3.mutex.Lock()
	defer s3.mutex.Unlock()
	delete(s3.messages, chatID)
	log.Printf("[S3Storage] Чат %d: История в кеше памяти очищена.", chatID)
	// Примечание: Не удаляем объект из S3 при очистке кеша.
	// Удаление из S3 должно быть явным действием, если потребуется.
}

// --- Функции Load/Save для S3 ---

func (s3 *S3Storage) getObjectName(chatID int64) string {
	// Имя объекта в S3 бакете. Можно добавить префикс, если нужно.
	return fmt.Sprintf("chat_%d.json", chatID)
}

// LoadChatHistory загружает историю из S3.
func (s3 *S3Storage) LoadChatHistory(chatID int64) ([]*tgbotapi.Message, error) {
	objectName := s3.getObjectName(chatID)
	log.Printf("[S3Storage] Загружаю историю для чата %d из S3: %s/%s", s3.bucketName, objectName)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second) // Таймаут на загрузку
	defer cancel()

	object, err := s3.client.GetObject(ctx, s3.bucketName, objectName, minio.GetObjectOptions{})
	if err != nil {
		// Проверяем, является ли ошибка "NoSuchKey"
		errResponse := minio.ToErrorResponse(err)
		if errResponse.Code == "NoSuchKey" {
			log.Printf("[S3Storage] Объект '%s' не найден в бакете '%s'.", objectName, s3.bucketName)
			return nil, nil // Не ошибка, просто нет истории
		}
		log.Printf("[S3Storage ERROR] Ошибка получения объекта '%s': %v", objectName, err)
		return nil, fmt.Errorf("ошибка получения истории из S3: %w", err)
	}
	defer object.Close() // Важно закрыть объект

	// Проверяем метаданные перед чтением, чтобы убедиться, что объект существует
	_, err = object.Stat()
	if err != nil {
		errResponse := minio.ToErrorResponse(err)
		if errResponse.Code == "NoSuchKey" {
			log.Printf("[S3Storage] Объект '%s' не найден в бакете '%s' (проверка Stat).", objectName, s3.bucketName)
			return nil, nil // Не ошибка
		}
		log.Printf("[S3Storage ERROR] Ошибка Stat для объекта '%s': %v", objectName, err)
		return nil, fmt.Errorf("ошибка Stat истории из S3: %w", err)
	}

	data, err := ioutil.ReadAll(object)
	if err != nil {
		log.Printf("[S3Storage ERROR] Ошибка чтения данных объекта '%s': %v", objectName, err)
		return nil, fmt.Errorf("ошибка чтения истории из S3: %w", err)
	}

	if len(data) == 0 || string(data) == "null" {
		log.Printf("[S3Storage] Объект '%s' пуст или содержит null.", objectName)
		s3.ClearChatHistory(chatID) // Очищаем кеш, если в S3 пусто
		return []*tgbotapi.Message{}, nil
	}

	var storedMessages []*StoredMessage
	if err := json.Unmarshal(data, &storedMessages); err != nil {
		log.Printf("[S3Storage ERROR] Ошибка десериализации JSON из объекта '%s': %v", objectName, err)
		return nil, fmt.Errorf("ошибка десериализации истории из S3: %w", err)
	}

	var messages []*tgbotapi.Message
	for _, stored := range storedMessages {
		apiMsg := ConvertToAPIMessage(stored)
		if apiMsg != nil {
			messages = append(messages, apiMsg)
		} else {
			log.Printf("[S3Storage WARN] Чат %d: Не удалось конвертировать StoredMessage ID %d из S3", chatID, stored.MessageID)
		}
	}
	log.Printf("[S3Storage OK] Чат %d: Успешно загружено %d сообщений из S3 ('%s').", chatID, len(messages), objectName)

	// Обновляем кеш в памяти загруженными данными
	s3.mutex.Lock()
	s3.messages[chatID] = messages
	s3.mutex.Unlock()

	return messages, nil
}

// SaveChatHistory сохраняет историю чата (из кеша памяти) в S3.
func (s3 *S3Storage) SaveChatHistory(chatID int64) error {
	s3.mutex.RLock()
	messages, exists := s3.messages[chatID]
	s3.mutex.RUnlock()

	if !exists || len(messages) == 0 {
		// log.Printf("[S3Storage] Чат %d: Нет сообщений в кеше для сохранения в S3.", chatID)
		// Возможно, стоит удалить объект из S3, если кеш пуст? Зависит от логики.
		// Пока просто выходим.
		return nil
	}

	objectName := s3.getObjectName(chatID)
	log.Printf("[S3Storage Save] Чат %d: Начинаю сохранение в S3 объект: %s/%s (%d сообщений в кеше)", chatID, s3.bucketName, objectName, len(messages))

	var storedMessages []*StoredMessage
	for _, msg := range messages {
		stored := ConvertToStoredMessage(msg)
		if stored != nil {
			storedMessages = append(storedMessages, stored)
		}
	}

	data, err := json.MarshalIndent(storedMessages, "", "  ")
	if err != nil {
		log.Printf("[S3Storage ERROR] Чат %d: Ошибка маршалинга JSON для S3: %v", chatID, err)
		return fmt.Errorf("ошибка маршалинга истории для S3: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second) // Таймаут на загрузку
	defer cancel()

	// PutObject загружает данные в S3.
	// Размер объекта будет определен автоматически.
	// Используем bytes.NewReader для передачи среза байт как io.Reader.
	contentType := "application/json"
	log.Printf("[S3Storage Save] Чат %d: Вызов PutObject для %s/%s (Размер данных: %d байт, ContentType: %s)", chatID, s3.bucketName, objectName, len(data), contentType)

	info, err := s3.client.PutObject(ctx, s3.bucketName, objectName, bytes.NewReader(data), int64(len(data)), minio.PutObjectOptions{ContentType: contentType})
	if err != nil {
		log.Printf("[S3Storage Save ERROR] Чат %d: Ошибка PutObject для %s/%s: %v", chatID, s3.bucketName, objectName, err)
		return fmt.Errorf("ошибка сохранения истории в S3: %w", err)
	}

	log.Printf("[S3Storage Save OK] Чат %d: Успешно сохранено в S3: %s/%s (Версия: %s, Размер: %d байт)", chatID, s3.bucketName, objectName, info.VersionID, info.Size)
	return nil
}

// SaveAllChatHistories сохраняет все чаты из кеша памяти в S3.
func (s3 *S3Storage) SaveAllChatHistories() error {
	s3.mutex.RLock()
	chatIDs := make([]int64, 0, len(s3.messages))
	for id := range s3.messages {
		// Сохраняем только если есть сообщения в кеше
		if len(s3.messages[id]) > 0 {
			chatIDs = append(chatIDs, id)
		}
	}
	s3.mutex.RUnlock()

	if len(chatIDs) == 0 {
		log.Printf("[S3Storage] Нет чатов в кеше для сохранения в S3.")
		return nil
	}

	log.Printf("[S3Storage] Начинаю сохранение истории для %d чатов в S3...", len(chatIDs))
	var wg sync.WaitGroup
	var firstError error
	var errMutex sync.Mutex

	for _, id := range chatIDs {
		wg.Add(1)
		go func(cid int64) {
			defer wg.Done()
			if err := s3.SaveChatHistory(cid); err != nil {
				log.Printf("[S3Storage SaveAll ERROR] Ошибка сохранения истории S3 для чата %d: %v", cid, err)
				errMutex.Lock()
				if firstError == nil {
					firstError = err
				}
				errMutex.Unlock()
			}
		}(id)
	}
	wg.Wait()
	log.Printf("[S3Storage] Сохранение истории для всех чатов в S3 завершено.")
	return firstError
}

// loadAllChatHistoriesFromS3 загружает историю для всех объектов в бакете при старте.
// ПРИМЕЧАНИЕ: Может быть медленным при большом количестве чатов.
func (s3 *S3Storage) loadAllChatHistoriesFromS3() error {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second) // Увеличенный таймаут
	defer cancel()

	objectCh := s3.client.ListObjects(ctx, s3.bucketName, minio.ListObjectsOptions{
		Prefix:    "chat_", // Ищем только файлы чатов
		Recursive: false,   // Не рекурсивно
	})

	loadedCount := 0
	var loadErrors []error

	for object := range objectCh {
		if object.Err != nil {
			log.Printf("[S3Storage LoadAll ERROR] Ошибка листинга объектов: %v", object.Err)
			loadErrors = append(loadErrors, object.Err)
			continue // Пропускаем этот объект
		}

		// Извлекаем chatID из имени файла (например, "chat_12345.json")
		var chatID int64
		// Убираем префикс и суффикс
		baseName := strings.TrimPrefix(object.Key, "chat_")
		baseName = strings.TrimSuffix(baseName, ".json")
		if _, err := fmt.Sscan(baseName, &chatID); err == nil && chatID != 0 {
			log.Printf("[S3Storage LoadAll] Найден объект: %s, пытаюсь загрузить для chatID: %d", object.Key, chatID)
			// Загружаем историю для найденного chatID
			_, err := s3.LoadChatHistory(chatID) // LoadChatHistory обновит кеш
			if err != nil {
				log.Printf("[S3Storage LoadAll WARN] Ошибка загрузки истории для чата %d из '%s': %v", chatID, object.Key, err)
				// Не добавляем в loadErrors, так как LoadChatHistory уже логирует ошибку
			} else {
				loadedCount++
			}
		} else {
			log.Printf("[S3Storage LoadAll WARN] Не удалось извлечь chatID из имени объекта: %s", object.Key)
		}
	}

	log.Printf("[S3Storage LoadAll] Завершено сканирование бакета '%s'. Загружено историй: %d.", s3.bucketName, loadedCount)
	if len(loadErrors) > 0 {
		return fmt.Errorf("были ошибки при листинге объектов S3: %v", loadErrors)
	}
	return nil
}
