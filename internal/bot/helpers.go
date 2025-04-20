package bot

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Henry-Case-dev/rofloslav/internal/config"
	"github.com/Henry-Case-dev/rofloslav/internal/storage"
	"github.com/Henry-Case-dev/rofloslav/internal/utils"
	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// formatRemainingTime форматирует оставшееся время
func formatRemainingTime(d time.Duration) string {
	if d <= 0 {
		return "0с"
	}
	hours := int(d.Hours())
	minutes := int(d.Minutes()) % 60
	seconds := int(d.Seconds()) % 60

	parts := []string{}
	if hours > 0 {
		parts = append(parts, fmt.Sprintf("%dч", hours))
	}
	if minutes > 0 {
		parts = append(parts, fmt.Sprintf("%dм", minutes))
	}
	if seconds > 0 || len(parts) == 0 { // Показываем секунды, если нет часов/минут, или если время < 1 минуты
		parts = append(parts, fmt.Sprintf("%dс", seconds))
	}

	return strings.Join(parts, " ")
}

// saveChatSettings сохраняет настройки чата в JSON файл
func saveChatSettings(chatID int64, settings *ChatSettings) error {
	dataDir := "data"
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return fmt.Errorf("ошибка создания директории data: %w", err)
	}

	filename := filepath.Join(dataDir, fmt.Sprintf("chat_%d_settings.json", chatID))
	file, err := os.Create(filename)
	if err != nil {
		return fmt.Errorf("ошибка создания файла настроек %s: %w", filename, err)
	}
	defer file.Close()

	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ") // Для читаемости файла
	if err := encoder.Encode(settings); err != nil {
		return fmt.Errorf("ошибка кодирования настроек в JSON для чата %d: %w", chatID, err)
	}
	return nil
}

// isAdmin проверяет, является ли пользователь администратором бота
func (b *Bot) isAdmin(user *tgbotapi.User) bool {
	if user == nil {
		return false
	}
	for _, adminUsername := range b.config.AdminUsernames {
		if strings.EqualFold(user.UserName, adminUsername) {
			return true
		}
	}
	return false
}

// getUserIDByUsername ищет ID пользователя по его @username в профилях чата
func (b *Bot) getUserIDByUsername(chatID int64, username string) (int64, error) {
	profiles, err := b.storage.GetAllUserProfiles(chatID)
	if err != nil {
		return 0, fmt.Errorf("ошибка получения профилей: %w", err)
	}

	cleanUsername := strings.TrimPrefix(username, "@")

	for _, p := range profiles {
		if strings.EqualFold(p.Username, cleanUsername) {
			return p.UserID, nil
		}
	}

	return 0, fmt.Errorf("пользователь @%s не найден в профилях этого чата", cleanUsername)
}

// findUserProfileByUsername ищет профиль пользователя по его @username
func (b *Bot) findUserProfileByUsername(chatID int64, username string) (*storage.UserProfile, error) {
	profiles, err := b.storage.GetAllUserProfiles(chatID)
	if err != nil {
		return nil, fmt.Errorf("ошибка получения профилей: %w", err)
	}

	cleanUsername := strings.TrimPrefix(username, "@")

	for _, p := range profiles {
		if strings.EqualFold(p.Username, cleanUsername) {
			return p, nil
		}
	}

	return nil, fmt.Errorf("профиль пользователя @%s не найден", cleanUsername)
}

