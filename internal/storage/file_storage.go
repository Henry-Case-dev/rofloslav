package storage

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"path/filepath"
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

// SaveChatHistory сохраняет историю сообщений в файл
func (fs *FileStorage) SaveChatHistory(chatID int64) error {
	fs.mutex.RLock()
	messages, exists := fs.messages[chatID]
	fs.mutex.RUnlock()

	if !exists || len(messages) == 0 {
		// Убрал лог, т.к. это нормальная ситуация
		// log.Printf("[SaveChatHistory] Чат %d: Нет сообщений для сохранения или чат не найден.", chatID)
		return nil
	}

	// Уменьшил уровень логирования, чтобы не засорять логи
	// log.Printf("[SaveChatHistory] Чат %d: Начинаю сохранение %d сообщений.", chatID, len(messages))

	// --- Используем директорию /data для Amvera ---
	historyDir := "/data"
	if err := os.MkdirAll(historyDir, 0755); err != nil {
		log.Printf("[SaveChatHistory ERROR] Чат %d: Ошибка создания/доступа к директории /data: %v", chatID, err)
		return fmt.Errorf("error creating/accessing history directory '/data': %w", err)
	}
	// Убрал лог об успешной проверке директории
	// log.Printf("[SaveChatHistory] Чат %d: Директория '%s' проверена/создана.", chatID, historyDir)

	fileName := filepath.Join(historyDir, fmt.Sprintf("chat_%d.json", chatID))
	// Убрал лог имени файла
	// log.Printf("[SaveChatHistory] Чат %d: Файл для сохранения: '%s'", chatID, fileName)

	// Преобразуем сообщения в формат для хранения
	var storedMessages []*StoredMessage
	for _, msg := range messages {
		storedMsg := ConvertToStoredMessage(msg)
		if storedMsg != nil {
			storedMessages = append(storedMessages, storedMsg)
		}
	}
	// Убрал лог о конвертации
	// log.Printf("[SaveChatHistory] Чат %d: Сообщения сконвертированы в StoredMessage (%d).", chatID, len(storedMessages))

	// Сериализуем в JSON
	data, err := json.MarshalIndent(storedMessages, "", "  ")
	if err != nil {
		log.Printf("[SaveChatHistory ERROR] Чат %d: Ошибка маршалинга JSON: %v", chatID, err)
		return fmt.Errorf("error marshaling chat history: %w", err)
	}
	// Убрал лог о сериализации
	// log.Printf("[SaveChatHistory] Чат %d: История успешно сериализована в JSON (%d байт).", chatID, len(data))

	// Записываем в файл
	if err := ioutil.WriteFile(fileName, data, 0644); err != nil {
		log.Printf("[SaveChatHistory ERROR] Чат %d: Ошибка записи в файл '%s': %v", chatID, fileName, err)
		return fmt.Errorf("error writing chat history to '%s': %w", fileName, err)
	}

	log.Printf("[SaveChatHistory OK] Чат %d: История (%d сообщ.) записана в '%s'.", chatID, len(storedMessages), fileName)
	return nil
}

// LoadChatHistory загружает историю сообщений из файла
func (fs *FileStorage) LoadChatHistory(chatID int64) ([]*tgbotapi.Message, error) {
	// --- Используем директорию /data для Amvera ---
	historyDir := "/data"
	fileName := filepath.Join(historyDir, fmt.Sprintf("chat_%d.json", chatID))
	log.Printf("[LoadChatHistory] Чат %d: Загружаю историю из '%s'", chatID, fileName)

	// Проверяем существование файла
	if _, err := os.Stat(fileName); os.IsNotExist(err) {
		log.Printf("[LoadChatHistory] Чат %d: Файл истории '%s' не найден.", chatID, fileName)
		return nil, nil // Файла нет - это не ошибка, просто нет истории
	} else if err != nil {
		// Другая ошибка при проверке файла
		log.Printf("[LoadChatHistory ERROR] Чат %d: Ошибка проверки файла '%s': %v", chatID, fileName, err)
		return nil, fmt.Errorf("error checking history file stat: %w", err)
	}

	// Читаем данные из файла
	// log.Printf("[LoadChatHistory] Чат %d: Файл '%s' найден, читаю содержимое.", chatID, fileName)
	data, err := ioutil.ReadFile(fileName)
	if err != nil {
		log.Printf("[LoadChatHistory ERROR] Чат %d: Ошибка чтения файла '%s': %v", chatID, fileName, err)
		return nil, fmt.Errorf("error reading chat history: %w", err)
	}
	// log.Printf("[LoadChatHistory] Чат %d: Файл '%s' прочитан (%d байт).", chatID, fileName, len(data))

	// Десериализуем из JSON
	var storedMessages []*StoredMessage
	if err := json.Unmarshal(data, &storedMessages); err != nil {
		// Попытка обработать пустой или поврежденный JSON
		if len(data) == 0 || string(data) == "null" || string(data) == "[]" {
			log.Printf("[LoadChatHistory] Чат %d: Файл '%s' пуст или содержит пустой JSON, возвращаю пустую историю.", chatID, fileName)
			return []*tgbotapi.Message{}, nil // Возвращаем пустой слайс, а не ошибку
		}
		log.Printf("[LoadChatHistory ERROR] Чат %d: Ошибка десериализации JSON из '%s': %v", chatID, fileName, err)
		return nil, fmt.Errorf("error unmarshaling chat history: %w", err)
	}
	// log.Printf("[LoadChatHistory] Чат %d: JSON успешно десериализован в %d StoredMessage.", chatID, len(storedMessages))

	// Преобразуем обратно в tgbotapi.Message
	var messages []*tgbotapi.Message
	for _, stored := range storedMessages {
		apiMsg := ConvertToAPIMessage(stored)
		if apiMsg != nil { // Добавляем проверку, что конвертация успешна
			messages = append(messages, apiMsg)
		} else {
			log.Printf("[LoadChatHistory WARN] Чат %d: Не удалось конвертировать StoredMessage ID %d в tgbotapi.Message", chatID, stored.MessageID)
		}
	}
	log.Printf("[LoadChatHistory OK] Чат %d: Успешно загружено %d сообщений из '%s'.", chatID, len(messages), fileName)

	return messages, nil
}

