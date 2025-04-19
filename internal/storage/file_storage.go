package storage

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// StoredMessage представляет сообщение для хранения в файле
type StoredMessage struct {
	ChatID           int64  `json:"chat_id"`
	MessageID        int    `json:"message_id"`
	UserID           int64  `json:"user_id,omitempty"`
	Username         string `json:"username,omitempty"`
	FirstName        string `json:"first_name,omitempty"`
	LastName         string `json:"last_name,omitempty"`
	IsBot            bool   `json:"is_bot,omitempty"`
	Text             string `json:"text,omitempty"`
	Date             int    `json:"date"`
	ReplyToMessageID int    `json:"reply_to_message_id,omitempty"`
	RawMessageJSON   string `json:"raw_message_json,omitempty"` // Для хранения всего сообщения

	// Поля для пересылки
	IsForward              bool  `json:"is_forward,omitempty"`
	ForwardedFromUserID    int64 `json:"forwarded_from_user_id,omitempty"`
	ForwardedFromChatID    int64 `json:"forwarded_from_chat_id,omitempty"`
	ForwardedFromMessageID int   `json:"forwarded_from_message_id,omitempty"`
	ForwardedDate          int   `json:"forwarded_date,omitempty"`
}

// ConvertToStoredMessage преобразует tgbotapi.Message в StoredMessage
func ConvertToStoredMessage(msg *tgbotapi.Message) *StoredMessage {
	if msg == nil {
		return nil
	}

	storedMsg := &StoredMessage{
		ChatID:    msg.Chat.ID,
		MessageID: msg.MessageID,
		Text:      msg.Text, // Сохраняем и Text, и Caption
		Date:      msg.Date,
	}
	if msg.From != nil {
		storedMsg.UserID = msg.From.ID
		storedMsg.Username = msg.From.UserName
		storedMsg.FirstName = msg.From.FirstName
		storedMsg.LastName = msg.From.LastName
		storedMsg.IsBot = msg.From.IsBot
	}
	if msg.ReplyToMessage != nil {
		storedMsg.ReplyToMessageID = msg.ReplyToMessage.MessageID
	}

	// Сохраняем информацию о пересылке
	if msg.ForwardDate > 0 {
		storedMsg.IsForward = true
		storedMsg.ForwardedDate = msg.ForwardDate
		if msg.ForwardFrom != nil {
			storedMsg.ForwardedFromUserID = msg.ForwardFrom.ID
		}
		if msg.ForwardFromChat != nil {
			storedMsg.ForwardedFromChatID = msg.ForwardFromChat.ID
		}
		storedMsg.ForwardedFromMessageID = msg.ForwardFromMessageID
	}

	// Сериализуем всё сообщение в JSON для RawMessageJSON
	rawJSONBytes, err := json.Marshal(msg)
	if err == nil {
		storedMsg.RawMessageJSON = string(rawJSONBytes)
	} else {
		log.Printf("Error marshaling raw message for chat %d, msg %d: %v", msg.Chat.ID, msg.MessageID, err)
		// Можно решить, что делать в случае ошибки - пропустить или записать пустую строку
		storedMsg.RawMessageJSON = ""
	}

	return storedMsg
}

