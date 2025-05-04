package bot

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/Henry-Case-dev/rofloslav/internal/config"
	"github.com/Henry-Case-dev/rofloslav/internal/storage"
	"github.com/Henry-Case-dev/rofloslav/internal/utils"
	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// ModerationService обрабатывает логику модерации чатов
type ModerationService struct {
	bot             *Bot                          // Ссылка на основной объект бота
	messageCounters map[int64]int                 // Счетчик сообщений для каждого чата [chatID]count
	messageBuffer   map[int64][]*tgbotapi.Message // Буфер сообщений для каждого чата [chatID]messages
	// activePurges отслеживает активные задачи очистки [chatID][userID]cancelFunc
	activePurges map[int64]map[int64]context.CancelFunc
	rules        []config.ModerationRule // Загруженные правила модерации
	activeChats  map[int64]bool          // Чаты, в которых модерация активна [chatID]isActive
	mutex        sync.RWMutex            // Мьютекс для защиты доступа к картам
}

// NewModerationService создает новый экземпляр сервиса модерации
func NewModerationService(bot *Bot) *ModerationService {
	return &ModerationService{
		bot:             bot,
		messageCounters: make(map[int64]int),
		messageBuffer:   make(map[int64][]*tgbotapi.Message),
		activePurges:    make(map[int64]map[int64]context.CancelFunc),
		rules:           bot.config.ModRules, // Копируем правила из конфига
		activeChats:     make(map[int64]bool),
		mutex:           sync.RWMutex{},
	}
}

// CheckAdminRightsAndActivate проверяет права администратора бота в чате
// и активирует/деактивирует модерацию для этого чата.
func (ms *ModerationService) CheckAdminRightsAndActivate(chatID int64) {
	ms.mutex.Lock()
	defer ms.mutex.Unlock()

	shouldBeActive := false // По умолчанию модерация выключена
	notificationText := ""

	// 1. Проверяем, нужно ли проверять права админа
	if ms.bot.config.ModCheckAdminRights {
		member, err := ms.bot.getBotMember(chatID)
		if err != nil {
			log.Printf("[Moderation Check ERROR] Чат %d: Не удалось получить информацию о боте: %v", chatID, err)
			// Не активируем модерацию при ошибке
			ms.activeChats[chatID] = false
			ms.bot.sendAutoDeleteMessage(chatID, fmt.Sprintf("⚠️ Не удалось проверить права администратора для модерации: %v", err), 10*time.Second)
			return
		}

		if member.Status == "administrator" || member.Status == "creator" {
			if ms.bot.config.Debug {
				log.Printf("[Moderation Check DEBUG] Чат %d: Бот является администратором (статус: %s). Модерация будет включена.", chatID, member.Status)
			}
			shouldBeActive = true
			notificationText = "✅ Модерация включена (бот является администратором)."
		} else {
			if ms.bot.config.Debug {
				log.Printf("[Moderation Check DEBUG] Чат %d: Бот НЕ является администратором (статус: %s). Модерация будет выключена.", chatID, member.Status)
			}
			shouldBeActive = false
			notificationText = "ℹ️ Модерация неактивна. Бот должен быть администратором чата."
		}
	} else {
		// Проверка прав отключена в конфиге, включаем модерацию по умолчанию
		if ms.bot.config.Debug {
			log.Printf("[Moderation Check DEBUG] Чат %d: Проверка прав администратора отключена (MOD_CHECK_ADMIN_RIGHTS=false). Модерация включена по умолчанию.", chatID)
		}
		shouldBeActive = true
		notificationText = "✅ Модерация включена (проверка прав отключена)."
	}

	// 2. Обновляем статус и отправляем уведомление
	wasActive := ms.activeChats[chatID]
	ms.activeChats[chatID] = shouldBeActive

	// Отправляем уведомление только если статус изменился или это первая проверка
	if wasActive != shouldBeActive {
		ms.bot.sendAutoDeleteMessage(chatID, notificationText, 10*time.Second)
		if shouldBeActive {
			log.Printf("[Moderation INFO] Чат %d: Модерация АКТИВИРОВАНА.", chatID)
			// Сбрасываем счетчик и буфер при активации
			ms.messageCounters[chatID] = 0
			ms.messageBuffer[chatID] = make([]*tgbotapi.Message, 0, ms.bot.config.ModInterval)
		} else {
			log.Printf("[Moderation INFO] Чат %d: Модерация ДЕАКТИВИРОВАНА.", chatID)
			// Очищаем счетчик и буфер при деактивации
			delete(ms.messageCounters, chatID)
			delete(ms.messageBuffer, chatID)
			// TODO: Отменить активные purge для этого чата? Возможно, стоит оставить их дорабатывать.
		}
	}
}