// formatHistoryWithProfiles форматирует историю сообщений для передачи в LLM,
// добавляя информацию из профилей пользователей.
func formatHistoryWithProfiles(chatID int64, messages []*tgbotapi.Message, store storage.ChatHistoryStorage, cfg *config.Config, timeZone string) string {
	var formattedHistory strings.Builder
	loc, err := time.LoadLocation(timeZone)
	if err != nil {
		log.Printf("[WARN] Ошибка загрузки часового пояса '%s', использую UTC: %v", timeZone, err)
		loc = time.UTC
	}

	// 1. Получаем все профили для этого чата ОДИН РАЗ
	profiles, err := store.GetAllUserProfiles(chatID)
	if err != nil {
		log.Printf("[ERROR] Ошибка получения профилей для чата %d при форматировании истории: %v", chatID, err)
		profiles = []*storage.UserProfile{} // Используем пустой список в случае ошибки
	}

	// Создаем мапу для быстрого доступа к профилям по UserID
	profileMap := make(map[int64]*storage.UserProfile)
	for _, p := range profiles {
		profileMap[p.UserID] = p
	}

	// 2. Форматируем информацию о профилях
	formattedHistory.WriteString("Участники чата и информация о них:\n")
	if len(profiles) > 0 {
		sort.Slice(profiles, func(i, j int) bool {
			return profiles[i].UserID < profiles[j].UserID
		})
		for _, p := range profiles {
			formattedHistory.WriteString(fmt.Sprintf("- Пользователь ID %d (@%s):\n", p.UserID, p.Username))
			formattedHistory.WriteString(fmt.Sprintf("  Прозвище: %s\n", p.Alias))
			if p.Gender != "" {
				formattedHistory.WriteString(fmt.Sprintf("  Пол: %s\n", p.Gender))
			}
			if p.RealName != "" {
				formattedHistory.WriteString(fmt.Sprintf("  Настоящее имя: %s\n", p.RealName))
			}
			if p.Bio != "" {
				formattedHistory.WriteString(fmt.Sprintf("  Bio: %s\n", p.Bio))
			}
			formattedHistory.WriteString(fmt.Sprintf("  Последнее сообщение: %s\n", p.LastSeen.In(loc).Format("2006-01-02 15:04")))
		}
	} else {
		formattedHistory.WriteString("(Информация о профилях недоступна)\n")
	}
	formattedHistory.WriteString("\n---\n")

	// --- Добавляем информацию из долгосрочной памяти (если включено) ОДИН РАЗ --- \
	if cfg.LongTermMemoryEnabled && len(messages) > 0 {
		// Используем текст ПОСЛЕДНЕГО сообщения как запрос для LTM
		lastMsg := messages[len(messages)-1]
		queryText := lastMsg.Text
		if queryText == "" {
			queryText = lastMsg.Caption // Используем подпись, если текст пуст
		}

		if queryText != "" {
			log.Printf("[formatHistory LTM DEBUG] Chat %d: Поиск LTM по запросу: '%s...', K=%d", chatID, truncateString(queryText, 50), cfg.LongTermMemoryFetchK)
			relevantMsgs, searchErr := store.SearchRelevantMessages(chatID, queryText, cfg.LongTermMemoryFetchK)
			if searchErr != nil {
				log.Printf("[ERROR][formatHistory LTM] Ошибка поиска релевантных сообщений для чата %d (запрос по последнему сообщению): %v", chatID, searchErr)
			} else if len(relevantMsgs) > 0 {
				log.Printf("[formatHistory LTM DEBUG] Chat %d: Найдено %d релевантных сообщений.", chatID, len(relevantMsgs))
				formattedHistory.WriteString(fmt.Sprintf("[Контекст долгосрочной памяти для '%s...']:\n", truncateString(queryText, 30)))
				// Сортируем по дате от старых к новым
				sort.SliceStable(relevantMsgs, func(i, j int) bool {
					// Сравнение может потребовать парсинга или использования временных меток, если они есть
					// Предполагаем, что Date - это int Unix timestamp
					return relevantMsgs[i].Date < relevantMsgs[j].Date
				})
				for _, relMsg := range relevantMsgs {
					userAlias := "Неизвестно"
					if relMsg.From != nil {
						if profile, ok := profileMap[relMsg.From.ID]; ok {
							userAlias = profile.Alias
						} else {
							userAlias = relMsg.From.FirstName
						}
					} else if relMsg.SenderChat != nil {
						userAlias = relMsg.SenderChat.Title
					}
					msgTime := time.Unix(int64(relMsg.Date), 0).In(loc)
					msgText := relMsg.Text
					if msgText == "" {
						msgText = relMsg.Caption
					}
					formattedHistory.WriteString(fmt.Sprintf("> [%s] %s: %s\n",
						msgTime.Format("2006-01-02 15:04"), userAlias, msgText))
				}
				formattedHistory.WriteString("[Конец контекста долгосрочной памяти]\n\n---\n")
			}
		}
	}
	// --- Конец добавления информации из долгосрочной памяти --- \

	formattedHistory.WriteString("История сообщений (новые внизу):\n")

	// 3. Форматируем историю сообщений, используя мапу профилей
	for _, msg := range messages {
		userAlias := "Неизвестно"
		var userID int64
		if msg.From != nil {
			userID = msg.From.ID
			if profile, ok := profileMap[userID]; ok {
				userAlias = profile.Alias // Используем Alias из профиля
			} else {
				userAlias = msg.From.FirstName // Запасной вариант, если профиля нет
			}
		} else if msg.SenderChat != nil { // Сообщение от имени канала
			userAlias = msg.SenderChat.Title
			userID = msg.SenderChat.ID // Используем ID чата как userID для каналов
		}

		msgTime := time.Unix(int64(msg.Date), 0).In(loc)
		formattedTime := msgTime.Format("15:04")

		// Формируем текст сообщения, включая подпись, если есть
		messageText := msg.Text
		if msg.Caption != "" {
			if messageText != "" {
				messageText += "\n" + msg.Caption // Добавляем подпись к тексту
			} else {
				messageText = msg.Caption
			}
		}

		// Добавляем информацию о пересылке, если есть
		if msg.ForwardDate > 0 {
			forwardedFromAlias := "Неизвестный источник"
			if msg.ForwardFrom != nil {
				if profile, ok := profileMap[msg.ForwardFrom.ID]; ok {
					forwardedFromAlias = profile.Alias
				} else {
					forwardedFromAlias = msg.ForwardFrom.FirstName
				}
			} else if msg.ForwardFromChat != nil {
				forwardedFromAlias = fmt.Sprintf("Канал '%s'", msg.ForwardFromChat.Title)
			} else if msg.ForwardSenderName != "" {
				forwardedFromAlias = msg.ForwardSenderName
			}
			formattedHistory.WriteString(fmt.Sprintf("> %s (%s) [переслано от %s]: %s\n",
				formattedTime, userAlias, forwardedFromAlias, messageText))
		} else {
			formattedHistory.WriteString(fmt.Sprintf("> %s (%s): %s\n",
				formattedTime, userAlias, messageText))
		}
	}

	// Обрезаем историю, если она слишком длинная для модели
	// Используем простой подсчет символов (можно улучшить до токенов)
	// maxLen := 30000 // Примерный лимит для Gemini Flash (нужно уточнить)
	// if formattedHistory.Len() > maxLen {
	// 	log.Printf("[WARN] История для чата %d (%d символов) слишком длинная, обрезаю до %d", chatID, formattedHistory.Len(), maxLen)
	// 	// Простой способ обрезки - удаляем начало строки
	// 	startIndex := formattedHistory.Len() - maxLen
	// 	// Ищем ближайший перенос строки после startIndex, чтобы не резать посередине
	// 	newLineIndex := strings.Index(formattedHistory.String()[startIndex:], "\n")
	// 	if newLineIndex != -1 {
	// 		startIndex += newLineIndex + 1
	// 	}
	// 	return formattedHistory.String()[startIndex:]
	// }

	return formattedHistory.String()
}

