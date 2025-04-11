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

	// Импортируем config
	"github.com/Henry-Case-dev/rofloslav/internal/storage"
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

// formatRemainingTime форматирует оставшееся время в строку "X мин Y сек"
func formatRemainingTime(d time.Duration) string {
	if d <= 0 {
		return "0 сек"
	}
	d = d.Round(time.Second)
	m := d / time.Minute
	s := (d % time.Minute) / time.Second
	if m > 0 {
		return fmt.Sprintf("%d мин %d сек", m, s)
	}
	return fmt.Sprintf("%d сек", s)
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

// isAdmin проверяет, является ли пользователь администратором бота.
// Сравнивает username пользователя (без @) со списком AdminUsernames из конфига.
func (b *Bot) isAdmin(user *tgbotapi.User) bool {
	if user == nil {
		return false
	}
	usernameLower := strings.ToLower(user.UserName)
	for _, adminUsername := range b.config.AdminUsernames {
		if strings.ToLower(adminUsername) == usernameLower {
			return true
		}
	}
	return false
}

// parseProfileArgs разбирает текст сообщения для команды /profile_set.
// Ожидаемый формат: @username - Alias - RealName - Bio или @username - Alias - Gender - RealName - Bio
// или @username - Alias - RealName - Gender - Bio
func parseProfileArgs(text string) (targetUsername string, targetUserID int64, alias, gender, realName, bio string, err error) {
	if text == "" {
		err = fmt.Errorf("пустой текст сообщения")
		return
	}

	parts := strings.SplitN(text, " - ", 5) // Разделяем максимум на 5 частей

	// 1. Извлечение username
	if len(parts) < 1 || !strings.HasPrefix(parts[0], "@") {
		err = fmt.Errorf("не удалось извлечь @username из начала строки")
		return
	}
	targetUsername = strings.TrimPrefix(parts[0], "@")

	// 2. Минимум должно быть @username и Alias
	if len(parts) < 2 {
		err = fmt.Errorf("недостаточно аргументов, минимум: @username - Alias")
		return
	}
	alias = strings.TrimSpace(parts[1])

	// 3. Обработка остальных частей (Gender, RealName, Bio)
	// Они могут быть в разном порядке или отсутствовать
	for i := 2; i < len(parts); i++ {
		part := strings.TrimSpace(parts[i])
		if part == "" {
			continue // Пропускаем пустые части
		}

		// Пытаемся определить Gender (например, "male", "female", "m", "f")
		// Можно добавить более строгую валидацию
		lowerPart := strings.ToLower(part)
		if gender == "" && (lowerPart == "male" || lowerPart == "female" || lowerPart == "m" || lowerPart == "f" || lowerPart == "other") {
			gender = part // Сохраняем оригинальное значение, не lowerPart
			continue
		}

		// Если RealName еще не задан, считаем это им
		if realName == "" {
			realName = part
			continue
		}

		// Все остальное считаем частью Bio
		if bio == "" {
			bio = part
		} else {
			// Если частей больше 4, объединяем остаток в Bio
			bio += " - " + part
		}
	}

	// Проверка, что Alias не пустой
	if alias == "" {
		err = fmt.Errorf("Alias не может быть пустым")
		return
	}

	// Возвращаем nil для targetUserID, так как мы не можем определить его из текста
	targetUserID = 0

	return
}

// getUserIDByUsername ищет пользователя в профилях чата по его username.
// Возвращает 0, если пользователь не найден.
// Username передается БЕЗ символа @
func (b *Bot) getUserIDByUsername(chatID int64, username string) (int64, error) {
	profiles, err := b.storage.GetAllUserProfiles(chatID)
	if err != nil {
		return 0, fmt.Errorf("ошибка получения профилей чата: %w", err)
	}

	usernameLower := strings.ToLower(username)
	for _, profile := range profiles {
		if strings.ToLower(profile.Username) == usernameLower {
			return profile.UserID, nil
		}
	}

	return 0, nil // Пользователь не найден
}

// findUserProfileByUsername ищет профиль пользователя по его Username в указанном чате.
// Возвращает nil, nil если профиль не найден.
// Username передается БЕЗ символа @
func (b *Bot) findUserProfileByUsername(chatID int64, username string) (*storage.UserProfile, error) {
	profiles, err := b.storage.GetAllUserProfiles(chatID)
	if err != nil {
		return nil, fmt.Errorf("ошибка получения профилей чата %d для поиска @%s: %w", chatID, username, err)
	}

	usernameLower := strings.ToLower(username)
	for _, profile := range profiles {
		// Сравниваем без учета регистра
		if strings.ToLower(profile.Username) == usernameLower {
			// Нашли профиль, возвращаем его копию (для безопасности)
			foundProfile := *profile // Копируем значение
			return &foundProfile, nil
		}
	}

	// Профиль не найден
	return nil, nil
}

// formatHistoryWithProfiles форматирует историю сообщений и профили пользователей в единый текст
// ОПТИМИЗИРОВАННАЯ ВЕРСИЯ: Загружает профили один раз.
func formatHistoryWithProfiles(chatID int64, messages []*tgbotapi.Message, store storage.ChatHistoryStorage, debug bool) string {
	var contextBuilder strings.Builder

	// 1. Получаем ВСЕ профили для чата ОДНИМ запросом
	allProfiles, err := store.GetAllUserProfiles(chatID)
	if err != nil {
		log.Printf("[WARN][FormatHistory] Чат %d: Ошибка получения профилей: %v. Контекст будет без профилей.", chatID, err)
		// Продолжаем без профилей
	}

	// 2. Создаем карту профилей для быстрого доступа по UserID
	profilesMap := make(map[int64]*storage.UserProfile)
	if len(allProfiles) > 0 {
		for _, p := range allProfiles {
			if p != nil { // Дополнительная проверка на nil
				profilesMap[p.UserID] = p
			}
		}
		if debug && len(profilesMap) > 0 {
			log.Printf("[DEBUG][FormatHistory] Чат %d: Создана карта профилей (%d записей).", chatID, len(profilesMap))
		}
	}

	// 3. Добавляем информацию о профилях в начало контекста (если они есть)
	if len(profilesMap) > 0 {
		contextBuilder.WriteString("=== Информация об участниках чата ===\n")
		profileCount := 0
		for _, p := range profilesMap { // Итерируемся по карте
			profileInfo := fmt.Sprintf("- @%s:", p.Username)
			details := []string{}
			if p.Alias != "" {
				details = append(details, fmt.Sprintf("Прозвище: %s", p.Alias))
			}
			if p.Gender != "" && p.Gender != "unknown" {
				details = append(details, fmt.Sprintf("Пол: %s", p.Gender))
			}
			if p.RealName != "" {
				details = append(details, fmt.Sprintf("Наст.имя: %s", p.RealName))
			}
			if p.Bio != "" {
				details = append(details, fmt.Sprintf("Био: %s", p.Bio))
			}
			if len(details) > 0 {
				profileInfo += " " + strings.Join(details, "; ")
				contextBuilder.WriteString(profileInfo + "\n")
				profileCount++
			}
		}
		if profileCount == 0 { // Если все профили оказались без деталей
			contextBuilder.Reset() // Убираем заголовок
			if debug {
				log.Printf("[DEBUG][FormatHistory] Чат %d: Профили загружены, но без деталей для включения в контекст.", chatID)
			}
		} else {
			contextBuilder.WriteString("=================================\n\n")
			if debug {
				log.Printf("[DEBUG][FormatHistory] Чат %d: Добавлено %d профилей в контекст.", chatID, profileCount)
			}
		}
	} else if debug {
		log.Printf("[DEBUG][FormatHistory] Чат %d: Профили пользователей не найдены или карта пуста.", chatID)
	}

	// 4. Добавляем историю сообщений, используя карту профилей
	contextBuilder.WriteString("=== История сообщений ===\n")
	processedMessages := 0
	for _, msg := range messages {
		if msg == nil || (msg.Text == "" && msg.Caption == "") {
			continue
		}

		var authorInfo string
		if msg.From != nil {
			username := msg.From.UserName
			userID := msg.From.ID
			// Ищем профиль в КАРТЕ
			profile, exists := profilesMap[userID]
			if exists && profile.Alias != "" {
				authorInfo = fmt.Sprintf("[%s (%s, @%s)]", msg.Time().Format("15:04"), profile.Alias, username)
			} else if username != "" {
				authorInfo = fmt.Sprintf("[%s (@%s)]", msg.Time().Format("15:04"), username)
			} else {
				name := msg.From.FirstName
				if name == "" {
					name = fmt.Sprintf("User_%d", userID)
				}
				authorInfo = fmt.Sprintf("[%s (%s)]", msg.Time().Format("15:04"), name)
			}
		} else {
			authorInfo = fmt.Sprintf("[%s (Unknown User)]", msg.Time().Format("15:04"))
		}

		// Добавляем текст или подпись
		messageText := msg.Text
		if messageText == "" && msg.Caption != "" {
			messageText = fmt.Sprintf("[Медиа с подписью: %s]", msg.Caption)
		} else if msg.Photo != nil || msg.Video != nil || msg.Document != nil || msg.Audio != nil || msg.Voice != nil || msg.Sticker != nil {
			if messageText == "" {
				messageText = "[Медиа без подписи]"
			} else {
				messageText = fmt.Sprintf("[Медиа] %s", messageText)
			}
		}

		contextBuilder.WriteString(fmt.Sprintf("%s: %s\n", authorInfo, messageText))
		processedMessages++
	}

	if processedMessages == 0 && contextBuilder.Len() < 50 {
		if debug {
			log.Printf("[DEBUG][FormatHistory] Чат %d: История сообщений пуста или содержит только пустые сообщения.", chatID)
		}
		return ""
	}

	contextBuilder.WriteString("=========================\n")
	if debug {
		log.Printf("[DEBUG][FormatHistory] Чат %d: Добавлено %d сообщений в контекст.", chatID, processedMessages)
	}

	return contextBuilder.String()
}