// ConvertToAPIMessage преобразует StoredMessage обратно в *tgbotapi.Message.
// Основная логика теперь полагается на десериализацию из RawMessageJSON,
// но мы сохраняем базовую конвертацию для обратной совместимости или случаев,
// когда RawMessageJSON отсутствует.
func ConvertToAPIMessage(stored *StoredMessage) *tgbotapi.Message {
	if stored == nil {
		return nil
	}

	// Пытаемся десериализовать из RawMessageJSON в первую очередь
	if stored.RawMessageJSON != "" {
		var msg tgbotapi.Message
		err := json.Unmarshal([]byte(stored.RawMessageJSON), &msg)
		if err == nil {
			return &msg // Успешно десериализовано
		}
		log.Printf("Error unmarshaling raw message for chat %d, msg %d: %v. Falling back to manual conversion.", stored.ChatID, stored.MessageID, err)
	}

	// Fallback: ручное восстановление из полей StoredMessage
	msg := &tgbotapi.Message{
		MessageID: stored.MessageID,
		From: &tgbotapi.User{
			ID:        stored.UserID,
			IsBot:     stored.IsBot,
			FirstName: stored.FirstName,
			LastName:  stored.LastName,
			UserName:  stored.Username,
		},
		Chat: &tgbotapi.Chat{
			ID: stored.ChatID,
			// Другие поля Chat могут быть недоступны в StoredMessage
		},
		Date: stored.Date,
		Text: stored.Text,
		// Entities и другие поля будут пустыми при ручном восстановлении
	}

	if stored.ReplyToMessageID != 0 {
		// Создаем "пустое" сообщение для ReplyTo, так как полных данных нет
		msg.ReplyToMessage = &tgbotapi.Message{
			MessageID: stored.ReplyToMessageID,
			Chat:      msg.Chat, // Предполагаем, что ответ в том же чате
		}
	}

	// Восстанавливаем информацию о пересылке
	if stored.IsForward {
		msg.ForwardDate = stored.ForwardedDate
		msg.ForwardFromMessageID = stored.ForwardedFromMessageID
		if stored.ForwardedFromUserID != 0 {
			msg.ForwardFrom = &tgbotapi.User{ID: stored.ForwardedFromUserID}
			// Остальные поля ForwardFrom User неизвестны
		}
		if stored.ForwardedFromChatID != 0 {
			msg.ForwardFromChat = &tgbotapi.Chat{ID: stored.ForwardedFromChatID}
			// Остальные поля ForwardFromChat неизвестны
		}
	}

	return msg
}

// FileStorage реализует интерфейс ChatHistoryStorage для хранения данных в файлах JSON.
type FileStorage struct {
	messages      map[int64][]*tgbotapi.Message
	userProfiles  map[int64]map[int64]*UserProfile // map[chatID]map[userID]UserProfile
	contextWindow int
	mutex         sync.RWMutex
	autoSave      bool
}

// Убедимся, что FileStorage реализует интерфейс ChatHistoryStorage.
var _ ChatHistoryStorage = (*FileStorage)(nil)

// NewFileStorage создает новое файловое хранилище.
func NewFileStorage(contextWindow int, autoSave bool) *FileStorage {
	fs := &FileStorage{
		messages:      make(map[int64][]*tgbotapi.Message),
		userProfiles:  make(map[int64]map[int64]*UserProfile),
		contextWindow: contextWindow,
		autoSave:      autoSave,
	}
	// Загрузка существующих историй при старте
	// TODO: Пересмотреть логику загрузки при старте
	return fs
}

// AddMessage добавляет сообщение в историю чата в памяти.
func (fs *FileStorage) AddMessage(chatID int64, message *tgbotapi.Message) {
	fs.mutex.Lock()
	defer fs.mutex.Unlock()

	fs.messages[chatID] = append(fs.messages[chatID], message)
	// Ограничиваем размер истории
	if len(fs.messages[chatID]) > fs.contextWindow {
		fs.messages[chatID] = fs.messages[chatID][len(fs.messages[chatID])-fs.contextWindow:]
	}
}

// GetMessages возвращает последние N сообщений для указанного чата из памяти.
func (fs *FileStorage) GetMessages(chatID int64, limit int) ([]*tgbotapi.Message, error) {
	fs.mutex.RLock()
	defer fs.mutex.RUnlock()

	messages, exists := fs.messages[chatID]
	if !exists {
		return nil, nil // Возвращаем nil, nil, если истории для чата нет
	}

	// Копируем срез, чтобы избежать гонки данных при возврате
	// и обрезаем до лимита, если нужно
	numMessages := len(messages)
	start := 0
	if numMessages > limit {
		start = numMessages - limit
	}
	msgsCopy := make([]*tgbotapi.Message, numMessages-start)
	copy(msgsCopy, messages[start:])

	// Сообщения в file storage хранятся в хронологическом порядке (старые -> новые)
	// Возвращаем последние 'limit' сообщений
	return msgsCopy, nil
}