// ProcessIncomingMessage обрабатывает входящее сообщение для модерации.
// Увеличивает счетчик, добавляет в буфер и запускает проверку, если достигнут интервал.
func (ms *ModerationService) ProcessIncomingMessage(message *tgbotapi.Message) {
	chatID := message.Chat.ID

	ms.mutex.Lock()
	// Проверяем, активна ли модерация для этого чата
	if !ms.activeChats[chatID] {
		ms.mutex.Unlock()
		if ms.bot.config.Debug {
			// Не логируем каждое сообщение, чтобы не спамить
			// log.Printf("[Moderation Process DEBUG] Чат %d: Модерация не активна, сообщение ID %d пропущено.", chatID, message.MessageID)
		}
		return
	}

	// Увеличиваем счетчик и добавляем сообщение в буфер
	ms.messageCounters[chatID]++
	currentCount := ms.messageCounters[chatID]
	ms.messageBuffer[chatID] = append(ms.messageBuffer[chatID], message)

	if ms.bot.config.Debug {
		log.Printf("[Moderation Process DEBUG] Чат %d: Сообщение ID %d добавлено в буфер. Счетчик: %d/%d", chatID, message.MessageID, currentCount, ms.bot.config.ModInterval)
	}

	// Проверяем, достигнут ли интервал
	if currentCount >= ms.bot.config.ModInterval {
		log.Printf("[Moderation Process INFO] Чат %d: Достигнут интервал проверки (%d). Запуск анализа пакета сообщений.", chatID, ms.bot.config.ModInterval)
		// Копируем буфер, чтобы избежать гонки данных при асинхронной обработке
		messagesToProcess := make([]*tgbotapi.Message, len(ms.messageBuffer[chatID]))
		copy(messagesToProcess, ms.messageBuffer[chatID])

		// Сбрасываем счетчик и очищаем буфер
		ms.messageCounters[chatID] = 0
		ms.messageBuffer[chatID] = make([]*tgbotapi.Message, 0, ms.bot.config.ModInterval)

		// Разблокируем мьютекс ПЕРЕД запуском горутины
		ms.mutex.Unlock()

		// Запускаем обработку пакета в отдельной горутине
		go ms.processMessageBatch(chatID, messagesToProcess)
	} else {
		// Интервал не достигнут, просто разблокируем мьютекс
		ms.mutex.Unlock()
	}
}