// formatDirectReplyContext формирует структурированный контекст для прямого ответа,
// объединяя ветку ответов, общий контекст и релевантные сообщения (RAG).
func formatDirectReplyContext(chatID int64,
	replyChain []*tgbotapi.Message,
	commonContext []*tgbotapi.Message,
	relevantMessages []*tgbotapi.Message,
	store storage.ChatHistoryStorage,
	cfg *config.Config,
	timeZone string) string {

	var builder strings.Builder
	seenMessageIDs := make(map[int]bool) // Для отслеживания дубликатов

	// --- 1. Ветка ответов (Приоритет) ---
	builder.WriteString("=== НЕПОСРЕДСТВЕННЫЙ ДИАЛОГ (Ветка ответов) ===\n")
	if len(replyChain) > 0 {
		formattedChain := formatMessagesWithProfilesInternal(chatID, replyChain, store, cfg, timeZone, seenMessageIDs)
		builder.WriteString(formattedChain)
	} else {
		builder.WriteString("(Нет сообщений в прямой ветке ответа)\n")
	}
	builder.WriteString("\n") // Отступ между секциями

	// --- 2. Общий контекст чата (Недавние сообщения) ---
	builder.WriteString("=== ОБЩИЙ КОНТЕКСТ ЧАТА (Недавние сообщения) ===\n")
	if len(commonContext) > 0 {
		formattedCommon := formatMessagesWithProfilesInternal(chatID, commonContext, store, cfg, timeZone, seenMessageIDs)
		if formattedCommon != "" { // Если остались сообщения после удаления дубликатов
			builder.WriteString(formattedCommon)
		} else {
			builder.WriteString("(Все недавние сообщения уже присутствуют в ветке диалога)\n")
		}
	} else {
		builder.WriteString("(Нет недавних сообщений в общем контексте)\n")
	}
	builder.WriteString("\n")

	// --- 3. Релевантные сообщения из прошлого (Долгосрочная память/RAG) ---
	if cfg.LongTermMemoryEnabled && len(relevantMessages) > 0 { // Показываем секцию только если RAG включен и что-то найдено
		builder.WriteString("=== РЕЛЕВАНТНЫЕ СООБЩЕНИЯ ИЗ ПРОШЛОГО (Долгосрочная память) ===\n")
		formattedRelevant := formatMessagesWithProfilesInternal(chatID, relevantMessages, store, cfg, timeZone, seenMessageIDs)
		if formattedRelevant != "" { // Если остались сообщения после удаления дубликатов
			builder.WriteString(formattedRelevant)
		} else {
			builder.WriteString("(Все найденные релевантные сообщения уже присутствуют в ветке диалога или общем контексте)\n")
		}
	}

	return builder.String()
}

