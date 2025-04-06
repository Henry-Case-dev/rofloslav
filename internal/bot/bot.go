package bot

import (
	"context"
	"fmt"
	"log"
	"math/rand"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Henry-Case-dev/rofloslav/internal/config"
	"github.com/Henry-Case-dev/rofloslav/internal/gemini"
	"github.com/Henry-Case-dev/rofloslav/internal/storage"
	"github.com/Henry-Case-dev/rofloslav/internal/types"
	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/google/generative-ai-go/genai"
)

// Bot представляет собой основной объект бота.
type Bot struct {
	api                *tgbotapi.BotAPI
	gemini             *gemini.Client
	storage            storage.HistoryStorage // Основное хранилище (Qdrant или File)
	localHistory       storage.HistoryStorage // Дополнительное локальное хранилище для саммари/контекста
	config             *config.Config
	stop               chan struct{}
	chatSettings       map[int64]*ChatSettings
	settingsMutex      sync.RWMutex
	lastSummaryRequest map[int64]time.Time
	summaryMutex       sync.Mutex
	// Добавляем поле для хранения времени последнего прямого ответа для каждого пользователя в каждом чате
	directReplyTimestamps map[int64]map[int64][]time.Time // map[chatID][userID][]timestamps
	directReplyMutex      sync.Mutex
	botID                 int64
	responseTimeout       time.Duration // Таймаут для ответов Gemini
}

// ChatSettings содержит специфичные для чата настройки.
type ChatSettings struct {
	Active bool
	// Добавить другие настройки по мере необходимости (например, язык, персона)
}

// NewBot создает и инициализирует нового бота.
func NewBot(cfg *config.Config, geminiClient *gemini.Client, primaryStorage storage.HistoryStorage, localHistoryStorage storage.HistoryStorage) (*Bot, error) {
	tgAPI, err := tgbotapi.NewBotAPI(cfg.TelegramToken)
	if err != nil {
		return nil, fmt.Errorf("ошибка инициализации Telegram Bot API: %w", err)
	}

	tgAPI.Debug = cfg.Debug
	log.Printf("Авторизован как %s", tgAPI.Self.UserName)

	b := &Bot{
		api:                   tgAPI,
		gemini:                geminiClient,
		storage:               primaryStorage,
		localHistory:          localHistoryStorage,
		config:                cfg,
		stop:                  make(chan struct{}),
		chatSettings:          make(map[int64]*ChatSettings),
		settingsMutex:         sync.RWMutex{},
		lastSummaryRequest:    make(map[int64]time.Time),
		summaryMutex:          sync.Mutex{},
		directReplyTimestamps: make(map[int64]map[int64][]time.Time),
		directReplyMutex:      sync.Mutex{},
		botID:                 tgAPI.Self.ID,
		responseTimeout:       time.Duration(cfg.ResponseTimeoutSec) * time.Second,
	}

	// Загрузка существующих настроек чатов (если есть)
	// b.loadChatSettings() // TODO: Implement loading if needed

	// Запуск планировщиков
	// go b.autoSummarizeScheduler()
	// go b.cleanupScheduler()

	return b, nil
}

// Run запускает основного цикла обработки сообщений бота.
func (b *Bot) Run() {
	log.Println("Запуск бота...")
	u := tgbotapi.NewUpdate(0)
	u.Timeout = 60

	updates := b.api.GetUpdatesChan(u)

	for {
		select {
		case update := <-updates:
			b.handleUpdate(update)
		case <-b.stop:
			log.Println("Остановка бота...")
			return
		}
	}
}

// Stop останавливает работу бота.
func (b *Bot) Stop() {
	close(b.stop)
}