// processMessageBatch обрабатывает пакет сообщений, проверяя их на соответствие правилам.
func (ms *ModerationService) processMessageBatch(chatID int64, messages []*tgbotapi.Message) {
	if ms.bot.config.Debug {
		log.Printf("[Moderation Batch DEBUG] Чат %d: Начало обработки пакета из %d сообщений.", chatID, len(messages))
	}

	// Получаем все профили пользователей для этого чата ОДИН РАЗ
	profiles, err := ms.bot.storage.GetAllUserProfiles(chatID)
	if err != nil {
		log.Printf("[Moderation Batch ERROR] Чат %d: Не удалось получить профили пользователей: %v. LLM контекст будет без псевдонимов.", chatID, err)
		profiles = []*storage.UserProfile{} // Используем пустой список в случае ошибки
	}
	profileMap := make(map[int64]*storage.UserProfile)
	for _, p := range profiles {
		profileMap[p.UserID] = p
	}

	// Форматируем весь пакет для возможной передачи в LLM
	// Это делается один раз для всего пакета, чтобы передать контекст
	contextForLLM := ms.formatContextForLLM(messages, profileMap)

messageLoop:
	for _, msg := range messages {
		if msg == nil || msg.From == nil { // Пропускаем системные сообщения или сообщения без автора
			continue
		}

		userID := msg.From.ID
		messageText := msg.Text // Используем текст или подпись
		if messageText == "" {
			messageText = msg.Caption
		}
		if messageText == "" {
			continue // Пропускаем сообщения без текста/подписи
		}

		for _, rule := range ms.rules {
			// 1. Проверка соответствия Chat ID
			// ParsedChatID = 0 - для всех чатов, -1 - ошибка парсинга
			if rule.ParsedChatID != 0 && rule.ParsedChatID != chatID && rule.ParsedChatID != -1 {
				continue // Правило не для этого чата
			}

			// 2. Проверка соответствия User ID
			// ParsedUserID = 0 - для всех юзеров, -1 - ошибка парсинга
			if rule.ParsedUserID != 0 && rule.ParsedUserID != userID && rule.ParsedUserID != -1 {
				continue // Правило не для этого пользователя
			}

			// 3. Проверка ключевых слов
			if !ms.matchKeywords(messageText, rule.Keywords) {
				continue // Ключевые слова не найдены
			}

			// --- Правило сработало ---
			log.Printf("[Moderation Trigger INFO] Чат %d: Сообщение ID %d от пользователя %d (@%s) попало под правило '%s'.",
				chatID, msg.MessageID, userID, msg.From.UserName, rule.RuleName)

			// 4. Проверка LLM (если требуется)
			applyPunishment := false
			if rule.LLMInstruction == "none" {
				log.Printf("[Moderation Trigger DEBUG] Чат %d, Правило '%s': LLM проверка пропущена (instruction='none'). Наказание будет применено.", chatID, rule.RuleName)
				applyPunishment = true
			} else {
				log.Printf("[Moderation Trigger DEBUG] Чат %d, Правило '%s': Требуется проверка LLM.", chatID, rule.RuleName)
				// Вызываем LLM с отформатированным контекстом и инструкцией из правила
				llmVerdict, llmError := ms.bot.llm.GenerateArbitraryResponse(rule.LLMInstruction, contextForLLM)

				if llmError != nil {
					log.Printf("[Moderation LLM ERROR] Чат %d, Правило '%s': Ошибка при вызове LLM: %v", chatID, rule.RuleName, llmError)
					// Не применяем наказание при ошибке LLM
				} else {
					if strings.HasPrefix(strings.ToUpper(strings.TrimSpace(llmVerdict)), "ПОЛОЖИТЕЛЬНО") {
						log.Printf("[Moderation LLM INFO] Чат %d, Правило '%s': LLM вернул ПОЛОЖИТЕЛЬНЫЙ вердикт. Наказание будет применено.", chatID, rule.RuleName)
						applyPunishment = true
					} else {
						log.Printf("[Moderation LLM INFO] Чат %d, Правило '%s': LLM вернул ОТРИЦАТЕЛЬНЫЙ вердикт. Наказание не применяется.", chatID, rule.RuleName)
					}
				}
			}

			// 5. Применение наказания (если требуется)
			if applyPunishment {
				ms.applyPunishment(chatID, userID, msg.From.UserName, rule, msg)
			}

			// Прерываем проверку ПРАВИЛ для ТЕКУЩЕГО сообщения, так как одно правило уже сработало
			continue messageLoop
		}
	}

	if ms.bot.config.Debug {
		log.Printf("[Moderation Batch DEBUG] Чат %d: Завершена обработка пакета из %d сообщений.", chatID, len(messages))
	}
}

// matchKeywords проверяет, содержит ли текст хотя бы одно из ключевых слов/фраз.
// Проверка выполняется без учета регистра, ищет целые слова/фразы.
func (ms *ModerationService) matchKeywords(text string, keywords []string) bool {
	if len(keywords) == 0 {
		return false // Нет ключевых слов для проверки
	}
	if len(keywords) == 1 && strings.ToLower(keywords[0]) == "любые" {
		return true // Срабатывает всегда
	}

	lowerText := " " + strings.ToLower(text) + " " // Добавляем пробелы для поиска целых слов

	for _, keyword := range keywords {
		lowerKeyword := strings.ToLower(strings.TrimSpace(keyword))
		if lowerKeyword == "" {
			continue // Пропускаем пустые ключевые слова
		}

		// Ищем ключевое слово/фразу, окруженную не-буквенно-цифровыми символами или пробелами
		// Простой вариант: ищем " keyword " (с пробелами)
		if strings.Contains(lowerText, " "+lowerKeyword+" ") {
			if ms.bot.config.Debug {
				log.Printf("[Moderation Keyword DEBUG] Найдено ключевое слово '%s' в тексте: %s...", keyword, utils.TruncateString(text, 50))
			}
			return true
		}

		// TODO: Добавить более продвинутый поиск похожих слов (например, расстояние Левенштейна), если требуется.
		// Сейчас реализован только поиск точного совпадения целого слова/фразы.
	}

	return false // Ни одно ключевое слово не найдено
}

