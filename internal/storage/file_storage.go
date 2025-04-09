package storage

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"path/filepath"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// StoredMessage представляет сообщение для хранения в файле
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

// ConvertToStoredMessage преобразует tgbotapi.Message в StoredMessage
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

// ConvertToAPIMessage преобразует StoredMessage обратно в tgbotapi.Message
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