// GetMessagesSince возвращает сообщения из указанного чата, начиная с определенного времени.
func (fs *FileStorage) GetMessagesSince(chatID int64, since time.Time) ([]*tgbotapi.Message, error) {
	fs.mutex.RLock()
	defer fs.mutex.RUnlock()

	messages, exists := fs.messages[chatID]
	if !exists {
		return nil, nil // Нет истории для чата
	}

	// Ищем индекс первого сообщения, которое >= since
	startIndex := -1
	for i, msg := range messages {
		if time.Unix(int64(msg.Date), 0).After(since) || time.Unix(int64(msg.Date), 0).Equal(since) {
			startIndex = i
			break
		}
	}

	if startIndex == -1 {
		return nil, nil // Нет сообщений после указанной даты
	}

	// Копируем срез, чтобы избежать гонки данных при возврате
	msgsCopy := make([]*tgbotapi.Message, len(messages)-startIndex)
	copy(msgsCopy, messages[startIndex:])

	return msgsCopy, nil // Возвращаем (slice, nil)
}

// AddMessagesToContext добавляет предоставленные сообщения в контекст чата.
func (fs *FileStorage) AddMessagesToContext(chatID int64, messages []*tgbotapi.Message) {
	fs.mutex.Lock()
	defer fs.mutex.Unlock()
	fs.messages[chatID] = messages

	// Ограничиваем размер истории после добавления
	if len(fs.messages[chatID]) > fs.contextWindow {
		fs.messages[chatID] = fs.messages[chatID][len(fs.messages[chatID])-fs.contextWindow:]
	}
}

// ClearChatHistory очищает историю сообщений для чата в памяти и удаляет файл.
func (fs *FileStorage) ClearChatHistory(chatID int64) error {
	fs.mutex.Lock()
	defer fs.mutex.Unlock()

	delete(fs.messages, chatID)

	// Удаляем файл истории, если он существует
	if err := fs.deleteChatHistoryFile(chatID); err != nil {
		// Логируем ошибку, но не считаем её критичной для очистки памяти
		log.Printf("[FileStorage ClearHistory WARN] Ошибка удаления файла истории для chatID %d: %v", chatID, err)
	}
	return nil
}

// Close в FileStorage ничего не делает, но должен быть для интерфейса.
func (fs *FileStorage) Close() error {
	if fs.autoSave {
		log.Println("FileStorage: Сохранение всех историй перед закрытием...")
		for chatID := range fs.messages {
			if err := fs.SaveChatHistory(chatID); err != nil {
				log.Printf("[FileStorage Close ERROR] Ошибка сохранения истории для chatID %d: %v", chatID, err)
			}
		}
		log.Println("FileStorage: Сохранение завершено.")
	}
	return nil
}

// SaveChatHistory сохраняет историю сообщений чата в JSON файл.
func (fs *FileStorage) SaveChatHistory(chatID int64) error {
	fs.mutex.RLock() // Блокируем на чтение
	messages, exists := fs.messages[chatID]
	fs.mutex.RUnlock()

	if !exists {
		// Если истории нет, ничего не сохраняем (или удаляем файл?) Пока ничего.
		return nil
	}

	// Конвертируем сообщения в формат для хранения
	storedMessages := make([]*StoredMessage, 0, len(messages))
	for _, msg := range messages {
		storedMessages = append(storedMessages, ConvertToStoredMessage(msg))
	}

	data, err := json.MarshalIndent(storedMessages, "", "  ")
	if err != nil {
		return fmt.Errorf("ошибка маршалинга истории чата %d: %w", chatID, err)
	}

	filename := fs.getChatHistoryFilename(chatID)
	if err := os.WriteFile(filename, data, 0644); err != nil {
		return fmt.Errorf("ошибка записи истории чата %d в файл %s: %w", chatID, filename, err)
	}

	return nil
}