// handleUpdate обрабатывает входящие обновления от Telegram.
func (b *Bot) handleUpdate(update tgbotapi.Update) {
	// Обработка только сообщений (обычных или отредактированных)
	var message *tgbotapi.Message
	if update.Message != nil {
		message = update.Message
	} else if update.EditedMessage != nil {
		message = update.EditedMessage
		log.Printf("Получено отредактированное сообщение %d в чате %d", message.MessageID, message.Chat.ID)
		// TODO: Решить, как обрабатывать отредактированные сообщения (обновлять в хранилище?)
		// Пока просто логируем и обрабатываем как новое для простоты
	} else {
		// Игнорируем другие типы обновлений (callback query, inline query и т.д.)
		return
	}

	// Игнорируем сообщения без текста или медиа с подписью
	if message.Text == "" && message.Caption == "" {
		return
	}

	chatID := message.Chat.ID
	userID := message.From.ID

	// Логируем основную информацию о сообщении
	log.Printf("[%d] %s (%d): %s", chatID, message.From.UserName, userID, truncateString(message.Text, 50))

	// --- Сохранение сообщения ---
	go func(msgToSave *tgbotapi.Message) {
		if msgToSave == nil {
			return
		}
		b.storage.AddMessage(msgToSave.Chat.ID, msgToSave)
		log.Printf("[DEBUG] Сообщение %d от %d сохранено в основное хранилище для чата %d.", msgToSave.MessageID, msgToSave.From.ID, msgToSave.Chat.ID)

		if b.localHistory != b.storage {
			b.localHistory.AddMessage(msgToSave.Chat.ID, msgToSave)
			log.Printf("[DEBUG] Сообщение %d от %d сохранено в локальное хранилище для чата %d.", msgToSave.MessageID, msgToSave.From.ID, msgToSave.Chat.ID)
		}
	}(message)

	// --- Обработка команд ---
	if message.IsCommand() {
		b.handleCommand(message)
		return
	}

	// --- Обработка упоминаний и ответов боту ---
	mentioned := false
	if message.Entities != nil {
		for _, entity := range message.Entities {
			if entity.Type == "mention" {
				mentionText := message.Text[entity.Offset : entity.Offset+entity.Length]
				if mentionText == "@"+b.api.Self.UserName {
					mentioned = true
					break
				}
			} else if entity.Type == "text_mention" && entity.User != nil && entity.User.ID == b.botID {
				mentioned = true
				break
			}
		}
	}
	repliedToBot := message.ReplyToMessage != nil && message.ReplyToMessage.From != nil && message.ReplyToMessage.From.ID == b.botID

	if mentioned || repliedToBot {
		b.handleDirectReply(message) // Обрабатываем как прямое обращение
		return
	}

	// --- Обработка обычных сообщений в активных чатах ---
	settings := b.getChatSettings(chatID)
	if settings.Active {
		// Решаем, нужно ли отвечать (например, случайным образом или по другим условиям)
		if shouldReply(message, b.config) {
			b.sendAIResponse(message) // Отправляем ответ с использованием контекста
		}
	}
}

// handleCommand обрабатывает команды, адресованные боту.
func (b *Bot) handleCommand(message *tgbotapi.Message) {
	chatID := message.Chat.ID

	switch message.Command() {
	case "start", "help":
		helpMsg := b.config.HelpMessage
		if helpMsg == "" {
			helpMsg = "Привет! Я бот для чата. Команды: /activate, /deactivate, /status, /summarize, /srach [запрос]"
		}
		b.sendReply(chatID, helpMsg)
	case "activate":
		b.setChatActive(chatID, true)
		b.sendReply(chatID, "Бот активирован для этого чата.")
	case "deactivate":
		b.setChatActive(chatID, false)
		b.sendReply(chatID, "Бот деактивирован для этого чата.")
	case "status":
		settings := b.getChatSettings(chatID)
		status := "неактивен"
		if settings.Active {
			status = "активен"
		}
		b.sendReply(chatID, fmt.Sprintf("Статус бота в этом чате: %s", status))
	case "summarize":
		b.handleSummarizeCommand(message)
	case "srach": // Пример команды для поиска
		b.handleSrachCommand(message)
	default:
		b.sendReply(chatID, "Неизвестная команда. Используйте /help для списка команд.")
	}
}