// formatContextForLLM форматирует пакет сообщений для передачи в LLM,
// заменяя ID пользователей на их псевдонимы/имена.
func (ms *ModerationService) formatContextForLLM(messages []*tgbotapi.Message, profileMap map[int64]*storage.UserProfile) string {
	var contextBuilder strings.Builder
	loc, _ := time.LoadLocation(ms.bot.config.TimeZone)

	contextBuilder.WriteString("Анализируемый контекст сообщений:\n")

	for _, msg := range messages {
		if msg == nil {
			continue
		}

		authorIdentifier := "Unknown"
		if msg.From != nil {
			userID := msg.From.ID
			if profile, ok := profileMap[userID]; ok && profile.Alias != "" {
				authorIdentifier = profile.Alias
			} else if msg.From.UserName != "" {
				authorIdentifier = "@" + msg.From.UserName
			} else if msg.From.FirstName != "" {
				authorIdentifier = msg.From.FirstName
			} else {
				authorIdentifier = fmt.Sprintf("User_%d", userID)
			}
		} else if msg.SenderChat != nil {
			authorIdentifier = msg.SenderChat.Title
			if authorIdentifier == "" {
				authorIdentifier = fmt.Sprintf("Chat_%d", msg.SenderChat.ID)
			}
		}

		msgTime := time.Unix(int64(msg.Date), 0).In(loc)
		formattedTime := msgTime.Format("15:04:05")

		messageText := msg.Text
		if messageText == "" {
			messageText = msg.Caption
		}

		// Добавляем префикс с автором и временем
		contextBuilder.WriteString(fmt.Sprintf("[%s] %s: %s\n",
			formattedTime, authorIdentifier, messageText))
	}

	return contextBuilder.String()
}

