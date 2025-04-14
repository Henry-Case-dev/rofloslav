package bot

import (
	"encoding/json"
	"fmt"
	"log"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	// Импортируем config
	"github.com/Henry-Case-dev/rofloslav/internal/config"
	"github.com/Henry-Case-dev/rofloslav/internal/llm"
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

// formatHistoryWithProfiles форматирует историю сообщений, добавляя информацию о профилях пользователей
// и (если включено) релевантные сообщения из долгосрочной памяти.
func formatHistoryWithProfiles(chatID int64, messages []*tgbotapi.Message, store storage.ChatHistoryStorage, cfg *config.Config, llmClient llm.LLMClient, debug bool, timeZone string) string {
	var historyBuilder strings.Builder

	// 1. Загрузка всех профилей для чата ОДИН РАЗ
	profiles, err := store.GetAllUserProfiles(chatID)
	if err != nil {
		log.Printf("[ERROR][FormatHistory] Чат %d: Ошибка загрузки профилей: %v", chatID, err)
		// Продолжаем без профилей
	}
	profilesMap := make(map[int64]*storage.UserProfile)
	for _, p := range profiles {
		if p != nil {
			profilesMap[p.UserID] = p
		}
	}
	if debug {
		log.Printf("[DEBUG][FormatHistory] Чат %d: Загружено %d профилей.", chatID, len(profilesMap))
	}

	// 2. Загрузка настроек чата (если нужны для форматирования, пока нет)
	// chatSettings, settingsErr := store.GetChatSettings(chatID)
	// ...

	// 3. Загрузка долгосрочной памяти (если включено и есть последний текст)
	longTermMemoryContext := ""
	lastMessageText := "" // Текст последнего сообщения для поиска
	if len(messages) > 0 && messages[len(messages)-1] != nil {
		lastMsg := messages[len(messages)-1]
		if lastMsg.Text != "" {
			lastMessageText = lastMsg.Text
		} else if lastMsg.Caption != "" {
			lastMessageText = lastMsg.Caption
		}
	}

	if cfg != nil && cfg.LongTermMemoryEnabled && lastMessageText != "" && store != nil && llmClient != nil {
		if debug {
			log.Printf("[DEBUG][FormatHistory LTM] Чат %d: Включена долгосрочная память. Ищем %d сообщений, релевантных: '%s...'", chatID, cfg.LongTermMemoryFetchK, lastMessageText[:min(len(lastMessageText), 50)])
		}
		relevantMessages, searchErr := store.SearchRelevantMessages(chatID, lastMessageText, cfg.LongTermMemoryFetchK)
		if searchErr != nil {
			log.Printf("[ERROR][FormatHistory LTM] Чат %d: Ошибка поиска релевантных сообщений: %v", chatID, searchErr)
		} else if len(relevantMessages) > 0 {
			if debug {
				log.Printf("[DEBUG][FormatHistory LTM] Чат %d: Найдено %d релевантных сообщений.", chatID, len(relevantMessages))
			}
			var ltmBuilder strings.Builder
			ltmBuilder.WriteString("### Воспоминания из прошлого (релевантный контекст):\n") // Исправлен текст заголовка
			// Сортируем по дате от старых к новым (если они пришли не отсортированными)
			sort.SliceStable(relevantMessages, func(i, j int) bool {
				return relevantMessages[i].Time().Before(relevantMessages[j].Time())
			})
			for _, relMsg := range relevantMessages {
				if relMsg == nil {
					continue
				}
				userNameOrAlias := "Unknown"
				profile, profileFound := profilesMap[relMsg.From.ID]
				if profileFound && profile.Alias != "" {
					userNameOrAlias = profile.Alias
				} else if relMsg.From != nil {
					userNameOrAlias = relMsg.From.FirstName // Используем FirstName если нет Alias
				}
				relMsgTimeStr := relMsg.Time().Format("2006-01-02 15:04") // Более полный формат для старых сообщений
				relMsgText := relMsg.Text
				if relMsg.Caption != "" {
					relMsgText = relMsg.Caption
				}
				ltmBuilder.WriteString(fmt.Sprintf("- [%s] %s: %s\n", relMsgTimeStr, userNameOrAlias, relMsgText))
			}
			ltmBuilder.WriteString("### Конец воспоминаний\n\n")
			longTermMemoryContext = ltmBuilder.String()
		} else if debug {
			log.Printf("[DEBUG][FormatHistory LTM] Чат %d: Релевантные сообщения не найдены.", chatID)
		}
	}

	// 4. Форматирование основной истории с профилями
	// Загрузка локации один раз
	loc, locErr := time.LoadLocation(timeZone)
	if locErr != nil {
		log.Printf("[WARN][FormatHistory] Чат %d: Не удалось загрузить часовой пояс '%s', использую UTC. Ошибка: %v", chatID, timeZone, locErr)
		loc = time.UTC
	}

	// Добавляем блок долгосрочной памяти в начало
	if longTermMemoryContext != "" {
		historyBuilder.WriteString(longTermMemoryContext)
	}

	// Используем двойные кавычки для строки с переносом
	historyBuilder.WriteString("### Недавняя история сообщений:\n")

	for _, msg := range messages {
		if msg == nil || (msg.Text == "" && msg.Caption == "") { // Пропускаем пустые
			continue
		}

		var profileInfo strings.Builder
		userNameOrAlias := "Unknown User"

		if msg.From != nil {
			profile, profileFound := profilesMap[msg.From.ID]
			if profileFound {
				if profile.Alias != "" {
					userNameOrAlias = profile.Alias
				} else {
					userNameOrAlias = msg.From.FirstName // Фоллбэк на FirstName, если Alias пуст
				}
				profileInfo.WriteString(fmt.Sprintf(" (%s", userNameOrAlias)) // Открываем скобку
				if profile.RealName != "" {
					profileInfo.WriteString(fmt.Sprintf(", Реальное имя: %s", profile.RealName))
				}
				if profile.Gender != "" {
					profileInfo.WriteString(fmt.Sprintf(", Пол: %s", profile.Gender))
				}
				if profile.Bio != "" {
					profileInfo.WriteString(fmt.Sprintf(", Био: %s", profile.Bio))
				}
				// Добавляем LastSeen, если он не нулевой
				if !profile.LastSeen.IsZero() {
					profileInfo.WriteString(fmt.Sprintf(", Последний раз был(а) виден(а): %s", profile.LastSeen.In(loc).Format("2006-01-02 15:04:05 MST")))
				}
				profileInfo.WriteString(")") // Закрываем скобку
			} else {
				// Профиль не найден в БД, используем данные из сообщения
				userNameOrAlias = msg.From.FirstName
				if msg.From.UserName != "" {
					profileInfo.WriteString(fmt.Sprintf(" (@%s)", msg.From.UserName))
				}
			}
		} else {
			userNameOrAlias = "[Unknown Sender]"
		}

		msgTimeStr := msg.Time().In(loc).Format("15:04:05")
		messageText := msg.Text
		if msg.Caption != "" {
			messageText = msg.Caption
		}

		// Форматируем сообщение
		historyBuilder.WriteString(fmt.Sprintf("[%s] %s%s: %s\n",
			msgTimeStr,
			userNameOrAlias,
			profileInfo.String(),
			messageText,
		))
	}
	// Используем двойные кавычки для строки с переносом
	historyBuilder.WriteString("### Конец недавней истории\n")

	if debug && len(messages) > 0 {
		// Убираем перенос строки внутри Printf, объединяя аргументы
		log.Printf("[DEBUG][FormatHistory] Чат %d: Сформирован контекст (%d байт), включая %d сообщений, %d профилей, долгосрочная память: %t.",
			chatID, historyBuilder.Len(), len(messages), len(profilesMap), longTermMemoryContext != "")
	}

	return historyBuilder.String()
}

// formatSingleMessage форматирует одно сообщение для контекста LLM,
// добавляя информацию об авторе из профиля.
func formatSingleMessage(msg *tgbotapi.Message, profiles []*storage.UserProfile, loc *time.Location) string {
	if msg == nil {
		return ""
	}

	// Определяем текст сообщения (текст или подпись)
	messageText := msg.Text
	if messageText == "" && msg.Caption != "" {
		messageText = msg.Caption
	}
	if messageText == "" {
		messageText = "[сообщение без текста/подписи]"
	}

	authorInfo := "Неизвестный"
	if msg.From != nil {
		authorInfo = fmt.Sprintf("%s %s (@%s, ID: %d)", msg.From.FirstName, msg.From.LastName, msg.From.UserName, msg.From.ID)
		// Попробуем найти профиль и использовать Alias
		for _, p := range profiles {
			if p.UserID == msg.From.ID {
				if p.Alias != "" {
					authorInfo = p.Alias // Используем Alias из профиля
				} else {
					authorInfo = msg.From.FirstName // Fallback на FirstName, если Alias пуст
				}
				// Можно добавить гендер или другую инфу из профиля при необходимости
				break
			}
		}
	}

	// Форматируем время сообщения с учетом временной зоны
	msgTime := msg.Time().In(loc).Format("15:04:05")

	// Формируем строку сообщения
	// Используем только Время и Автора для краткости
	return fmt.Sprintf("%s (%s): %s", msgTime, authorInfo, messageText)
}

// findUserProfile находит профиль пользователя в срезе.
// Возвращает указатель на профиль или nil, если не найден.
// (Перенесено в storage)
// func findUserProfile(userID int64, profiles []*storage.UserProfile) *storage.UserProfile {
// 	for _, p := range profiles {
// 		if p.UserID == userID {
// 			return p
// 		}
// 	}
// 	return nil
// }