// sendAIResponse генерирует и отправляет ответ AI на основе контекста чата.
func (b *Bot) sendAIResponse(message *tgbotapi.Message) {
	chatID := message.Chat.ID
	log.Printf("Генерация AI ответа для сообщения %d в чате %d...", message.MessageID, chatID)

	// Получаем все сообщения из основного хранилища
	rawRecentMessages := b.storage.GetMessages(chatID)
	// Конвертируем в types.Message
	recentMessages := convertTgMessagesToTypesMessages(rawRecentMessages)
	if recentMessages == nil {
		recentMessages = []types.Message{}
	}
	log.Printf("Получено %d сообщений из основного хранилища для чата %d", len(recentMessages), chatID)

	// Получаем саммари (если есть) из локального хранилища
	rawSummaryMessages := b.localHistory.GetMessages(chatID)
	summaryText := ""
	if len(rawSummaryMessages) > 0 {
		lastLocalMsg := rawSummaryMessages[len(rawSummaryMessages)-1]
		summaryCandidate := convertTgBotMessageToTypesMessage(lastLocalMsg)
		if summaryCandidate != nil && (summaryCandidate.Role == "summary" || len(rawSummaryMessages) == 1) {
			summaryText = summaryCandidate.Text
			log.Printf("Используем саммари для чата %d: %s...", chatID, truncateString(summaryText, 50))
		} else {
			log.Printf("Последнее сообщение (%d) в локальном хранилище чата %d не является саммари (Роль: %s).", lastLocalMsg.MessageID, chatID, summaryCandidate.Role)
		}
	}

	// Формируем промпт для Gemini, включая саммари, если оно есть
	prompt := b.config.BaseSystemPrompt
	if prompt == "" {
		prompt = "Ты - участник группового чата."
	} // Дефолтный промпт
	if summaryText != "" {
		prompt += "\n\nВот краткое содержание предыдущего диалога (саммари):\n" + summaryText
	}

	// Объединяем историю сообщений
	contextMessages := recentMessages

	// Добавляем текущее сообщение в конец (конвертируем его)
	currentMessageConverted := convertTgBotMessageToTypesMessage(message)
	if currentMessageConverted != nil {
		contextMessages = append(contextMessages, *currentMessageConverted)
	}

	// Сортируем по времени, чтобы гарантировать порядок
	sort.SliceStable(contextMessages, func(i, j int) bool {
		return contextMessages[i].Timestamp < contextMessages[j].Timestamp
	})

	// Оставляем только последние N сообщений для контекста, если их больше
	if len(contextMessages) > b.config.MaxMessagesForContext {
		contextMessages = contextMessages[len(contextMessages)-b.config.MaxMessagesForContext:]
		log.Printf("Контекст для чата %d обрезан до %d сообщений", chatID, b.config.MaxMessagesForContext)
	}

	log.Printf("Отправка AI запроса для чата %d с %d сообщениями в контексте...", chatID, len(contextMessages))

	// Отправляем запрос в Gemini
	geminiHistory := convertMessagesToGenaiContent(contextMessages)
	lastMessageText := "" // Последнее сообщение уже включено в contextMessages
	ctxResp, cancelResp := context.WithTimeout(context.Background(), b.responseTimeout)
	defer cancelResp()
	var response string
	var err error
	response, err = b.gemini.GenerateContent(ctxResp, prompt, geminiHistory, lastMessageText, b.config.DefaultGenerationSettings)
	if err != nil {
		log.Printf("[ERROR] sendAIResponse: Ошибка генерации ответа от Gemini для чата %d: %v", chatID, err)
		return
	}

	// Отправляем ответ пользователю
	b.sendReply(chatID, response)
}

