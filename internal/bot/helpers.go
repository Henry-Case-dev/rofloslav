package bot

import (
	"encoding/json"
	"fmt"
	"log"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// sendReply отправляет текстовое сообщение в указанный чат.
// Использует Markdown для форматирования.
func (b *Bot) sendReply(chatID int64, text string) {
	if text == "" {
		log.Printf("[WARN] Попытка отправить пустое сообщение в чат %d", chatID)
		return
	}
	msg := tgbotapi.NewMessage(chatID, text)
	msg.ParseMode = "Markdown" // Используем Markdown

	_, err := b.api.Send(msg)
	if err != nil {
		// Улучшаем логирование ошибок API Telegram
		log.Printf("[ERROR] Ошибка отправки сообщения в чат %d: %v. Текст: %s...", chatID, err, truncateString(text, 50))
		// Дополнительная информация об ошибке, если доступна
		if tgErr, ok := err.(tgbotapi.Error); ok {
			log.Printf("[ERROR] Telegram API Error: Code %d, Description: %s", tgErr.Code, tgErr.Message)
		}
	}
}

// sendReplyWithKeyboard отправляет текстовое сообщение с inline-клавиатурой.
// Использует Markdown для форматирования.
func (b *Bot) sendReplyWithKeyboard(chatID int64, text string, keyboard tgbotapi.InlineKeyboardMarkup) {
	if text == "" {
		log.Printf("[WARN] Попытка отправить пустое сообщение с клавиатурой в чат %d", chatID)
		return
	}
	msg := tgbotapi.NewMessage(chatID, text)
	msg.ParseMode = "Markdown" // Используем Markdown
	msg.ReplyMarkup = keyboard

	_, err := b.api.Send(msg)
	if err != nil {
		log.Printf("[ERROR] Ошибка отправки сообщения с клавиатурой в чат %d: %v. Текст: %s...", chatID, err, truncateString(text, 50))
		if tgErr, ok := err.(tgbotapi.Error); ok {
			log.Printf("[ERROR] Telegram API Error: Code %d, Description: %s", tgErr.Code, tgErr.Message)
		}
	}
}

// answerCallback отправляет ответ на CallbackQuery (например, уведомление при нажатии кнопки).
func (b *Bot) answerCallback(callbackID string, text string) {
	callback := tgbotapi.NewCallback(callbackID, text)
	// Не показываем alert по умолчанию (ShowAlert: false)
	_, err := b.api.Request(callback)
	if err != nil {
		// Эта ошибка менее критична, чем отправка сообщения, можно логировать с меньшим уровнем.
		log.Printf("[WARN] Ошибка ответа на callback %s: %v", callbackID, err)
	}
}

// truncateString обрезает строку до указанной максимальной длины (в рунах),
// добавляя "..." в конце, если строка была обрезана.
// Безопасно для Unicode.
func truncateString(s string, maxLen int) string {
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	// Обеспечиваем минимальную длину для добавления "..."
	if maxLen < 3 {
		if maxLen <= 0 {
			return ""
		}
		return string(runes[:maxLen])
	}
	// Обрезаем и добавляем троеточие
	return string(runes[:maxLen-3]) + "..."
}

// formatDuration форматирует time.Duration в более читаемый вид (например, "5m10s").
func formatDuration(d time.Duration) string {
	return d.Round(time.Second).String()
}

// min возвращает меньшее из двух целых чисел.
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// max возвращает большее из двух целых чисел.
func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// formatMessageForAnalysis форматирует сообщение для передачи в LLM при анализе срача
// или для логов. Включает имя пользователя и информацию об ответе.
func formatMessageForAnalysis(msg *tgbotapi.Message) string {
	if msg == nil {
		return "[пустое сообщение]"
	}
	userName := "UnknownUser"
	if msg.From != nil {
		userName = msg.From.UserName
		if userName == "" {
			userName = strings.TrimSpace(msg.From.FirstName + " " + msg.From.LastName)
		}
	}
	// Добавляем информацию об ответе, если есть
	replyInfo := ""
	if msg.ReplyToMessage != nil && msg.ReplyToMessage.From != nil {
		replyUser := msg.ReplyToMessage.From.UserName
		if replyUser == "" {
			replyUser = strings.TrimSpace(msg.ReplyToMessage.From.FirstName + " " + msg.ReplyToMessage.From.LastName)
		}
		replyInfo = fmt.Sprintf(" (ответ %s)", replyUser)
	}

	// Обрабатываем текст
	text := msg.Text
	if text == "" {
		if msg.Caption != "" {
			text = fmt.Sprintf("[Подпись к медиа: %s]", truncateString(msg.Caption, 30))
		} else if msg.Sticker != nil {
			text = fmt.Sprintf("[Стикер: %s]", msg.Sticker.Emoji)
		} else if len(msg.Photo) > 0 {
			text = "[Фото]"
		} else if msg.Video != nil {
			text = "[Видео]"
		} else if msg.Voice != nil {
			text = "[Голосовое сообщение]"
		} else if msg.Document != nil {
			text = fmt.Sprintf("[Документ: %s]", msg.Document.FileName)
		} else {
			text = "[Нетекстовое сообщение]"
		}
	}

	return fmt.Sprintf("[%s]%s: %s", userName, replyInfo, text)
}

// deleteMessage удаляет сообщение из чата
func (b *Bot) deleteMessage(chatID int64, messageID int) {
	if messageID == 0 {
		return // Нечего удалять
	}
	deleteMsgConfig := tgbotapi.NewDeleteMessage(chatID, messageID)
	_, err := b.api.Request(deleteMsgConfig)
	if err != nil {
		// Логируем ошибку, но не прерываем выполнение (сообщение могло быть уже удалено)
		// Игнорируем "message to delete not found" и "message can't be deleted"
		if !strings.Contains(err.Error(), "message to delete not found") && !strings.Contains(err.Error(), "message can't be deleted") {
			log.Printf("[WARN][DeleteMessage] Ошибка удаления сообщения %d в чате %d: %v", messageID, chatID, err)
		}
	} else {
		if b.config.Debug {
			log.Printf("[DEBUG][DeleteMessage] Сообщение %d успешно удалено из чата %d", messageID, chatID)
		}
	}
}

// saveChatSettings сохраняет настройки чата в JSON файл
func saveChatSettings(chatID int64, settings *ChatSettings) error {
	filePath := filepath.Join("data", fmt.Sprintf("settings_%d.json", chatID))
	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return fmt.Errorf("ошибка маршалинга настроек чата %d: %w", chatID, err)
	}

	// Создаем директорию data, если она не существует
	if err := os.MkdirAll("data", 0755); err != nil {
		return fmt.Errorf("ошибка создания директории data: %w", err)
	}

	if err := os.WriteFile(filePath, data, 0644); err != nil {
		return fmt.Errorf("ошибка записи файла настроек чата %d: %w", chatID, err)
	}
	// log.Printf("Настройки для чата %d сохранены в %s", chatID, filePath)
	return nil
}