// LoadChatHistory загружает историю сообщений из JSON файла.
func (fs *FileStorage) LoadChatHistory(chatID int64) ([]*tgbotapi.Message, error) {
	filename := fs.getChatHistoryFilename(chatID)
	data, err := os.ReadFile(filename)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil // Файл не найден - это не ошибка, просто нет истории
		}
		return nil, fmt.Errorf("ошибка чтения истории чата %d из файла %s: %w", chatID, filename, err)
	}

	if len(data) == 0 {
		return []*tgbotapi.Message{}, nil // Пустой файл - пустая история
	}

	var storedMessages []*StoredMessage
	if err := json.Unmarshal(data, &storedMessages); err != nil {
		return nil, fmt.Errorf("ошибка демаршалинга истории чата %d из файла %s: %w", chatID, filename, err)
	}

	// Конвертируем обратно в формат API
	messages := make([]*tgbotapi.Message, 0, len(storedMessages))
	for _, stored := range storedMessages {
		messages = append(messages, ConvertToAPIMessage(stored))
	}

	return messages, nil
}

// getChatHistoryFilename возвращает имя файла для истории чата.
func (fs *FileStorage) getChatHistoryFilename(chatID int64) string {
	// Создаем папку data, если ее нет
	if _, err := os.Stat("data"); os.IsNotExist(err) {
		_ = os.Mkdir("data", 0755)
	}
	return filepath.Join("data", fmt.Sprintf("%d.json", chatID))
}

// deleteChatHistoryFile удаляет файл истории чата.
func (fs *FileStorage) deleteChatHistoryFile(chatID int64) error {
	filename := fs.getChatHistoryFilename(chatID)
	err := os.Remove(filename)
	if err != nil && !os.IsNotExist(err) {
		return err // Возвращаем ошибку, если она не "файл не найден"
	}
	return nil
}

// --- User Profile Methods (FileStorage - In-Memory) ---

// GetUserProfile возвращает профиль пользователя из памяти.
func (fs *FileStorage) GetUserProfile(chatID int64, userID int64) (*UserProfile, error) {
	fs.mutex.RLock()
	defer fs.mutex.RUnlock()

	if profiles, ok := fs.userProfiles[chatID]; ok {
		if profile, ok := profiles[userID]; ok {
			// Возвращаем копию, чтобы избежать изменения вне мьютекса
			profileCopy := *profile
			return &profileCopy, nil
		}
	}
	return nil, nil // Профиль не найден
}

// SetUserProfile сохраняет профиль пользователя в память.
func (fs *FileStorage) SetUserProfile(profile *UserProfile) error {
	fs.mutex.Lock()
	defer fs.mutex.Unlock()

	if _, ok := fs.userProfiles[profile.ChatID]; !ok {
		fs.userProfiles[profile.ChatID] = make(map[int64]*UserProfile)
	}
	// Сохраняем копию
	profileCopy := *profile
	fs.userProfiles[profile.ChatID][profile.UserID] = &profileCopy
	// TODO: Добавить сохранение профилей в файл?
	return nil
}

// GetAllUserProfiles возвращает все профили пользователей для чата из памяти.
func (fs *FileStorage) GetAllUserProfiles(chatID int64) ([]*UserProfile, error) {
	fs.mutex.RLock()
	defer fs.mutex.RUnlock()

	if profilesMap, ok := fs.userProfiles[chatID]; ok {
		profiles := make([]*UserProfile, 0, len(profilesMap))
		for _, profile := range profilesMap {
			// Возвращаем копии
			profileCopy := *profile
			profiles = append(profiles, &profileCopy)
		}
		return profiles, nil
	}
	return []*UserProfile{}, nil // Возвращаем пустой срез, если для чата нет профилей
}

// GetAllChatIDs возвращает все ID чатов, для которых есть история в памяти.
func (fs *FileStorage) GetAllChatIDs() ([]int64, error) {
	fs.mutex.RLock()
	defer fs.mutex.RUnlock()

	chatIDs := make([]int64, 0, len(fs.messages))
	for chatID := range fs.messages {
		chatIDs = append(chatIDs, chatID)
	}
	return chatIDs, nil
}

// GetStatus возвращает строку со статусом FileStorage.
func (fs *FileStorage) GetStatus(chatID int64) string {
	fs.mutex.RLock()
	defer fs.mutex.RUnlock()

	msgCount := 0
	if msgs, ok := fs.messages[chatID]; ok {
		msgCount = len(msgs)
	}
	profileCount := 0
	if profiles, ok := fs.userProfiles[chatID]; ok {
		profileCount = len(profiles)
	}

	return fmt.Sprintf("FileStorage | Сообщ: %d/%d | Профили: %d | Автосохр: %t",
		msgCount, fs.contextWindow, profileCount, fs.autoSave)
}