// applyPunishment применяет указанное наказание к пользователю.
func (ms *ModerationService) applyPunishment(chatID int64, userID int64, username string, rule config.ModerationRule, triggerMessage *tgbotapi.Message) {
	logPrefix := fmt.Sprintf("[Moderation Apply] Чат %d, Правило '%s', Пользователь %d (@%s):", chatID, rule.RuleName, userID, username)

	// 1. Определяем длительность наказания (для mute/ban)
	var untilDate int64 = 0 // 0 означает навсегда или не применимо
	switch rule.Punishment {
	case config.PunishMute:
		if ms.bot.config.ModMuteTimeMin > 0 {
			untilDate = time.Now().Add(time.Duration(ms.bot.config.ModMuteTimeMin) * time.Minute).Unix()
		}
	case config.PunishBan:
		if ms.bot.config.ModBanTimeMin > 0 {
			untilDate = time.Now().Add(time.Duration(ms.bot.config.ModBanTimeMin) * time.Minute).Unix()
		}
	}

	// 2. Применяем наказание через Telegram API
	success := false
	var apiErr error
	switch rule.Punishment {
	case config.PunishMute:
		restrictConfig := tgbotapi.RestrictChatMemberConfig{
			ChatMemberConfig: tgbotapi.ChatMemberConfig{
				ChatID: chatID,
				UserID: userID,
			},
			UntilDate: untilDate,
			Permissions: &tgbotapi.ChatPermissions{ // Запрещаем всё, кроме чтения
				CanSendMessages:       false,
				CanSendMediaMessages:  false,
				CanSendPolls:          false,
				CanSendOtherMessages:  false,
				CanAddWebPagePreviews: false,
				CanChangeInfo:         false,
				CanInviteUsers:        false,
				CanPinMessages:        false,
			},
		}
		_, apiErr = ms.bot.api.Request(restrictConfig)
		if apiErr == nil {
			success = true
			log.Printf("%s Применен MUTE (до %v).", logPrefix, time.Unix(untilDate, 0))
		}

	case config.PunishKick:
		banConfig := tgbotapi.BanChatMemberConfig{
			ChatMemberConfig: tgbotapi.ChatMemberConfig{ChatID: chatID, UserID: userID},
			UntilDate:        time.Now().Add(1 * time.Minute).Unix(), // Кик = бан на 1 минуту
			RevokeMessages:   false,                                  // Не удаляем сообщения при кике
		}
		_, apiErr = ms.bot.api.Request(banConfig)
		if apiErr == nil {
			success = true
			log.Printf("%s Применен KICK.", logPrefix)
			// Разбан не нужен, так как бан временный
		}

	case config.PunishBan:
		banConfig := tgbotapi.BanChatMemberConfig{
			ChatMemberConfig: tgbotapi.ChatMemberConfig{ChatID: chatID, UserID: userID},
			UntilDate:        untilDate,
			RevokeMessages:   false, // Пока не удаляем сообщения при бане, можно сделать опцией
		}
		_, apiErr = ms.bot.api.Request(banConfig)
		if apiErr == nil {
			success = true
			log.Printf("%s Применен BAN (до %v).", logPrefix, time.Unix(untilDate, 0))
		}

	case config.PunishPurge:
		purgeDuration := ms.bot.config.ModPurgeDuration
		log.Printf("%s Запуск PURGE сообщений пользователя за последние %v.", logPrefix, purgeDuration)
		// Запускаем асинхронную очистку
		purgeCtx, cancelFunc := context.WithCancel(context.Background())
		ms.mutex.Lock() // Защищаем доступ к activePurges
		if _, ok := ms.activePurges[chatID]; !ok {
			ms.activePurges[chatID] = make(map[int64]context.CancelFunc)
		}
		// Отменяем предыдущий purge для этого юзера, если он был
		if existingCancel, ok := ms.activePurges[chatID][userID]; ok {
			log.Printf("%s Обнаружен предыдущий активный purge, отменяем его.", logPrefix)
			existingCancel()
		}
		ms.activePurges[chatID][userID] = cancelFunc
		ms.mutex.Unlock()

		go ms.purgeUserMessages(purgeCtx, chatID, userID, purgeDuration, rule.RuleName)
		success = true // Считаем успешным запуск задачи

	case config.PunishNone:
		log.Printf("%s Тип наказания 'none', никаких действий не предпринято.", logPrefix)
		success = true // Действий не было, но и ошибки нет

	default:
		log.Printf("%s Неизвестный тип наказания: %s", logPrefix, rule.Punishment)
		apiErr = fmt.Errorf("неизвестный тип наказания: %s", rule.Punishment)
	}

	// 3. Обработка ошибок API
	if apiErr != nil {
		log.Printf("%s Ошибка применения наказания (%s) через API: %v", logPrefix, rule.Punishment, apiErr)
		// Отправляем сообщение об ошибке в чат (с автоудалением)
		notifyText := fmt.Sprintf("⚠️ Ошибка модерации (правило '%s'): Не удалось применить наказание '%s' к @%s. Ошибка: %v",
			rule.RuleName, rule.Punishment, username, apiErr)
		ms.bot.sendAutoDeleteMessage(chatID, notifyText, 15*time.Second)
		return // Прерываем выполнение, если наказание не удалось применить
	}

	// 4. Уведомления (если наказание успешно применено или тип 'none')
	if success {
		notifyEnabled := ms.bot.config.ModDefaultNotify
		// Переопределение стандартной настройки
		// (В текущей структуре rule нет поля для переопределения, используем Chat/User Notify)

		punishmentDurationStr := ""
		if untilDate > 0 {
			punishmentDurationStr = fmt.Sprintf(" до %s", time.Unix(untilDate, 0).Format("2006-01-02 15:04:05 MST"))
		}

		baseNotifyText := fmt.Sprintf("Модерация: К @%s применено наказание '%s'%s по правилу '%s'.",
			username, rule.Punishment, punishmentDurationStr, rule.RuleName)
		if rule.Punishment == config.PunishKick {
			baseNotifyText = fmt.Sprintf("Модерация: @%s кикнут по правилу '%s'.", username, rule.RuleName)
		}
		if rule.Punishment == config.PunishPurge {
			baseNotifyText = fmt.Sprintf("Модерация: Запущена очистка сообщений @%s по правилу '%s'.", username, rule.RuleName)
		}
		if rule.Punishment == config.PunishNone {
			baseNotifyText = fmt.Sprintf("Модерация: Зафиксировано нарушение правила '%s' пользователем @%s.", rule.RuleName, username)
		}

		note := ""
		if rule.PunishmentNote != "" {
			note = fmt.Sprintf("\nПримечание: %s", rule.PunishmentNote)
		}

		// Уведомление в чат
		if rule.NotifyChat && notifyEnabled {
			ms.bot.sendAutoDeleteMessage(chatID, baseNotifyText+note, 10*time.Second)
		}

		// Уведомление пользователя в ЛС
		if rule.NotifyUser && notifyEnabled {
			// Отправляем сообщение в ЛС пользователю userID
			// Убедимся, что не отправляем ему его же сообщение (если purge)
			if rule.Punishment != config.PunishPurge {
				pmText := fmt.Sprintf("В чате (ID: %d) к вам применено наказание '%s'%s по правилу '%s'.%s",
					chatID, rule.Punishment, punishmentDurationStr, rule.RuleName, note)
				ms.bot.sendReply(userID, pmText) // Отправляем в ЛС
			}
		}
	}
}