// GetChatSettings возвращает пустые настройки, так как FileStorage их не хранит.
func (fs *FileStorage) GetChatSettings(chatID int64) (*ChatSettings, error) {
	log.Printf("[WARN][FileStorage] GetChatSettings вызван для chatID %d, но FileStorage не хранит настройки. Возвращены пустые.", chatID)
	return &ChatSettings{ChatID: chatID}, nil // Возвращаем пустую структуру
}

// SetChatSettings ничего не делает, так как FileStorage не хранит настройки.
func (fs *FileStorage) SetChatSettings(settings *ChatSettings) error {
	log.Printf("[WARN][FileStorage] SetChatSettings вызван для chatID %d, но FileStorage не хранит настройки. Операция проигнорирована.", settings.ChatID)
	return nil // Не ошибка, просто ничего не делаем
}

// --- Новые методы для обновления отдельных настроек лимитов (заглушки) ---

// UpdateDirectLimitEnabled ничего не делает, так как FileStorage не хранит настройки чата.
func (fs *FileStorage) UpdateDirectLimitEnabled(chatID int64, enabled bool) error {
	log.Printf("[WARN][FileStorage] UpdateDirectLimitEnabled вызван для chatID %d, но FileStorage не хранит настройки. Операция проигнорирована.", chatID)
	return nil // FileStorage не хранит ChatSettings
}

// UpdateDirectLimitCount ничего не делает, так как FileStorage не хранит настройки чата.
func (fs *FileStorage) UpdateDirectLimitCount(chatID int64, count int) error {
	log.Printf("[WARN][FileStorage] UpdateDirectLimitCount вызван для chatID %d, но FileStorage не хранит настройки. Операция проигнорирована.", chatID)
	return nil // FileStorage не хранит ChatSettings
}

// UpdateDirectLimitDuration ничего не делает, так как FileStorage не хранит настройки чата.
func (fs *FileStorage) UpdateDirectLimitDuration(chatID int64, duration time.Duration) error {
	log.Printf("[WARN][FileStorage] UpdateDirectLimitDuration вызван для chatID %d, но FileStorage не хранит настройки. Операция проигнорирована.", chatID)
	return nil // FileStorage не хранит ChatSettings
}

// SearchRelevantMessages (Заглушка для FileStorage)
// FileStorage не поддерживает векторный поиск.
func (fs *FileStorage) SearchRelevantMessages(chatID int64, queryText string, k int) ([]*tgbotapi.Message, error) {
	log.Printf("[WARN][FileStorage] SearchRelevantMessages вызван для chatID %d, но FileStorage не поддерживает векторный поиск. Возвращен пустой результат.", chatID)
	return []*tgbotapi.Message{}, nil
}

// === Методы, специфичные для MongoDB (заглушки для FileStorage) ===

// GetTotalMessagesCount (заглушка)
func (fs *FileStorage) GetTotalMessagesCount(chatID int64) (int64, error) {
	log.Printf("[WARN][FileStorage] GetTotalMessagesCount вызван для chatID %d, но FileStorage не поддерживает эту операцию.", chatID)
	return 0, fmt.Errorf("GetTotalMessagesCount не поддерживается FileStorage")
}

// FindMessagesWithoutEmbedding (заглушка)
func (fs *FileStorage) FindMessagesWithoutEmbedding(chatID int64, limit int, skipMessageIDs []int) ([]MongoMessage, error) {
	log.Printf("[WARN][FileStorage] FindMessagesWithoutEmbedding вызван для chatID %d (лимит %d, пропуск %d ID), но FileStorage не поддерживает эту операцию.", chatID, limit, len(skipMessageIDs))
	return nil, fmt.Errorf("FindMessagesWithoutEmbedding не поддерживается FileStorage")
}

// UpdateMessageEmbedding (заглушка)
func (fs *FileStorage) UpdateMessageEmbedding(chatID int64, messageID int, vector []float32) error {
	log.Printf("[WARN][FileStorage] UpdateMessageEmbedding вызван для chatID %d, MsgID %d, но FileStorage не поддерживает эту операцию.", chatID, messageID)
	return fmt.Errorf("UpdateMessageEmbedding не поддерживается FileStorage")
}

// UpdateVoiceTranscriptionEnabled обновляет настройку транскрипции голоса (Заглушка для FileStorage)
func (fs *FileStorage) UpdateVoiceTranscriptionEnabled(chatID int64, enabled bool) error {
	log.Printf("[WARN][FileStorage] UpdateVoiceTranscriptionEnabled для чата %d: операция не поддерживается FileStorage.", chatID)
	return fmt.Errorf("UpdateVoiceTranscriptionEnabled не поддерживается FileStorage")
}

// UpdateSrachAnalysisEnabled обновляет настройку анализа срачей (Заглушка для FileStorage)
func (fs *FileStorage) UpdateSrachAnalysisEnabled(chatID int64, enabled bool) error {
	log.Printf("[WARN][FileStorage] UpdateSrachAnalysisEnabled для чата %d: операция не поддерживается FileStorage.", chatID)
	return fmt.Errorf("UpdateSrachAnalysisEnabled не поддерживается FileStorage")
}