// --- Chat Settings Methods (FileStorage - Not Implemented) ---

func (fs *FileStorage) GetChatSettings(chatID int64) (*ChatSettings, error) {
	// FileStorage не хранит настройки чатов персистентно.
	// Возвращаем nil и nil, чтобы вызывающий код использовал дефолтные настройки.
	return nil, nil
}

func (fs *FileStorage) SetChatSettings(settings *ChatSettings) error {
	log.Printf("[FileStorage WARN] SetChatSettings не поддерживается FileStorage для chatID %d", settings.ChatID)
	return fmt.Errorf("SetChatSettings не поддерживается FileStorage")
}

func (fs *FileStorage) UpdateDirectLimitEnabled(chatID int64, enabled bool) error {
	log.Printf("[FileStorage WARN] UpdateDirectLimitEnabled не поддерживается FileStorage для chatID %d", chatID)
	return fmt.Errorf("UpdateDirectLimitEnabled не поддерживается FileStorage")
}

func (fs *FileStorage) UpdateDirectLimitCount(chatID int64, count int) error {
	log.Printf("[FileStorage WARN] UpdateDirectLimitCount не поддерживается FileStorage для chatID %d", chatID)
	return fmt.Errorf("UpdateDirectLimitCount не поддерживается FileStorage")
}

func (fs *FileStorage) UpdateDirectLimitDuration(chatID int64, duration time.Duration) error {
	log.Printf("[FileStorage WARN] UpdateDirectLimitDuration не поддерживается FileStorage для chatID %d", chatID)
	return fmt.Errorf("UpdateDirectLimitDuration не поддерживается FileStorage")
}

func (fs *FileStorage) UpdateVoiceTranscriptionEnabled(chatID int64, enabled bool) error {
	log.Printf("[FileStorage WARN] UpdateVoiceTranscriptionEnabled не поддерживается FileStorage для chatID %d", chatID)
	return fmt.Errorf("UpdateVoiceTranscriptionEnabled не поддерживается FileStorage")
}

func (fs *FileStorage) UpdateSrachAnalysisEnabled(chatID int64, enabled bool) error {
	log.Printf("[FileStorage WARN] UpdateSrachAnalysisEnabled не поддерживается FileStorage для chatID %d", chatID)
	return fmt.Errorf("UpdateSrachAnalysisEnabled не поддерживается FileStorage")
}

// --- Embedding and Vector Search Methods (FileStorage - Stubs) ---

func (fs *FileStorage) SearchRelevantMessages(chatID int64, queryText string, k int) ([]*tgbotapi.Message, error) {
	log.Printf("[FileStorage WARN] SearchRelevantMessages не поддерживается FileStorage для chatID %d", chatID)
	return nil, fmt.Errorf("векторный поиск не поддерживается FileStorage")
}

func (fs *FileStorage) GetTotalMessagesCount(chatID int64) (int64, error) {
	log.Printf("[FileStorage WARN] GetTotalMessagesCount не поддерживается FileStorage для chatID %d", chatID)
	fs.mutex.RLock()
	defer fs.mutex.RUnlock()
	if msgs, ok := fs.messages[chatID]; ok {
		return int64(len(msgs)), nil
	}
	return 0, nil // Возвращаем 0, если истории нет
}

func (fs *FileStorage) FindMessagesWithoutEmbedding(chatID int64, limit int, skipMessageIDs []int) ([]MongoMessage, error) {
	log.Printf("[FileStorage WARN] FindMessagesWithoutEmbedding не поддерживается FileStorage для chatID %d", chatID)
	return nil, fmt.Errorf("операции с эмбеддингами не поддерживаются FileStorage")
}

func (fs *FileStorage) UpdateMessageEmbedding(chatID int64, messageID int, vector []float32) error {
	log.Printf("[FileStorage WARN] UpdateMessageEmbedding не поддерживается FileStorage для chatID %d, messageID %d", chatID, messageID)
	return fmt.Errorf("операции с эмбеддингами не поддерживаются FileStorage")
}