// purgeUserMessages асинхронно удаляет сообщения пользователя за указанный период.
func (ms *ModerationService) purgeUserMessages(ctx context.Context, chatID int64, userID int64, duration time.Duration, ruleName string) {
	logPrefix := fmt.Sprintf("[Moderation Purge] Чат %d, Правило '%s', Пользователь %d:", chatID, ruleName, userID)
	startTime := time.Now()
	deletedCount := 0

	defer func() {
		// Удаляем запись об активном purge после завершения
		ms.mutex.Lock()
		if chatPurges, ok := ms.activePurges[chatID]; ok {
			delete(chatPurges, userID)
			if len(chatPurges) == 0 {
				delete(ms.activePurges, chatID)
			}
		}
		ms.mutex.Unlock()
		log.Printf("%s Завершена очистка сообщений. Удалено: %d. Время: %v.", logPrefix, deletedCount, time.Since(startTime))
	}()

	// 1. Получаем сообщения пользователя за период
	sinceTime := time.Now().Add(-duration)
	messagesToDelete, err := ms.bot.storage.GetMessagesSince(ctx, chatID, userID, sinceTime, 0) // 0 - без лимита
	if err != nil {
		log.Printf("%s Ошибка получения сообщений для удаления: %v", logPrefix, err)
		return
	}

	if len(messagesToDelete) == 0 {
		log.Printf("%s Не найдено сообщений для удаления за период %v.", logPrefix, duration)
		return
	}

	log.Printf("%s Найдено %d сообщений для удаления.", logPrefix, len(messagesToDelete))

	// 2. Удаляем сообщения по одному с небольшой задержкой
	for _, msg := range messagesToDelete {
		// Проверяем контекст отмены перед каждым удалением
		select {
		case <-ctx.Done():
			log.Printf("%s Операция очистки отменена.", logPrefix)
			return
		default:
			// Продолжаем удаление
		}

		ms.bot.deleteMessage(chatID, msg.MessageID)
		deletedCount++
		time.Sleep(100 * time.Millisecond) // Небольшая задержка, чтобы не перегружать API
	}
}

// StopPurge отменяет активную задачу очистки сообщений для пользователя.
func (ms *ModerationService) StopPurge(chatID int64, userID int64) bool {
	ms.mutex.Lock()
	defer ms.mutex.Unlock()

	if chatPurges, ok := ms.activePurges[chatID]; ok {
		if cancelFunc, ok := chatPurges[userID]; ok {
			log.Printf("[Moderation StopPurge] Чат %d, Пользователь %d: Отмена активной задачи purge.", chatID, userID)
			cancelFunc()               // Вызываем функцию отмены контекста
			delete(chatPurges, userID) // Удаляем запись
			if len(chatPurges) == 0 {
				delete(ms.activePurges, chatID)
			}
			return true // Успешно отменено
		}
	}
	log.Printf("[Moderation StopPurge] Чат %d, Пользователь %d: Активная задача purge не найдена.", chatID, userID)
	return false // Задача не найдена
}

// Дальнейшие методы будут добавлены здесь...