// handleDirectReply обрабатывает прямое упоминание или ответ боту
func (b *Bot) handleDirectReply(message *tgbotapi.Message) {
	chatID := message.Chat.ID
	userID := message.From.ID
	log.Printf("Получено прямое обращение от пользователя %d (%s) в чате %d: %s", userID, message.From.UserName, chatID, message.Text)

	// Получаем настройки чата
	settings := b.getChatSettings(chatID)
	if !settings.Active {
		log.Printf("Бот неактивен в чате %d, игнорируем прямое обращение.", chatID)
		return
	}

	// --- Проверка лимитов ---
	now := time.Now()
	b.directReplyMutex.Lock()
	if _, ok := b.directReplyTimestamps[chatID]; !ok {
		b.directReplyTimestamps[chatID] = make(map[int64][]time.Time)
	}
	userTimestamps := b.directReplyTimestamps[chatID][userID]

	validTimestamps := []time.Time{}
	windowStart := now.Add(-b.config.DirectReplyWindow)
	for _, ts := range userTimestamps {
		if ts.After(windowStart) {
			validTimestamps = append(validTimestamps, ts)
		}
	}

	if len(validTimestamps) >= b.config.DirectReplyLimitCount {
		log.Printf("Превышен лимит прямых обращений для пользователя %d в чате %d. Игнорируем.", userID, chatID)
		if len(validTimestamps) == b.config.DirectReplyLimitCount {
			warningMsg := b.config.DirectReplyLimitPrompt
			if warningMsg == "" {
				warningMsg = "Вы слишком часто обращаетесь ко мне напрямую. Пожалуйста, подождите немного."
			}
			b.sendReplyToUser(chatID, message.MessageID, warningMsg)
		}
		b.directReplyMutex.Unlock()
		return
	}

	validTimestamps = append(validTimestamps, now)
	b.directReplyTimestamps[chatID][userID] = validTimestamps
	b.directReplyMutex.Unlock()

	// --- Логика получения контекста ---

	// Получаем все последние сообщения из основного хранилища
	rawRecentMessages := b.storage.GetMessages(chatID)
	recentMessages := convertTgMessagesToTypesMessages(rawRecentMessages)
	if recentMessages == nil {
		recentMessages = []types.Message{}
	}

	// Получаем саммари (если есть) из локального хранилища
	rawSummaryMessages := b.localHistory.GetMessages(chatID)
	summaryText := ""
	if len(rawSummaryMessages) > 0 {
		lastLocalMsg := rawSummaryMessages[len(rawSummaryMessages)-1]
		summaryCandidate := convertTgBotMessageToTypesMessage(lastLocalMsg)
		if summaryCandidate != nil && (summaryCandidate.Role == "summary" || len(rawSummaryMessages) == 1) {
			summaryText = summaryCandidate.Text
		}
	}

	// Ищем релевантные сообщения
	relevantMessages := []types.Message{}
	// Вызываем FindRelevantMessages напрямую из интерфейса HistoryStorage
	foundMessages, searchErr := b.storage.FindRelevantMessages(chatID, message.Text, b.config.RelevantMessagesCount)
	if searchErr != nil {
		// Обрабатываем ошибку поиска (но не прерываем выполнение, контекст все равно соберем)
		log.Printf("Ошибка поиска релевантных сообщений для прямого ответа в чате %d: %v", chatID, searchErr)
	} else {
		relevantMessages = foundMessages
		log.Printf("Найдено %d релевантных сообщений для прямого ответа в чате %d", len(relevantMessages), chatID)
	}

	// --- Формирование контекста и промпта ---
	prompt := b.config.DirectReplyPrompt
	if prompt == "" {
		prompt = "Тебе адресовали сообщение:"
	}
	if summaryText != "" {
		prompt += "\n\nВот краткое содержание предыдущего диалога (саммари):\n" + summaryText
	}

	// Объединяем сообщения: релевантные + недавние + текущее
	contextMessages := combineAndDeduplicateMessages(relevantMessages, recentMessages)

	// Добавляем текущее сообщение
	currentMessageConverted := convertTgBotMessageToTypesMessage(message)
	if currentMessageConverted != nil {
		found := false
		for _, msg := range contextMessages {
			if msg.ID == currentMessageConverted.ID && msg.ChatID == currentMessageConverted.ChatID {
				found = true
				break
			}
		}
		if !found {
			contextMessages = append(contextMessages, *currentMessageConverted)
		}
	}

	// Сортируем по времени
	sort.SliceStable(contextMessages, func(i, j int) bool {
		return contextMessages[i].Timestamp < contextMessages[j].Timestamp
	})

	// Ограничиваем общее количество сообщений для контекста
	if len(contextMessages) > b.config.MaxMessagesForContext {
		contextMessages = contextMessages[len(contextMessages)-b.config.MaxMessagesForContext:]
		log.Printf("Контекст для прямого ответа в чате %d обрезан до %d сообщений", chatID, b.config.MaxMessagesForContext)
	}

	log.Printf("Отправка AI запроса для прямого ответа в чате %d с %d сообщениями в контексте...", chatID, len(contextMessages))

	// --- Отправка запроса в Gemini ---
	geminiHistory := convertMessagesToGenaiContent(contextMessages)
	lastMessageText := ""
	ctxResp, cancelResp := context.WithTimeout(context.Background(), b.responseTimeout)
	defer cancelResp()
	var response string
	var err error
	response, err = b.gemini.GenerateContent(ctxResp, prompt, geminiHistory, lastMessageText, b.config.DefaultGenerationSettings)
	if err != nil {
		log.Printf("Ошибка генерации прямого ответа AI для чата %d: %v", chatID, err)
		return
	}

	// Отправляем ответ пользователю (как реплай на его сообщение)
	b.sendReplyToUser(chatID, message.MessageID, response)
}