// Вспомогательная функция для форматирования сообщений с учетом профилей и дубликатов
// Модифицированная версия formatHistoryWithProfiles
func formatMessagesWithProfilesInternal(chatID int64, messages []*tgbotapi.Message, store storage.ChatHistoryStorage, cfg *config.Config, timeZone string, seenMessageIDs map[int]bool) string {
	var builder strings.Builder
	profiles := make(map[int64]*storage.UserProfile) // Кеш профилей для этого вызова
	loc, _ := time.LoadLocation(timeZone)

	for _, msg := range messages {
		// Пропускаем дубликаты
		if seenMessageIDs[msg.MessageID] {
			continue
		}

		// ... (Остальная логика форматирования одного сообщения из formatHistoryWithProfiles)
		// Получаем профиль, форматируем время, текст и т.д.
		var authorAlias string
		var authorBio string
		var profileInfo string

		if msg.From != nil {
			userID := msg.From.ID
			// Проверяем кеш профилей
			profile, found := profiles[userID]
			if !found {
				// Загружаем профиль, если не в кеше
				loadedProfile, err := store.GetUserProfile(chatID, userID)
				if err != nil {
					log.Printf("[WARN][formatMsgInternal] Chat %d: Ошибка загрузки профиля для userID %d: %v", chatID, userID, err)
				} else if loadedProfile != nil {
					profiles[userID] = loadedProfile // Сохраняем в кеш
					profile = loadedProfile
				}
			}

			// Определяем алиас
			if profile != nil && profile.Alias != "" {
				authorAlias = profile.Alias
			} else if msg.From.FirstName != "" {
				authorAlias = msg.From.FirstName
			} else if msg.From.UserName != "" {
				authorAlias = msg.From.UserName
			} else {
				authorAlias = fmt.Sprintf("User_%d", userID)
			}

			// Получаем Bio, если есть
			if profile != nil && profile.Bio != "" {
				authorBio = profile.Bio
				profileInfo = fmt.Sprintf(" (Bio: %s)", utils.TruncateString(authorBio, 100)) // Добавляем инфо о Bio
			}
		} else if msg.SenderChat != nil { // Сообщение от имени канала/чата
			authorAlias = msg.SenderChat.Title
			if authorAlias == "" {
				authorAlias = fmt.Sprintf("Chat_%d", msg.SenderChat.ID)
			}
		} else {
			authorAlias = "Неизвестный"
		}

		msgTime := time.Unix(int64(msg.Date), 0).In(loc)
		formattedTime := msgTime.Format("15:04:05")

		// Используем текст сообщения или подпись
		msgText := msg.Text
		if msgText == "" {
			msgText = msg.Caption
		}

		// Добавляем информацию о голосовом сообщении
		voiceIndicator := ""
		if msg.Voice != nil { // Проверяем наличие оригинального Voice объекта
			voiceIndicator = "🗣️ "
		}

		// Добавляем информацию об ответе
		replyIndicator := ""
		if msg.ReplyToMessage != nil {
			replyIndicator = fmt.Sprintf(" (в ответ на #%d)", msg.ReplyToMessage.MessageID)
		}

		builder.WriteString(fmt.Sprintf("%s (%s)%s%s:%s %s\n",
			formattedTime,
			authorAlias,
			profileInfo,    // Добавлено инфо о Bio
			replyIndicator, // Добавлено инфо об ответе
			voiceIndicator, // Добавлен индикатор голоса
			msgText,
		))

		// Отмечаем ID как увиденный
		seenMessageIDs[msg.MessageID] = true
	}

	return builder.String()
}