// loadChatSettings загружает настройки чата из JSON файла
func loadChatSettings(chatID int64) (*ChatSettings, error) {
	filePath := filepath.Join("data", fmt.Sprintf("settings_%d.json", chatID))
	data, err := os.ReadFile(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil // Файл не найден, возвращаем nil без ошибки
		}
		return nil, fmt.Errorf("ошибка чтения файла настроек чата %d: %w", chatID, err)
	}

	var settings ChatSettings
	if err := json.Unmarshal(data, &settings); err != nil {
		return nil, fmt.Errorf("ошибка демаршалинга настроек чата %d: %w", chatID, err)
	}
	// log.Printf("Настройки для чата %d загружены из %s", chatID, filePath)
	return &settings, nil
}

// loadAllChatSettings загружает настройки для всех чатов из папки data
func loadAllChatSettings() (map[int64]*ChatSettings, error) {
	settingsMap := make(map[int64]*ChatSettings)
	files, err := os.ReadDir("data")
	if err != nil {
		if os.IsNotExist(err) {
			log.Println("Директория 'data' не найдена, настройки не загружены.")
			return settingsMap, nil // Не ошибка, просто нет сохраненных настроек
		}
		return nil, fmt.Errorf("ошибка чтения директории data: %w", err)
	}

	for _, file := range files {
		if !file.IsDir() && strings.HasPrefix(file.Name(), "settings_") && strings.HasSuffix(file.Name(), ".json") {
			var chatID int64
			_, err := fmt.Sscan(strings.TrimSuffix(strings.TrimPrefix(file.Name(), "settings_"), ".json"), &chatID)
			if err != nil {
				log.Printf("Ошибка парсинга chatID из имени файла %s: %v", file.Name(), err)
				continue
			}
			settings, err := loadChatSettings(chatID)
			if err != nil {
				log.Printf("Ошибка загрузки настроек из файла %s: %v", file.Name(), err)
				continue
			}
			if settings != nil {
				settingsMap[chatID] = settings
			}
		}
	}
	log.Printf("Загружено %d наборов настроек чатов.", len(settingsMap))
	return settingsMap, nil
}

// getRandomElement возвращает случайный элемент из среза строк
func getRandomElement(slice []string) string {
	if len(slice) == 0 {
		return ""
	}
	rand.Seed(time.Now().UnixNano()) // Убедимся, что генератор случайных чисел инициализирован
	return slice[rand.Intn(len(slice))]
}