// handleSummarizeCommand обрабатывает команду /summarize
func (b *Bot) handleSummarizeCommand(message *tgbotapi.Message) {
	chatID := message.Chat.ID
	log.Printf("Получена команда /summarize в чате %d от пользователя %d", chatID, message.From.ID)

	// --- Проверка кулдауна ---
	b.summaryMutex.Lock()
	lastReq, ok := b.lastSummaryRequest[chatID]
	now := time.Now()
	if ok && now.Sub(lastReq) < b.config.SummaryCooldown {
		b.summaryMutex.Unlock()
		log.Printf("Кулдаун команды /summarize для чата %d", chatID)
		if now.Sub(lastReq) < b.config.SummaryCooldown-time.Second*5 {
			prefix := b.config.SummaryRateLimitStaticPrefix
			suffix := b.config.SummaryRateLimitStaticSuffix
			insult := b.config.SummaryRateLimitInsultPrompt
			if insult == "" {
				insult = fmt.Sprintf("Команду /summarize можно использовать раз в %v. Пожалуйста, подождите.", b.config.SummaryCooldown)
			}
			b.sendReply(chatID, prefix+insult+suffix)
		}
		return
	}
	b.lastSummaryRequest[chatID] = now
	b.summaryMutex.Unlock()

	// Получаем сообщения для саммаризации из основного хранилища
	rawMessagesToSummarize := b.storage.GetMessages(chatID)
	messagesToSummarize := convertTgMessagesToTypesMessages(rawMessagesToSummarize)
	if messagesToSummarize == nil {
		messagesToSummarize = []types.Message{}
	}

	// Применяем лимит MaxMessagesForSummary ПОСЛЕ получения
	if len(messagesToSummarize) > b.config.MaxMessagesForSummary {
		messagesToSummarize = messagesToSummarize[len(messagesToSummarize)-b.config.MaxMessagesForSummary:]
		log.Printf("Сообщения для саммаризации в чате %d обрезаны до %d", chatID, b.config.MaxMessagesForSummary)
	}

	if len(messagesToSummarize) == 0 {
		log.Printf("Нет сообщений для саммаризации в чате %d", chatID)
		b.sendReply(chatID, "Не удалось получить сообщения для создания саммари.")
		return
	}

	log.Printf("Саммаризация %d сообщений для чата %d...", len(messagesToSummarize), chatID)

	// Формируем контекст для Gemini (только сообщения)
	contextMessages := messagesToSummarize

	// Сортируем по времени
	sort.SliceStable(contextMessages, func(i, j int) bool {
		return contextMessages[i].Timestamp < contextMessages[j].Timestamp
	})

	// Отправляем запрос в Gemini для саммаризации
	geminiHistory := convertMessagesToGenaiContent(contextMessages)
	lastMessageText := ""
	prompt := b.config.SummaryPrompt
	if prompt == "" {
		prompt = "Подведи итог этого диалога кратко:"
	}
	ctxSummary, cancelSummary := context.WithTimeout(context.Background(), b.responseTimeout)
	defer cancelSummary()
	var response string
	var err error
	response, err = b.gemini.GenerateContent(ctxSummary, prompt, geminiHistory, lastMessageText, b.config.DefaultGenerationSettings)
	if err != nil {
		log.Printf("[Summary ERROR] Чат %d: Ошибка генерации саммари от Gemini: %v", chatID, err)
		b.sendReply(chatID, "Не удалось сгенерировать саммари. Попробуйте позже.")
		return
	}

	log.Printf("Саммари для чата %d сгенерировано: %s...", chatID, truncateString(response, 100))

	// Сохраняем саммари в локальное хранилище
	summaryInternalMessage := types.Message{
		ID:        0,
		ChatID:    chatID,
		Text:      response,
		Timestamp: int(time.Now().Unix()),
		Role:      "summary",
		UserID:    b.botID,
		UserName:  b.api.Self.UserName,
		FirstName: b.api.Self.FirstName,
		IsBot:     true,
	}
	// Конвертируем types.Message обратно в *tgbotapi.Message для AddMessage
	summaryTgMessage := &tgbotapi.Message{
		MessageID: int(summaryInternalMessage.ID),
		Chat:      &tgbotapi.Chat{ID: chatID},
		From: &tgbotapi.User{
			ID:        summaryInternalMessage.UserID,
			IsBot:     summaryInternalMessage.IsBot,
			FirstName: summaryInternalMessage.FirstName,
			UserName:  summaryInternalMessage.UserName,
		},
		Date: summaryInternalMessage.Timestamp,
		Text: summaryInternalMessage.Text,
	}

	// Вызываем AddMessage для локального хранилища без ожидания ошибки
	b.localHistory.AddMessage(chatID, summaryTgMessage)
	log.Printf("Саммари для чата %d сохранено в локальное хранилище.", chatID)
	b.sendReply(chatID, "Саммари обновлено!\n\n"+response)
}

// handleSrachCommand - пример обработчика для /srach (поиска)
func (b *Bot) handleSrachCommand(message *tgbotapi.Message) {
	chatID := message.Chat.ID
	userID := message.From.ID

	// Получаем текст запроса после команды
	query := message.CommandArguments()
	if query == "" {
		b.sendReply(chatID, "Пожалуйста, укажите текст для поиска после команды /srach.")
		return
	}

	log.Printf("Получена команда /srach от %d в чате %d с запросом: %s", userID, chatID, query)

	// Ищем релевантные сообщения в основном хранилище, вызывая метод напрямую
	foundMessages, searchErr := b.storage.FindRelevantMessages(chatID, query, b.config.SrachResultCount)
	if searchErr != nil {
		log.Printf("Ошибка поиска /srach в чате %d: %v", chatID, searchErr)
		b.sendReply(chatID, "Произошла ошибка при поиске сообщений.")
		return
	}

	if len(foundMessages) == 0 {
		b.sendReply(chatID, "По вашему запросу ничего не найдено.")
		return
	}

	// Формируем ответ с найденными сообщениями
	var responseText strings.Builder
	responseText.WriteString(fmt.Sprintf("Найдено %d сообщений по запросу '%s':\n\n", len(foundMessages), query))

	for i, msg := range foundMessages {
		author := fmt.Sprintf("User %d", msg.UserID)
		if msg.UserName != "" {
			author = "@" + msg.UserName
		} else if msg.FirstName != "" {
			author = msg.FirstName
		}
		if msg.IsBot {
			author += " (Бот)"
		}

		msgTime := time.Unix(int64(msg.Timestamp), 0)
		timeStr := msgTime.Format("02.01.2006 15:04")

		responseText.WriteString(fmt.Sprintf("%d. [%s] %s: %s\n", i+1, timeStr, author, truncateString(msg.Text, 150)))
	}

	// Отправляем результат поиска
	b.sendReply(chatID, responseText.String())
}

// --- Вспомогательные функции бота ---

// sendReply отправляет сообщение в указанный чат.
func (b *Bot) sendReply(chatID int64, text string) {
	if text == "" {
		log.Printf("[WARNING] Попытка отправить пустое сообщение в чат %d", chatID)
		return
	}
	msg := tgbotapi.NewMessage(chatID, text)
	_, err := b.api.Send(msg)
	if err != nil {
		log.Printf("[ERROR] Не удалось отправить сообщение в чат %d: %v", chatID, err)
	}
}

// sendReplyToUser отправляет сообщение в указанный чат как ответ на конкретное сообщение.
func (b *Bot) sendReplyToUser(chatID int64, replyToMessageID int, text string) {
	if text == "" {
		log.Printf("[WARNING] Попытка отправить пустое сообщение в чат %d (в ответ на %d)", chatID, replyToMessageID)
		return
	}
	msg := tgbotapi.NewMessage(chatID, text)
	msg.ReplyToMessageID = replyToMessageID
	_, err := b.api.Send(msg)
	if err != nil {
		log.Printf("[ERROR] Не удалось отправить ответное сообщение в чат %d (на %d): %v", chatID, replyToMessageID, err)
	}
}

// getChatSettings возвращает настройки для чата, создавая их при необходимости.
func (b *Bot) getChatSettings(chatID int64) *ChatSettings {
	b.settingsMutex.RLock()
	settings, exists := b.chatSettings[chatID]
	b.settingsMutex.RUnlock()

	if !exists {
		b.settingsMutex.Lock()
		settings, exists = b.chatSettings[chatID]
		if !exists {
			log.Printf("Создание настроек по умолчанию для чата %d", chatID)
			settings = &ChatSettings{
				Active: b.config.ActivateNewChats,
			}
			b.chatSettings[chatID] = settings
		}
		b.settingsMutex.Unlock()
	}
	return settings
}

// setChatActive устанавливает статус активности чата.
func (b *Bot) setChatActive(chatID int64, active bool) {
	settings := b.getChatSettings(chatID)
	b.settingsMutex.Lock()
	settings.Active = active
	b.settingsMutex.Unlock()
	status := "активирован"
	if !active {
		status = "деактивирован"
	}
	log.Printf("Бот %s для чата %d", status, chatID)
}

// shouldReply определяет, должен ли бот отвечать на данное сообщение.
func shouldReply(message *tgbotapi.Message, cfg *config.Config) bool {
	if cfg.RandomReplyEnabled && cfg.ReplyChance > 0 {
		if rand.Float32() < cfg.ReplyChance {
			log.Printf("Случайный ответ активирован для сообщения %d в чате %d", message.MessageID, message.Chat.ID)
			return true
		}
	}
	return false
}

// --- Вспомогательные функции ---

// truncateString обрезает строку до указанной длины, добавляя "..." если она была обрезана.
func truncateString(s string, maxLength int) string {
	if len(s) <= maxLength {
		return s
	}
	runes := []rune(s)
	if len(runes) <= maxLength {
		return s
	}
	if maxLength <= 3 {
		return string(runes[:maxLength])
	}
	return string(runes[:maxLength-3]) + "..."
}

// combineAndDeduplicateMessages объединяет срезы сообщений, удаляя дубликаты по ID и ChatID.
func combineAndDeduplicateMessages(slices ...[]types.Message) []types.Message {
	seen := make(map[string]struct{})
	result := []types.Message{}

	for _, slice := range slices {
		for _, msg := range slice {
			key := fmt.Sprintf("%d:%d", msg.ChatID, msg.ID)
			if _, exists := seen[key]; !exists {
				seen[key] = struct{}{}
				result = append(result, msg)
			}
		}
	}
	return result
}

// --- Вспомогательные функции конвертации ---

// convertTgBotMessageToTypesMessage конвертирует *tgbotapi.Message в types.Message
func convertTgBotMessageToTypesMessage(msg *tgbotapi.Message) *types.Message {
	if msg == nil {
		return nil
	}
	role := "user"
	if msg.From != nil && msg.From.IsBot {
		// TODO: Определять роль 'model' для сообщений нашего бота
	}

	text := msg.Text
	if text == "" && msg.Caption != "" {
		text = msg.Caption
	}

	converted := &types.Message{
		ID:        int64(msg.MessageID),
		ChatID:    msg.Chat.ID,
		Text:      text,
		Timestamp: msg.Date,
		Role:      role,
	}
	if msg.From != nil {
		converted.UserID = msg.From.ID
		converted.UserName = msg.From.UserName
		converted.FirstName = msg.From.FirstName
		converted.IsBot = msg.From.IsBot
	}
	if msg.ReplyToMessage != nil {
		converted.ReplyToMsgID = msg.ReplyToMessage.MessageID
	}
	if len(msg.Entities) > 0 {
		converted.Entities = convertTgEntitiesToTypesEntities(msg.Entities)
	} else if len(msg.CaptionEntities) > 0 {
		converted.Entities = convertTgEntitiesToTypesEntities(msg.CaptionEntities)
	}
	return converted
}

// convertTgMessagesToTypesMessages конвертирует срез *tgbotapi.Message в срез types.Message
func convertTgMessagesToTypesMessages(tgMessages []*tgbotapi.Message) []types.Message {
	if tgMessages == nil {
		return nil
	}
	typeMessages := make([]types.Message, 0, len(tgMessages))
	for _, tgMsg := range tgMessages {
		converted := convertTgBotMessageToTypesMessage(tgMsg)
		if converted != nil {
			typeMessages = append(typeMessages, *converted)
		}
	}
	return typeMessages
}

// convertMessagesToGenaiContent конвертирует срез types.Message в формат genai.Content для Gemini API.
func convertMessagesToGenaiContent(messages []types.Message) []*genai.Content {
	contents := make([]*genai.Content, 0, len(messages))
	var lastRole string
	for _, msg := range messages {
		role := "user"
		if msg.IsBot {
			// TODO: Более точно определять роль 'model' для сообщений нашего бота
			role = "model"
		}
		if msg.Role == "model" {
			role = "model"
		}

		if len(contents) > 0 && role == lastRole {
			lastContent := contents[len(contents)-1]
			if len(lastContent.Parts) > 0 {
				if textPart, ok := lastContent.Parts[len(lastContent.Parts)-1].(genai.Text); ok {
					lastContent.Parts[len(lastContent.Parts)-1] = genai.Text(string(textPart) + "\n" + msg.Text)
				} else {
					lastContent.Parts = append(lastContent.Parts, genai.Text(msg.Text))
				}
			} else {
				lastContent.Parts = append(lastContent.Parts, genai.Text(msg.Text))
			}
			continue
		}

		contents = append(contents, &genai.Content{
			Parts: []genai.Part{genai.Text(msg.Text)},
			Role:  role,
		})
		lastRole = role
	}

	if len(contents) > 0 && contents[0].Role == "model" {
		contents = append([]*genai.Content{{Role: "user", Parts: []genai.Part{genai.Text("")}}}, contents...)
		log.Println("[WARN] Добавлено пустое сообщение user в начало истории для Gemini.")
	}

	return contents
}

// convertTgEntitiesToTypesEntities конвертирует []tgbotapi.MessageEntity в []types.MessageEntity
func convertTgEntitiesToTypesEntities(tgEntities []tgbotapi.MessageEntity) []types.MessageEntity {
	if tgEntities == nil {
		return nil
	}
	typeEntities := make([]types.MessageEntity, len(tgEntities))
	for i, e := range tgEntities {
		typeEntities[i] = types.MessageEntity{
			Type:     string(e.Type),
			Offset:   e.Offset,
			Length:   e.Length,
			URL:      e.URL,
			User:     convertTgUserToTypesUser(e.User),
			Language: e.Language,
		}
	}
	return typeEntities
}

// convertTgUserToTypesUser конвертирует *tgbotapi.User в *types.User
func convertTgUserToTypesUser(tgUser *tgbotapi.User) *types.User {
	if tgUser == nil {
		return nil
	}
	return &types.User{
		ID:           tgUser.ID,
		IsBot:        tgUser.IsBot,
		FirstName:    tgUser.FirstName,
		LastName:     tgUser.LastName,
		UserName:     tgUser.UserName,
		LanguageCode: tgUser.LanguageCode,
	}
}

// --- Конец вспомогательных функций ---
