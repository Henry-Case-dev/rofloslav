package bot

import (
	"fmt"
	"log"
	"math/rand"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Henry-Case-dev/rofloslav/internal/config"
	"github.com/Henry-Case-dev/rofloslav/internal/gemini"
	"github.com/Henry-Case-dev/rofloslav/internal/storage"
	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// Bot представляет Telegram бота
type Bot struct {
	api                *tgbotapi.BotAPI
	gemini             *gemini.Client
	storage            storage.HistoryStorage
	config             *config.Config
	chatSettings       map[int64]*ChatSettings
	settingsMutex      sync.RWMutex
	stop               chan struct{}
	summaryMutex       sync.RWMutex
	lastSummaryRequest map[int64]time.Time
	autoSummaryTicker  *time.Ticker

	// Поле для отслеживания прямых обращений: chatID -> userID -> []timestamp
	directReplyTimestamps map[int64]map[int64][]time.Time
	directReplyMutex      sync.Mutex // Мьютекс для защиты directReplyTimestamps

	// ID самого бота (для проверки Reply)
	botID int64
}

// ChatSettings содержит настройки для каждого чата
type ChatSettings struct {
	Active               bool
	CustomPrompt         string
	MinMessages          int
	MaxMessages          int
	MessageCount         int
	LastMessageID        int
	DailyTakeTime        int
	PendingSetting       string
	SummaryIntervalHours int
	LastAutoSummaryTime  time.Time

	// New fields for Srach Analysis
	SrachAnalysisEnabled bool      `json:"srach_analysis_enabled"`  // Включен ли анализ срачей
	SrachState           string    `json:"srach_state"`             // Текущее состояние срача ("none", "detected", "analyzing")
	SrachStartTime       time.Time `json:"srach_start_time"`        // Время начала обнаруженного срача
	SrachMessages        []string  `json:"srach_messages"`          // Сообщения, собранные во время срача для анализа
	LastSrachTriggerTime time.Time `json:"last_srach_trigger_time"` // Время последнего триггерного сообщения для таймера завершения
	SrachLlmCheckCounter int       `json:"srach_llm_check_counter"` // Счетчик сообщений для LLM проверки срача
}

// New создает новый экземпляр бота
func New(cfg *config.Config) (*Bot, error) {
	// Очищаем токен от пробелов по краям
	trimmedToken := strings.TrimSpace(cfg.TelegramToken)

	// ВРЕМЕННЫЙ ЛОГ ДЛЯ ОТЛАДКИ ТОКЕНА - УДАЛИТЬ ПОСЛЕ РЕШЕНИЯ ПРОБЛЕМЫ!
	log.Printf("!!! TOKEN DEBUG !!! Используется токен (очищенный): '%s...%s' (Длина: %d). Перед очисткой: %d", trimmedToken[:min(10, len(trimmedToken))], trimmedToken[max(0, len(trimmedToken)-5):], len(trimmedToken), len(cfg.TelegramToken))
	// Принудительно сбрасываем буфер логов, чтобы сообщение точно появилось
	if f, ok := log.Writer().(*os.File); ok {
		f.Sync()
	}

	api, err := tgbotapi.NewBotAPI(trimmedToken) // Используем очищенный токен
	if err != nil {
		// Добавляем лог ошибки *перед* возвратом
		log.Printf("!!! API Init Error !!! Ошибка при вызове tgbotapi.NewBotAPI: %v", err)
		if f, ok := log.Writer().(*os.File); ok {
			f.Sync()
		}
		return nil, fmt.Errorf("ошибка инициализации Telegram API: %w", err)
	}

	// Получаем ID бота
	botUser, err := api.GetMe()
	if err != nil {
		return nil, fmt.Errorf("ошибка получения информации о боте: %w", err)
	}
	log.Printf("Бот запущен как: %s (ID: %d)", botUser.UserName, botUser.ID)

	// Используем новый конструктор Gemini клиента
	geminiClient, err := gemini.New(cfg.GeminiAPIKey, cfg.GeminiModelName, cfg.Debug)
	if err != nil {
		return nil, fmt.Errorf("ошибка инициализации Gemini Client: %w", err)
	}

	// Используем фабрику для создания хранилища (S3 или Local)
	historyStorage, err := storage.NewHistoryStorage(cfg)
	if err != nil {
		// Фабрика уже логирует ошибку, но мы можем добавить еще или паниковать
		return nil, fmt.Errorf("критическая ошибка инициализации хранилища истории: %w", err)
	}
	log.Println("Хранилище истории успешно инициализировано.") // Подтверждаем успех

	bot := &Bot{
		api:                   api,
		gemini:                geminiClient,
		storage:               historyStorage,
		config:                cfg,
		chatSettings:          make(map[int64]*ChatSettings),
		settingsMutex:         sync.RWMutex{},
		stop:                  make(chan struct{}),
		summaryMutex:          sync.RWMutex{},
		lastSummaryRequest:    make(map[int64]time.Time),
		directReplyTimestamps: make(map[int64]map[int64][]time.Time),
		directReplyMutex:      sync.Mutex{},
		botID:                 botUser.ID,
	}

	// Загрузка истории для всех чатов при старте (опционально, может занять время)
	// S3Storage уже пытается загрузить при инициализации.
	// Для LocalStorage этот вызов может быть нужен, если хотим предзагрузить.
	// bot.loadAllChatHistoriesOnStart() // Раскомментируйте, если нужно

	// Запуск планировщика для ежедневного тейка
	go bot.scheduleDailyTake(cfg.DailyTakeTime, cfg.TimeZone)

	// Запуск планировщика для автоматического саммари
	go bot.schedulePeriodicSummary()

	log.Println("Бот успешно инициализирован.") // Обновляем лог
	return bot, nil
}

// Start запускает обработку сообщений
func (b *Bot) Start() error {
	u := tgbotapi.NewUpdate(0)
	u.Timeout = 60

	updates := b.api.GetUpdatesChan(u)

	for {
		select {
		case update := <-updates:
			b.handleUpdate(update)
		case <-b.stop:
			return nil
		}
	}
}

// Stop останавливает бота
func (b *Bot) Stop() {
	log.Println("Остановка бота...")
	close(b.stop) // Сигнал для остановки горутин

	// Закрываем клиент Gemini
	if err := b.gemini.Close(); err != nil {
		log.Printf("Ошибка при закрытии клиента Gemini: %v", err)
	}

	// Сохраняем все истории перед выходом (ВОССТАНОВЛЕНО)
	log.Println("Сохранение истории всех чатов перед остановкой...")
	b.saveAllChatHistories() // Используем метод, который вызывает SaveAllChatHistories интерфейса

	log.Println("Бот остановлен.")
}

// handleUpdate обрабатывает входящие обновления
func (b *Bot) handleUpdate(update tgbotapi.Update) {
	if update.Message == nil { // Игнорируем не-сообщения
		return
	}

	message := update.Message
	chatID := message.Chat.ID
	userID := message.From.ID // ID пользователя, отправившего сообщение
	text := message.Text

	// --- Логика определения прямого обращения ---
	isDirectReply := false
	if message.ReplyToMessage != nil && message.ReplyToMessage.From.ID == b.botID {
		isDirectReply = true // Это ответ на сообщение бота
		log.Printf("[DEBUG] Чат %d: Обнаружен прямой ответ (Reply) на сообщение бота от userID %d", chatID, userID)
	} else if message.Entities != nil {
		for _, entity := range message.Entities {
			if entity.Type == "mention" {
				mention := text[entity.Offset : entity.Offset+entity.Length]
				// Сравниваем с username бота (можно улучшить, если username меняется)
				if mention == "@"+b.api.Self.UserName {
					isDirectReply = true // Это упоминание бота
					log.Printf("[DEBUG] Чат %d: Обнаружено упоминание (Mention) бота от userID %d", chatID, userID)
					break
				}
			}
		}
	}
	// --- Конец определения прямого обращения ---

	// Добавляем сообщение в хранилище ДО генерации ответа
	b.storage.AddMessage(chatID, message)

	// --- ЗАГРУЗКА ИСТОРИИ ДЛЯ НОВОГО ЧАТА (если еще нет в кеше) ---
	// Это нужно, чтобы при первом сообщении в "новом" для этого запуска бота чате
	// мы подгрузили его историю из S3/файла, если она там есть.
	b.storage.GetMessages(chatID) // Этот вызов для S3Storage ничего не делает, но для LocalStorage создаст запись в map, если ее нет
	// Попробуем загрузить историю, если ее нет в кеше S3Storage или для LocalStorage
	if len(b.storage.GetMessages(chatID)) == 0 { // Проверяем кеш
		_, err := b.storage.LoadChatHistory(chatID) // Загружаем и добавляем в кеш/память
		if err != nil {
			log.Printf("[handleUpdate ERROR] Чат %d: Не удалось загрузить историю: %v", chatID, err)
		}
	}
	// --- КОНЕЦ ЗАГРУЗКИ ИСТОРИИ ---

	messageTime := message.Time()

	// --- Инициализация/Загрузка настроек чата --- (Используем loadChatSettings)
	settings, err := b.loadChatSettings(chatID)
	if err != nil {
		log.Printf("handleUpdate: Ошибка загрузки/создания настроек для чата %d: %v", chatID, err)
		return // Не можем обработать без настроек
	}
	// --- Конец инициализации ---

	// --- Обработка отмены ввода --- (остается без изменений)
	b.settingsMutex.RLock()
	currentPendingSetting := settings.PendingSetting
	b.settingsMutex.RUnlock()
	if text == "/cancel" && currentPendingSetting != "" {
		b.settingsMutex.Lock()
		settings.PendingSetting = "" // Сбрасываем ожидание
		b.settingsMutex.Unlock()
		b.sendReply(chatID, "Ввод отменен.")
		b.sendSettingsKeyboard(chatID) // Показываем меню настроек снова
		return
	}
	// --- Конец обработки отмены ---

	// --- Обработка ввода ожидаемой настройки --- (остается без изменений)
	if currentPendingSetting != "" {
		isValidInput := false
		parsedValue, err := strconv.Atoi(text)

		if err != nil {
			b.sendReply(chatID, "Ошибка: Введите числовое значение или /cancel для отмены.")
		} else {
			b.settingsMutex.Lock() // Блокируем для изменения настроек
			switch currentPendingSetting {
			case "min_messages":
				if parsedValue > 0 && parsedValue <= settings.MaxMessages {
					settings.MinMessages = parsedValue
					isValidInput = true
				} else {
					b.sendReply(chatID, fmt.Sprintf("Ошибка: Минимальное значение должно быть больше 0 и не больше максимального (%d).", settings.MaxMessages))
				}
			case "max_messages":
				if parsedValue >= settings.MinMessages {
					settings.MaxMessages = parsedValue
					isValidInput = true
				} else {
					b.sendReply(chatID, fmt.Sprintf("Ошибка: Максимальное значение должно быть не меньше минимального (%d).", settings.MinMessages))
				}
			case "daily_time":
				if parsedValue >= 0 && parsedValue <= 23 {
					settings.DailyTakeTime = parsedValue
					// Перезапускаем планировщик тейка с новым временем (если он уже был запущен)
					// Простая реализация: просто логируем, сложная - требует управления горутиной
					log.Printf("Настройка времени тейка изменена на %d для чата %d. Перезапустите бота для применения ко всем чатам или реализуйте динамическое обновление.", parsedValue, chatID)
					isValidInput = true
				} else {
					b.sendReply(chatID, "Ошибка: Введите час от 0 до 23.")
				}
			case "summary_interval":
				if parsedValue >= 0 { // 0 - выключено
					settings.SummaryIntervalHours = parsedValue
					settings.LastAutoSummaryTime = time.Time{} // Сбрасываем таймер при изменении интервала
					log.Printf("Интервал авто-саммари для чата %d изменен на %d ч.", chatID, parsedValue)
					isValidInput = true
				} else {
					b.sendReply(chatID, "Ошибка: Интервал не может быть отрицательным (0 - выключить).")
				}
			}

			if isValidInput {
				settings.PendingSetting = "" // Сбрасываем ожидание после успешного ввода
				b.sendReply(chatID, "Настройка успешно обновлена!")
			}
			b.settingsMutex.Unlock() // Разблокируем после изменения

			if isValidInput {
				b.sendSettingsKeyboard(chatID) // Показываем обновленное меню
			}
		}
		return // Прекращаем дальнейшую обработку сообщения, т.к. это был ввод настройки
	}
	// --- Конец обработки ввода ---

	// --- Обработка команд (если это не был ввод настройки) --- (остается без изменений)
	if message.IsCommand() {
		b.handleCommand(message)
		return
	}
	// --- Конец обработки команд ---

	// --- Логика Анализа Срачей ---
	b.settingsMutex.RLock()
	srachEnabled := settings.SrachAnalysisEnabled
	b.settingsMutex.RUnlock()

	if srachEnabled {
		isPotentialSrachMsg := b.isPotentialSrachTrigger(message)

		b.settingsMutex.Lock()
		currentState := settings.SrachState
		lastTriggerTime := settings.LastSrachTriggerTime

		if isPotentialSrachMsg {
			if settings.SrachState == "none" {
				settings.SrachState = "detected"
				settings.SrachStartTime = messageTime
				settings.SrachMessages = []string{formatMessageForAnalysis(message)}
				settings.LastSrachTriggerTime = messageTime // Запоминаем время триггера
				settings.SrachLlmCheckCounter = 0           // Сбрасываем счетчик LLM при старте
				// Разблокируем перед отправкой сообщения
				b.settingsMutex.Unlock()
				b.sendSrachWarning(chatID) // Объявляем начало
				log.Printf("Чат %d: Обнаружен потенциальный срач.", chatID)
				goto SaveMessage // Переходим к сохранению
			} else if settings.SrachState == "detected" {
				settings.SrachMessages = append(settings.SrachMessages, formatMessageForAnalysis(message))
				settings.LastSrachTriggerTime = messageTime // Обновляем время последнего триггера
				settings.SrachLlmCheckCounter++             // Увеличиваем счетчик

				// Проверяем каждое N-е (N=3) сообщение через LLM асинхронно
				const llmCheckInterval = 3
				if settings.SrachLlmCheckCounter%llmCheckInterval == 0 {
					msgTextToCheck := message.Text // Копируем текст перед запуском горутины
					go func() {
						isConfirmed := b.confirmSrachWithLLM(chatID, msgTextToCheck)
						log.Printf("[LLM Srach Confirm] Чат %d: Сообщение ID %d. Результат LLM: %t",
							chatID, message.MessageID, isConfirmed)
						// Пока только логируем, не меняем SrachState
					}()
				}
			}
		} else if currentState == "detected" {
			// Сообщение не триггер, но срач был активен
			settings.SrachMessages = append(settings.SrachMessages, formatMessageForAnalysis(message))

			// Проверка на завершение срача по таймеру
			const srachTimeout = 5 * time.Minute // Тайм-аут 5 минут
			if !lastTriggerTime.IsZero() && messageTime.Sub(lastTriggerTime) > srachTimeout {
				log.Printf("Чат %d: Срач считается завершенным по тайм-ауту (%v).", chatID, srachTimeout)
				b.settingsMutex.Unlock()
				go b.analyseSrach(chatID) // Запускаем анализ в горутине
				goto SaveMessage          // Переходим к сохранению
			}
		}
		b.settingsMutex.Unlock() // Разблокируем, если не вышли раньше
	}
	// --- Конец Логики Анализа Срачей ---

SaveMessage: // Метка для перехода после обработки срача
	// --- Обработка обычных сообщений --- (Переносим сохранение сюда)
	// Сохраняем сообщение в общую историю (всегда)
	b.storage.AddMessage(chatID, message)

	// Проверяем, является ли сообщение ответом на сообщение бота или обращением к боту
	isReplyToBot := message.ReplyToMessage != nil &&
		message.ReplyToMessage.From != nil &&
		message.ReplyToMessage.From.ID == b.api.Self.ID
	mentionsBot := false
	if len(message.Entities) > 0 {
		for _, entity := range message.Entities {
			if entity.Type == "mention" {
				mention := message.Text[entity.Offset : entity.Offset+entity.Length]
				if mention == "@"+b.api.Self.UserName {
					mentionsBot = true
					break
				}
			}
		}
	}

	// Отвечаем на прямое обращение к боту
	if isReplyToBot || mentionsBot {
		log.Printf("Обнаружено прямое обращение к боту, отправляю ответ")
		go b.sendDirectResponse(chatID, message)
		return
	}

	// Увеличиваем счетчик сообщений и проверяем, нужно ли отвечать
	b.settingsMutex.Lock()
	settings.MessageCount++
	// Проверяем, активен ли бот и не идет ли анализ срача (чтобы не мешать)
	shouldReply := settings.Active && settings.SrachState != "analyzing" && settings.MinMessages > 0 && settings.MessageCount >= rand.Intn(settings.MaxMessages-settings.MinMessages+1)+settings.MinMessages
	if shouldReply {
		settings.MessageCount = 0 // Сбрасываем счетчик
	}
	b.settingsMutex.Unlock()

	// Отвечаем, если нужно
	if shouldReply {
		go b.sendAIResponse(chatID)
	}

	// Проверяем, было ли это обновление о входе бота в чат
	if message.NewChatMembers != nil {
		for _, member := range message.NewChatMembers {
			if member.ID == b.api.Self.ID {
				log.Printf("Бот добавлен в чат: %d (%s)", chatID, message.Chat.Title)
				go b.loadChatHistory(chatID) // Загрузка истории ВКЛЮЧЕНА
				b.sendReplyWithKeyboard(chatID, "Привет! Я готов к работе. Используйте /settings для настройки.", getMainKeyboard())
				break
			}
		}
	}
	// --- Конец обработки обычных сообщений ---

	if isDirectReply {
		// --- Обработка лимита прямых ответов ---
		now := time.Now()
		b.directReplyMutex.Lock()

		if _, chatExists := b.directReplyTimestamps[chatID]; !chatExists {
			b.directReplyTimestamps[chatID] = make(map[int64][]time.Time)
		}
		if _, userExists := b.directReplyTimestamps[chatID][userID]; !userExists {
			b.directReplyTimestamps[chatID][userID] = make([]time.Time, 0)
		}

		// Очищаем старые временные метки
		validTimestamps := make([]time.Time, 0)
		cutoff := now.Add(-b.config.DirectReplyRateLimitWindow)
		for _, ts := range b.directReplyTimestamps[chatID][userID] {
			if ts.After(cutoff) {
				validTimestamps = append(validTimestamps, ts)
			}
		}
		b.directReplyTimestamps[chatID][userID] = validTimestamps

		// Проверяем лимит
		if len(b.directReplyTimestamps[chatID][userID]) >= b.config.DirectReplyRateLimitCount {
			// Лимит превышен
			log.Printf("[Rate Limit] Чат %d, Пользователь %d: Превышен лимит прямых ответов (%d/%d за %v). Отвечаем по спец. промпту.",
				chatID, userID, len(b.directReplyTimestamps[chatID][userID]), b.config.DirectReplyRateLimitCount, b.config.DirectReplyRateLimitWindow)
			b.directReplyMutex.Unlock() // Разблокируем перед вызовом LLM

			// Генерируем ответ по спец. промпту (без контекста)
			response, err := b.gemini.GenerateArbitraryResponse(b.config.RateLimitDirectReplyPrompt, "") // Контекст не нужен
			if err != nil {
				log.Printf("[Rate Limit ERROR] Чат %d: Ошибка генерации ответа при превышении лимита: %v", chatID, err)
			} else {
				b.sendReply(chatID, response)
			}
			return // Выходим, чтобы не генерировать обычный прямой ответ
		} else {
			// Лимит не превышен, добавляем метку и генерируем обычный прямой ответ
			log.Printf("[Direct Reply] Чат %d, Пользователь %d: Лимит не превышен (%d/%d). Добавляем метку.",
				chatID, userID, len(b.directReplyTimestamps[chatID][userID]), b.config.DirectReplyRateLimitCount)
			b.directReplyTimestamps[chatID][userID] = append(b.directReplyTimestamps[chatID][userID], now)
			b.directReplyMutex.Unlock() // Разблокируем перед вызовом LLM

			// Генерируем ответ по промпту DIRECT_PROMPT (без контекста)
			b.sendDirectResponse(chatID, message) // Используем существующую функцию
			return
		}
		// --- Конец обработки лимита ---
	}
}

// handleCommand обрабатывает команды
func (b *Bot) handleCommand(message *tgbotapi.Message) {
	command := message.Command()
	chatID := message.Chat.ID

	switch command {
	case "start":
		b.settingsMutex.Lock()
		if settings, exists := b.chatSettings[chatID]; exists {
			settings.Active = true
		}
		b.settingsMutex.Unlock()

		b.sendReplyWithKeyboard(chatID, "Бот запущен и готов к работе!", getMainKeyboard())

	case "stop":
		b.settingsMutex.Lock()
		if settings, exists := b.chatSettings[chatID]; exists {
			settings.Active = false
		}
		b.settingsMutex.Unlock()

		b.sendReply(chatID, "Бот поставлен на паузу. Используйте /start чтобы возобновить.")

	case "summary":
		// Проверяем ограничение по времени
		b.summaryMutex.RLock()
		lastRequestTime, ok := b.lastSummaryRequest[chatID]
		b.summaryMutex.RUnlock()

		// Ограничение в 10 минут
		const rateLimitDuration = 10 * time.Minute
		timeSinceLastRequest := time.Since(lastRequestTime)

		if b.config.Debug {
			log.Printf("[DEBUG] Чат %d: /summary вызван. Последний запрос был: %v (ok=%t). Прошло: %s. Лимит: %s.",
				chatID, lastRequestTime, ok, timeSinceLastRequest.Round(time.Second), rateLimitDuration)
			log.Printf("[DEBUG] Чат %d: Сравниваем %s < %s ?", chatID, timeSinceLastRequest.Round(time.Second), rateLimitDuration)
			log.Printf("[DEBUG] Чат %d: Сообщение об ошибке из конфига: '%s'", chatID, b.config.RateLimitErrorMessage)
		}

		if ok && timeSinceLastRequest < rateLimitDuration {
			remainingTime := rateLimitDuration - timeSinceLastRequest
			// Формируем сообщение
			errorMsgText := b.config.RateLimitErrorMessage // Получаем текст из конфига
			fullErrorMsg := fmt.Sprintf("%s Осталось подождать: %s.",
				errorMsgText, // Используем полученный текст
				remainingTime.Round(time.Second).String(),
			)
			if b.config.Debug {
				log.Printf("[DEBUG] Чат %d: Rate limit активен. Текст ошибки из конфига: '%s'. Формированное сообщение: '%s'", chatID, errorMsgText, fullErrorMsg)
			}
			// Отправляем сформированное сообщение
			b.sendReply(chatID, fullErrorMsg)
			return
		}

		// Если ограничение прошло или запроса еще не было, обновляем время и генерируем саммари
		if b.config.Debug {
			log.Printf("[DEBUG] Чат %d: Rate limit пройден. Обновляю время последнего запроса на %v.", chatID, time.Now())
		}
		b.summaryMutex.Lock()
		b.lastSummaryRequest[chatID] = time.Now()
		b.summaryMutex.Unlock()

		if b.config.Debug {
			log.Printf("[DEBUG] Чат %d: Начинаю генерацию саммари (после обновления времени).", chatID)
		}
		go b.generateSummary(chatID) // Запускаем в горутине, чтобы не блокировать

	case "settings":
		b.sendSettingsKeyboard(chatID)

	case "menu": // Добавляем обработку /menu
		b.sendReplyWithKeyboard(chatID, "Главное меню:", getMainKeyboard())

	case "srach": // Добавляем обработку /srach
		b.toggleSrachAnalysis(chatID)
		b.sendSettingsKeyboard(chatID) // Показываем обновленное меню настроек

		// Можно добавить default для неизвестных команд
		// default:
		// 	b.sendReply(chatID, "Неизвестная команда.")
	}
}

// handleCallback обрабатывает нажатия на кнопки
func (b *Bot) handleCallback(callback *tgbotapi.CallbackQuery) {
	chatID := callback.Message.Chat.ID

	// Общий ключ для PendingSetting (пока не используется, т.к. PendingSetting хранится для chatID)
	// pendingKey := fmt.Sprintf("%d", chatID)

	var promptText string
	var settingToSet string

	switch callback.Data {
	case "set_min_messages":
		settingToSet = "min_messages"
		promptText = b.config.PromptEnterMinMessages
	case "set_max_messages":
		settingToSet = "max_messages"
		promptText = b.config.PromptEnterMaxMessages
	case "set_daily_time":
		settingToSet = "daily_time"
		promptText = fmt.Sprintf(b.config.PromptEnterDailyTime, b.config.TimeZone) // Подставляем часовой пояс в промпт
	case "set_summary_interval":
		settingToSet = "summary_interval"
		promptText = b.config.PromptEnterSummaryInterval
	case "back_to_main":
		b.settingsMutex.Lock()
		// Сбрасываем ожидание ввода при выходе из настроек
		if settings, exists := b.chatSettings[chatID]; exists {
			settings.PendingSetting = ""
			deleteMsg := tgbotapi.NewDeleteMessage(chatID, callback.Message.MessageID) // Удаляем само меню настроек
			b.api.Request(deleteMsg)
		}
		b.settingsMutex.Unlock()

		// Отправляем основное меню
		b.sendReplyWithKeyboard(chatID, "Бот готов к работе!", getMainKeyboard())
		b.answerCallback(callback.ID, "") // Отвечаем на колбэк
		return                            // Выходим, дальнейшая обработка не нужна

	case "summary": // Обработка кнопки саммари из основного меню
		b.answerCallback(callback.ID, "Запрашиваю саммари...")
		// Корректно имитируем сообщение с командой
		// Создаем базовое сообщение, похожее на то, что пришло бы от пользователя
		fakeMessage := &tgbotapi.Message{
			MessageID: callback.Message.MessageID, // Можно использовать ID кнопки для контекста, но не обязательно
			From:      callback.From,              // Кто нажал кнопку
			Chat:      callback.Message.Chat,      // В каком чате
			Date:      int(time.Now().Unix()),     // Текущее время
			Text:      "/summary",                 // Текст команды
			Entities: []tgbotapi.MessageEntity{ // Указываем, что это команда
				{Type: "bot_command", Offset: 0, Length: len("/summary")},
			},
		}
		b.handleCommand(fakeMessage) // Передаем имитированное сообщение
		return                       // Выходим

	case "settings": // Обработка кнопки настроек из основного меню
		// Удаляем сообщение с основным меню
		deleteMsg := tgbotapi.NewDeleteMessage(chatID, callback.Message.MessageID)
		b.api.Request(deleteMsg)
		b.sendSettingsKeyboard(chatID)
		b.answerCallback(callback.ID, "") // Отвечаем на колбэк
		return                            // Выходим

	case "stop": // Обработка кнопки паузы из основного меню
		b.settingsMutex.Lock()
		if settings, exists := b.chatSettings[chatID]; exists {
			settings.Active = false
		}
		b.settingsMutex.Unlock()
		// Удаляем сообщение с основным меню
		deleteMsg := tgbotapi.NewDeleteMessage(chatID, callback.Message.MessageID)
		b.api.Request(deleteMsg)
		// Отправляем текстовое подтверждение
		b.sendReply(chatID, "Бот поставлен на паузу. Используйте /start чтобы возобновить.")
		b.answerCallback(callback.ID, "Бот остановлен")
		return // Выходим

	// Новые коллбэки для управления анализом срачей
	case "toggle_srach_on":
		b.setSrachAnalysis(chatID, true)
		b.answerCallback(callback.ID, "🔥 Анализ срачей включен")
		b.updateSettingsKeyboard(callback) // Обновляем сообщение с клавиатурой
		return                             // Выходим, дальнейшая обработка не нужна
	case "toggle_srach_off":
		b.setSrachAnalysis(chatID, false)
		b.answerCallback(callback.ID, "💀 Анализ срачей выключен")
		b.updateSettingsKeyboard(callback) // Обновляем сообщение с клавиатурой
		return                             // Выходим, дальнейшая обработка не нужна

	default:
		log.Printf("Неизвестный callback data: %s", callback.Data)
		b.answerCallback(callback.ID, "Неизвестное действие")
		return // Выходим
	}

	// Если мы дошли сюда, значит, была нажата кнопка "Установить..."
	if settingToSet != "" {
		b.settingsMutex.Lock()
		if settings, exists := b.chatSettings[chatID]; exists {
			settings.PendingSetting = settingToSet // Устанавливаем ожидание
		}
		b.settingsMutex.Unlock()

		// Отправляем сообщение с запросом ввода
		// Сначала удаляем старое меню настроек
		deleteMsg := tgbotapi.NewDeleteMessage(chatID, callback.Message.MessageID)
		b.api.Request(deleteMsg)
		// Затем отправляем промпт
		b.sendReply(chatID, promptText+"\n\nИли отправьте /cancel для отмены.")
		b.answerCallback(callback.ID, "Ожидаю ввода...")
	}
}

// sendAIResponse генерирует и отправляет ответ нейросети
func (b *Bot) sendAIResponse(chatID int64) {
	if b.config.Debug {
		log.Printf("[DEBUG] Генерация AI ответа для чата %d", chatID)
	}

	// Получаем историю сообщений
	messages := b.storage.GetMessages(chatID)
	if len(messages) == 0 {
		if b.config.Debug {
			log.Printf("[DEBUG] Нет сообщений для чата %d, ответ не отправлен", chatID)
		}
		return
	}

	// Получаем настройки промпта
	b.settingsMutex.RLock()
	settings, exists := b.chatSettings[chatID]
	prompt := b.config.DefaultPrompt
	if exists && settings.CustomPrompt != "" {
		prompt = settings.CustomPrompt
	}
	b.settingsMutex.RUnlock()

	if b.config.Debug {
		log.Printf("[DEBUG] Используется промпт: %s", prompt[:min(30, len(prompt))]+"...")
		log.Printf("[DEBUG] Количество сообщений в контексте: %d", len(messages))
	}

	// Отправляем запрос к Gemini
	response, err := b.gemini.GenerateResponse(prompt, messages)
	if err != nil {
		if b.config.Debug {
			log.Printf("[DEBUG] Ошибка при генерации ответа: %v. Полный текст ошибки: %s", err, err.Error())
			log.Printf("[DEBUG] API ключ: %s... (первые 5 символов)", b.config.GeminiAPIKey[:5])
		} else {
			log.Printf("Ошибка при генерации ответа: %v", err)
		}
		return
	}

	// Отправляем ответ в чат
	b.sendReply(chatID, response)

	if b.config.Debug {
		log.Printf("[DEBUG] Успешно отправлен AI ответ в чат %d", chatID)
	}
}

// generateSummary создает и отправляет саммари диалога
func (b *Bot) generateSummary(chatID int64) {
	// Получаем сообщения за последние 24 часа
	messages := b.storage.GetMessagesSince(chatID, time.Now().Add(-24*time.Hour))
	if len(messages) == 0 {
		b.sendReply(chatID, "Недостаточно сообщений для создания саммари.")
		return
	}

	if b.config.Debug {
		log.Printf("[DEBUG] Создаю саммари для чата %d. Найдено сообщений: %d", chatID, len(messages))
	}

	// Используем только промпт для саммари без комбинирования
	summaryPrompt := b.config.SummaryPrompt

	const maxAttempts = 3 // Максимальное количество попыток генерации
	const minWords = 10   // Минимальное количество слов в саммари

	var finalSummary string
	var lastErr error // Сохраняем последнюю ошибку API
	var attempt int

	for attempt = 1; attempt <= maxAttempts; attempt++ {
		if b.config.Debug {
			log.Printf("[DEBUG] Чат %d: Попытка генерации саммари №%d", chatID, attempt)
		}

		// Отправляем запрос к Gemini с промптом для саммари
		summary, err := b.gemini.GenerateResponse(summaryPrompt, messages)
		if err != nil {
			lastErr = err // Сохраняем последнюю ошибку
			if b.config.Debug {
				log.Printf("[DEBUG] Чат %d: Ошибка при генерации саммари (попытка %d): %v", chatID, attempt, err)
			}
			// При ошибке API нет смысла повторять сразу без паузы
			if attempt < maxAttempts {
				time.Sleep(1 * time.Second)
			}
			continue // Переходим к следующей попытке
		}

		// Проверяем количество слов
		wordCount := len(strings.Fields(summary))
		if b.config.Debug {
			log.Printf("[DEBUG] Чат %d: Сгенерировано саммари (попытка %d), слов: %d. Текст: %s...", chatID, attempt, wordCount, truncateString(summary, 50))
		}

		if wordCount >= minWords {
			finalSummary = summary
			lastErr = nil // Сбрасываем ошибку при успехе
			break         // Успешная генерация, выходим из цикла
		}

		// Если слов мало, добавляем небольшую задержку перед следующей попыткой
		if attempt < maxAttempts {
			time.Sleep(1 * time.Second)
		}
	}

	// Проверяем результат после всех попыток
	if finalSummary == "" {
		if b.config.Debug {
			log.Printf("[DEBUG] Чат %d: Не удалось сгенерировать достаточно длинное саммари после %d попыток.", chatID, maxAttempts)
		}
		errMsg := "Не удалось создать достаточно информативное саммари после нескольких попыток."
		if lastErr != nil { // Если последняя попытка завершилась ошибкой API или предыдущие были неудачными
			errMsg += fmt.Sprintf(" Последняя ошибка: %v", lastErr)
		}
		b.sendReply(chatID, errMsg)
		return
	}

	if b.config.Debug {
		log.Printf("[DEBUG] Саммари успешно создано для чата %d после %d попыток", chatID, attempt)
	}

	// Отправляем финальное саммари
	finalMessageText := fmt.Sprintf("📋 *Саммари диалога за последние 24 часа:*\n\n%s", finalSummary)
	msg := tgbotapi.NewMessage(chatID, finalMessageText)
	msg.ParseMode = "Markdown"
	_, sendErr := b.api.Send(msg)
	if sendErr != nil {
		log.Printf("Ошибка отправки финального саммари в чат %d: %v", chatID, sendErr)
	}
}

// Вспомогательные методы для работы с Telegram API
func (b *Bot) sendReply(chatID int64, text string) {
	msg := tgbotapi.NewMessage(chatID, text)
	msg.ParseMode = "Markdown"

	_, err := b.api.Send(msg)
	if err != nil {
		log.Printf("Ошибка отправки сообщения: %v", err)
	}
}

func (b *Bot) sendReplyWithKeyboard(chatID int64, text string, keyboard tgbotapi.InlineKeyboardMarkup) {
	msg := tgbotapi.NewMessage(chatID, text)
	msg.ParseMode = "Markdown"
	msg.ReplyMarkup = keyboard

	_, err := b.api.Send(msg)
	if err != nil {
		log.Printf("Ошибка отправки сообщения: %v", err)
	}
}

func (b *Bot) answerCallback(callbackID string, text string) {
	callback := tgbotapi.NewCallback(callbackID, text)
	_, err := b.api.Request(callback)
	if err != nil {
		log.Printf("Ошибка ответа на callback: %v", err)
	}
}

func (b *Bot) sendSettingsKeyboard(chatID int64) {
	settings, err := b.loadChatSettings(chatID) // Используем уже исправленную функцию
	if err != nil {
		log.Printf("sendSettingsKeyboard: Ошибка загрузки/создания настроек для чата %d: %v", chatID, err)
		// Можно отправить сообщение об ошибке пользователю
		return
	}

	b.settingsMutex.RLock()
	prevMessageID := settings.LastMessageID // Используем LastMessageID
	summaryInterval := settings.SummaryIntervalHours
	srachEnabled := settings.SrachAnalysisEnabled
	// Копируем нужные значения перед разблокировкой
	minMessages := settings.MinMessages
	maxMessages := settings.MaxMessages
	dailyTakeTime := settings.DailyTakeTime
	b.settingsMutex.RUnlock()

	// Удаляем предыдущее сообщение с меню, если оно существует
	if prevMessageID != 0 {
		deleteMsg := tgbotapi.NewDeleteMessage(chatID, prevMessageID)
		_, err := b.api.Request(deleteMsg)
		if err != nil {
			// Логируем ошибку, но не прерываем отправку нового меню
			log.Printf("Ошибка при удалении предыдущего меню (ID: %d) в чате %d: %v", prevMessageID, chatID, err)
		}
	}

	text := fmt.Sprintf("⚙️ *Настройки бота*\n\n"+
		"Ответ после: %d - %d сообщ.\n"+
		"Тема дня: %d:00 (%s)\n"+
		"Авто-саммари: %s\n"+
		"Анализ срачей: %s",
		minMessages, maxMessages,
		dailyTakeTime, b.config.TimeZone,
		formatSummaryInterval(summaryInterval),
		formatEnabled(srachEnabled))

	// Передаем все 5 аргументов
	keyboard := getSettingsKeyboard(minMessages, maxMessages, dailyTakeTime, summaryInterval, srachEnabled)

	// Отправляем новое меню и сохраняем его ID
	msg := tgbotapi.NewMessage(chatID, text)
	msg.ParseMode = "Markdown"
	msg.ReplyMarkup = keyboard

	sentMsg, err := b.api.Send(msg)
	if err != nil {
		log.Printf("Ошибка отправки сообщения настроек: %v", err)
		return
	}

	// Сохраняем ID отправленного сообщения
	b.settingsMutex.Lock()
	if settings, exists := b.chatSettings[chatID]; exists { // Проверяем еще раз на случай удаления чата
		settings.LastMessageID = sentMsg.MessageID // Используем LastMessageID
		settings.PendingSetting = ""               // Сбрасываем ожидание ввода при показе меню
	}
	b.settingsMutex.Unlock()
}

// formatSummaryInterval форматирует интервал саммари для отображения
func formatSummaryInterval(hours int) string {
	if hours <= 0 {
		return "Выкл."
	}
	return fmt.Sprintf("каждые %d ч.", hours)
}

// sendDirectResponse отправляет ответ на прямое обращение к боту
func (b *Bot) sendDirectResponse(chatID int64, message *tgbotapi.Message) {
	if b.config.Debug {
		log.Printf("[DEBUG] Отправка прямого ответа в чат %d на сообщение от %s (%s)",
			chatID, message.From.FirstName, message.From.UserName)
	}

	// Получаем некоторый контекст из истории
	messages := b.storage.GetMessages(chatID)

	// Для прямого ответа используем только DIRECT_PROMPT
	response, err := b.gemini.GenerateResponse(b.config.DirectPrompt, messages)
	if err != nil {
		if b.config.Debug {
			log.Printf("[DEBUG] Ошибка при генерации прямого ответа: %v. Полный текст ошибки: %s", err, err.Error())
			log.Printf("[DEBUG] API ключ: %s... (первые 5 символов)", b.config.GeminiAPIKey[:5])
			log.Printf("[DEBUG] Промпт для прямого ответа: %s", b.config.DirectPrompt)
			log.Printf("[DEBUG] Количество сообщений в контексте: %d", len(messages))
		} else {
			log.Printf("Ошибка при генерации прямого ответа: %v", err)
		}
		return
	}

	// Создаем сообщение с ответом на исходное сообщение
	msg := tgbotapi.NewMessage(chatID, response)
	msg.ParseMode = "Markdown"
	msg.ReplyToMessageID = message.MessageID

	_, err = b.api.Send(msg)
	if err != nil {
		if b.config.Debug {
			log.Printf("[DEBUG] Ошибка отправки сообщения: %v. Полный текст ошибки: %s", err, err.Error())
		} else {
			log.Printf("Ошибка отправки сообщения: %v", err)
		}
	} else if b.config.Debug {
		log.Printf("[DEBUG] Успешно отправлен прямой ответ в чат %d", chatID)
	}
}

// scheduleDailyTake запускает планировщик для ежедневного тейка
func (b *Bot) scheduleDailyTake(dailyTakeTime int, timeZone string) {
	// Получаем локацию из конфига
	loc, err := time.LoadLocation(timeZone)
	if err != nil {
		log.Printf("Ошибка загрузки часового пояса, используем UTC: %v", err)
		loc = time.UTC
	}

	for {
		now := time.Now().In(loc)
		targetTime := time.Date(
			now.Year(), now.Month(), now.Day(),
			dailyTakeTime, 0, 0, 0,
			loc,
		)

		// Если сейчас уже после времени запуска, планируем на завтра
		if now.After(targetTime) {
			targetTime = targetTime.Add(24 * time.Hour)
		}

		// Вычисляем время до следующего запуска
		sleepDuration := targetTime.Sub(now)
		log.Printf("Запланирован тейк через %v (в %s по %s)",
			sleepDuration, targetTime.Format("15:04"), timeZone)

		// Спим до нужного времени
		time.Sleep(sleepDuration)

		// Отправляем тейк во все активные чаты
		b.sendDailyTakeToAllChats()
	}
}

// sendDailyTakeToAllChats отправляет ежедневный тейк во все активные чаты
func (b *Bot) sendDailyTakeToAllChats() {
	if b.config.Debug {
		log.Printf("[DEBUG] Запуск ежедневного тейка для всех активных чатов")
	}

	// Используем только промпт для ежедневного тейка без комбинирования
	dailyTakePrompt := b.config.DailyTakePrompt

	// Генерируем тейк с промптом
	take, err := b.gemini.GenerateResponse(dailyTakePrompt, nil)
	if err != nil {
		if b.config.Debug {
			log.Printf("[DEBUG] Ошибка при генерации ежедневного тейка: %v. Полный текст ошибки: %s", err, err.Error())
			log.Printf("[DEBUG] API ключ: %s... (первые 5 символов)", b.config.GeminiAPIKey[:5])
			log.Printf("[DEBUG] Промпт для тейка: %s", dailyTakePrompt)
		} else {
			log.Printf("Ошибка при генерации ежедневного тейка: %v", err)
		}
		return
	}

	message := "🔥 *Тема дня:*\n\n" + take

	// Отправляем во все активные чаты
	b.settingsMutex.RLock()
	defer b.settingsMutex.RUnlock()

	activeChats := 0
	for chatID, settings := range b.chatSettings {
		if settings.Active {
			activeChats++
			go func(cid int64) {
				b.sendReply(cid, message)
			}(chatID)
		}
	}

	if b.config.Debug {
		log.Printf("[DEBUG] Тема дня отправлена в %d активных чатов", activeChats)
	}
}

// Вспомогательная функция для min
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// schedulePeriodicSummary запускает планировщик для автоматической генерации саммари
func (b *Bot) schedulePeriodicSummary() {
	// Проверяем необходимость запуска планировщика
	// (Можно добавить проверку, есть ли вообще чаты с включенным авто-саммари,
	// но для простоты пока запускаем тикер всегда)

	log.Println("Запуск планировщика автоматического саммари...")
	// Запускаем тикер, например, раз в час. Более частая проверка не имеет смысла,
	// так как минимальный интервал - 1 час.
	ticker := time.NewTicker(1 * time.Hour)
	b.autoSummaryTicker = ticker // Сохраняем тикер, чтобы можно было остановить
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			b.checkAndRunAutoSummaries()
		case <-b.stop:
			log.Println("Остановка планировщика автоматического саммари.")
			return
		}
	}
}

// checkAndRunAutoSummaries проверяет все чаты и запускает генерацию саммари, если пора
func (b *Bot) checkAndRunAutoSummaries() {
	now := time.Now()
	if b.config.Debug {
		log.Printf("[AutoSummary Check] Проверка необходимости авто-саммари в %v", now)
	}

	b.settingsMutex.RLock()
	chatsToCheck := make(map[int64]*ChatSettings)
	for chatID, settings := range b.chatSettings {
		// Копируем настройки, чтобы не держать мьютекс во время генерации
		chatsToCheck[chatID] = settings
	}
	b.settingsMutex.RUnlock()

	for chatID, settings := range chatsToCheck {
		if settings.Active && settings.SummaryIntervalHours > 0 {
			durationSinceLast := now.Sub(settings.LastAutoSummaryTime)
			requiredInterval := time.Duration(settings.SummaryIntervalHours) * time.Hour

			if durationSinceLast >= requiredInterval {
				if b.config.Debug {
					log.Printf("[AutoSummary Run] Чат %d: Пора генерировать саммари. Интервал: %dч. Прошло: %v. Последнее: %v",
						chatID, settings.SummaryIntervalHours, durationSinceLast, settings.LastAutoSummaryTime)
				}
				// Обновляем время *перед* запуском, чтобы избежать двойного запуска при долгой генерации
				b.updateLastAutoSummaryTime(chatID, now)
				// Запускаем генерацию в отдельной горутине
				go b.generateSummary(chatID)
			}
		}
	}
}

// updateLastAutoSummaryTime обновляет время последнего авто-саммари для чата
func (b *Bot) updateLastAutoSummaryTime(chatID int64, t time.Time) {
	b.settingsMutex.Lock()
	if settings, exists := b.chatSettings[chatID]; exists {
		settings.LastAutoSummaryTime = t
	}
	b.settingsMutex.Unlock()
}

// loadChatSettings загружает настройки для чата из памяти или создает новые.
func (b *Bot) loadChatSettings(chatID int64) (*ChatSettings, error) {
	b.settingsMutex.RLock() // Блокируем на чтение
	settings, exists := b.chatSettings[chatID]
	b.settingsMutex.RUnlock()

	if exists {
		// Настройки найдены в памяти
		return settings, nil
	}

	// Настройки не найдены в памяти - создаем новые
	// Блокируем на запись, так как будем изменять мапу
	b.settingsMutex.Lock()
	defer b.settingsMutex.Unlock()

	// Повторная проверка на случай, если другой поток создал настройки, пока мы ждали блокировки
	settings, exists = b.chatSettings[chatID]
	if exists {
		return settings, nil
	}

	// Создаем новые настройки
	log.Printf("Создаю новые настройки по умолчанию для чата %d", chatID)
	newSettings := &ChatSettings{
		Active:               true,
		CustomPrompt:         b.config.DefaultPrompt,
		MinMessages:          b.config.MinMessages,
		MaxMessages:          b.config.MaxMessages,
		MessageCount:         0,
		LastMessageID:        0,
		PendingSetting:       "",
		SummaryIntervalHours: b.config.SummaryIntervalHours,
		LastAutoSummaryTime:  time.Time{},
		// Инициализация полей Srach Analysis
		SrachAnalysisEnabled: true,
		SrachState:           "none",
		SrachStartTime:       time.Time{},
		SrachMessages:        make([]string, 0),
		LastSrachTriggerTime: time.Time{},
		SrachLlmCheckCounter: 0,
		// Добавляем поля, которые используются в sendSettingsKeyboard, если они есть в структуре
		DailyTakeTime: b.config.DailyTakeTime, // Убедимся, что это поле существует в ChatSettings
		// LastMessageID уже есть выше
	}

	// Добавляем новые настройки в мапу
	b.chatSettings[chatID] = newSettings

	// Нет необходимости сохранять в файл, так как настройки только в памяти

	return newSettings, nil
}

// Добавляем вспомогательные функции для управления SrachAnalysis
func (b *Bot) setSrachAnalysis(chatID int64, enabled bool) {
	b.settingsMutex.Lock()
	defer b.settingsMutex.Unlock()
	if settings, exists := b.chatSettings[chatID]; exists {
		settings.SrachAnalysisEnabled = enabled
		log.Printf("Чат %d: Анализ срачей %s", chatID, formatEnabled(enabled))
		// Можно добавить сохранение настроек, если они персистентны
	}
}

func (b *Bot) toggleSrachAnalysis(chatID int64) {
	b.settingsMutex.Lock()
	defer b.settingsMutex.Unlock()
	if settings, exists := b.chatSettings[chatID]; exists {
		settings.SrachAnalysisEnabled = !settings.SrachAnalysisEnabled
		log.Printf("Чат %d: Анализ срачей переключен на %s", chatID, formatEnabled(settings.SrachAnalysisEnabled))
		// Можно добавить сохранение настроек
	}
}

// updateSettingsKeyboard обновляет существующее сообщение с меню настроек
func (b *Bot) updateSettingsKeyboard(callback *tgbotapi.CallbackQuery) {
	chatID := callback.Message.Chat.ID

	settings, err := b.loadChatSettings(chatID)
	if err != nil {
		log.Printf("updateSettingsKeyboard: Ошибка загрузки/создания настроек для чата %d: %v", chatID, err)
		return
	}

	b.settingsMutex.RLock()
	// Копируем значения
	minMessages := settings.MinMessages
	maxMessages := settings.MaxMessages
	dailyTakeTime := settings.DailyTakeTime
	summaryInterval := settings.SummaryIntervalHours
	srachEnabled := settings.SrachAnalysisEnabled
	b.settingsMutex.RUnlock()

	text := fmt.Sprintf("⚙️ *Настройки бота*\n\n"+
		"Ответ после: %d - %d сообщ.\n"+
		"Тема дня: %d:00 (%s)\n"+
		"Авто-саммари: %s\n"+
		"Анализ срачей: %s",
		minMessages, maxMessages,
		dailyTakeTime, b.config.TimeZone,
		formatSummaryInterval(summaryInterval),
		formatEnabled(srachEnabled))

	keyboard := getSettingsKeyboard(minMessages, maxMessages, dailyTakeTime, summaryInterval, srachEnabled)

	// Обновляем существующее сообщение
	edit := tgbotapi.NewEditMessageTextAndMarkup(chatID, callback.Message.MessageID, text, keyboard)
	edit.ParseMode = "Markdown"

	_, err = b.api.Send(edit)
	if err != nil {
		log.Printf("Ошибка обновления сообщения настроек (EditMessageTextAndMarkup) в чате %d: %v", chatID, err)
	}
}

// formatEnabled форматирует булево значение для отображения
func formatEnabled(enabled bool) string {
	if enabled {
		return "Вкл 🔥"
	}
	return "Выкл 💀"
}

// --- Новые вспомогательные функции для анализа срачей ---

// isPotentialSrachTrigger проверяет, может ли сообщение быть триггером срача
// (Reply, Mention или ключевое слово)
func (b *Bot) isPotentialSrachTrigger(msg *tgbotapi.Message) bool {
	if msg == nil {
		return false
	}
	// 1. Проверка на Reply
	if msg.ReplyToMessage != nil {
		return true
	}
	// 2. Проверка на Mention
	if len(msg.Entities) > 0 {
		for _, entity := range msg.Entities {
			if entity.Type == "mention" || entity.Type == "text_mention" {
				return true
			}
		}
	}
	// 3. Проверка по ключевым словам из конфига
	if len(b.config.SrachKeywords) > 0 && msg.Text != "" {
		messageLower := strings.ToLower(msg.Text)
		for _, keyword := range b.config.SrachKeywords {
			// Можно усложнить до поиска целых слов, если нужно.
			if strings.Contains(messageLower, keyword) {
				if b.config.Debug {
					log.Printf("[Srach Detect] Найдено ключевое слово '%s' в сообщении: \"%s...\"", keyword, truncateString(msg.Text, 50))
				}
				return true
			}
		}
	}

	// TODO: Добавить оценку тональности через LLM?
	return false
}

// formatMessageForAnalysis форматирует сообщение для передачи в LLM при анализе срача
func formatMessageForAnalysis(msg *tgbotapi.Message) string {
	if msg == nil {
		return ""
	}
	userName := "UnknownUser"
	if msg.From != nil {
		userName = msg.From.UserName
		if userName == "" {
			userName = msg.From.FirstName
		}
	}
	// Добавляем информацию об ответе, если есть
	replyInfo := ""
	if msg.ReplyToMessage != nil && msg.ReplyToMessage.From != nil {
		replyUser := msg.ReplyToMessage.From.UserName
		if replyUser == "" {
			replyUser = msg.ReplyToMessage.From.FirstName
		}
		replyInfo = fmt.Sprintf(" (in reply to %s)", replyUser)
	}

	return fmt.Sprintf("[%s]%s: %s", userName, replyInfo, msg.Text)
}

// sendSrachWarning отправляет сообщение о начале срача
func (b *Bot) sendSrachWarning(chatID int64) {
	prompt := b.config.SRACH_WARNING_PROMPT // Загружаем промпт из конфига
	if prompt == "" {
		prompt = "Внимание, зафиксирован срач!"
	}
	// Пока просто отправляем текст из промпта
	b.sendReply(chatID, prompt)
}

// analyseSrach запускает анализ завершенного срача
func (b *Bot) analyseSrach(chatID int64) {
	b.settingsMutex.Lock()
	settings, exists := b.chatSettings[chatID]
	if !exists || settings.SrachState != "detected" || len(settings.SrachMessages) == 0 {
		b.settingsMutex.Unlock()
		return // Нечего анализировать
	}

	log.Printf("Чат %d: Срач завершен. Начинаю анализ %d сообщений.", chatID, len(settings.SrachMessages))
	settings.SrachState = "analyzing" // Меняем состояние
	srachHistory := strings.Join(settings.SrachMessages, "\n")
	settings.SrachMessages = make([]string, 0)       // Очищаем собранные сообщения
	analysisPrompt := b.config.SRACH_ANALYSIS_PROMPT // Получаем промпт для анализа
	b.settingsMutex.Unlock()

	// --- Вызов LLM для анализа --- (Используем новую функцию)
	var analysisResult string
	var err error

	if analysisPrompt == "" {
		log.Printf("Чат %d: Промпт SRACH_ANALYSIS_PROMPT пуст в конфиге!", chatID)
		analysisResult = "[Ошибка: Промпт для анализа не задан в конфигурации]"
		err = fmt.Errorf("SRACH_ANALYSIS_PROMPT is empty")
	} else {
		// Вызываем новую функцию Gemini клиента
		analysisResult, err = b.gemini.GenerateArbitraryResponse(analysisPrompt, srachHistory)
	}
	// --------------------------

	if err != nil {
		log.Printf("Чат %d: Ошибка анализа срача: %v", chatID, err)
		b.sendReply(chatID, "😵‍💫 Не удалось проанализировать срач. Возможно, сервер ИИ перегружен или произошла внутренняя ошибка.")
	} else {
		b.sendReply(chatID, analysisResult) // Отправляем результат
	}

	// Сбрасываем состояние после анализа
	b.settingsMutex.Lock()
	if settings, exists := b.chatSettings[chatID]; exists {
		// Убедимся, что состояние все еще 'analyzing', прежде чем сбрасывать
		if settings.SrachState == "analyzing" {
			settings.SrachState = "none"
			settings.LastSrachTriggerTime = time.Time{} // Сбрасываем и время триггера
		}
	}
	b.settingsMutex.Unlock()
}

// Вспомогательная функция для обрезки строки (ИСПРАВЛЕНА)
func truncateString(s string, maxLen int) string {
	runes := []rune(s) // Сразу работаем с рунами
	if len(runes) <= maxLen {
		return s // Если рун меньше или равно maxLen, возвращаем как есть
	}
	if maxLen < 3 { // Минимальная длина для добавления "..."
		// Если maxLen слишком мало, просто обрезаем до maxLen рун
		return string(runes[:maxLen])
	}
	// Обрезаем до maxLen-3 рун и добавляем "..."
	return string(runes[:maxLen-3]) + "..."
}

// --- Восстановленные функции для сохранения/загрузки истории ---

// loadChatHistory загружает историю сообщений для указанного чата
func (b *Bot) loadChatHistory(chatID int64) {
	if b.config.Debug {
		log.Printf("[DEBUG][Load History] Чат %d: Начинаю загрузку истории.", chatID)
	}

	b.sendReply(chatID, "⏳ Загружаю историю чата для лучшего понимания контекста...")

	// Загружаем историю из файла
	history, err := b.storage.LoadChatHistory(chatID)
	if err != nil {
		// Логируем ошибку, но не останавливаемся, просто начинаем без истории
		log.Printf("[ERROR][Load History] Чат %d: Ошибка загрузки истории: %v", chatID, err)
		b.sendReply(chatID, "⚠️ Не удалось загрузить историю чата. Начинаю работу с чистого листа.")
		// Убедимся, что старая история в памяти очищена, если была ошибка загрузки
		b.storage.ClearChatHistory(chatID) // Используем существующий метод
		return
	}

	if history == nil { // LoadChatHistory теперь возвращает nil, nil если файла нет
		if b.config.Debug {
			log.Printf("[DEBUG][Load History] Чат %d: История не найдена или файл не существует.", chatID)
		}
		b.sendReply(chatID, "✅ История чата не найдена. Начинаю работу с чистого листа!")
		return
	}

	if len(history) == 0 {
		if b.config.Debug {
			log.Printf("[DEBUG][Load History] Чат %d: Загружена пустая история (файл был пуст или содержал []).", chatID)
		}
		b.sendReply(chatID, "✅ История чата пуста. Начинаю работу с чистого листа!")
		return
	}

	// Определяем, сколько сообщений загружать (берем последние N)
	loadCount := len(history)
	if loadCount > b.config.ContextWindow {
		log.Printf("[DEBUG][Load History] Чат %d: История (%d) длиннее окна (%d), обрезаю.", chatID, loadCount, b.config.ContextWindow)
		history = history[loadCount-b.config.ContextWindow:]
		loadCount = len(history) // Обновляем количество после обрезки
	}

	// Добавляем сообщения в хранилище (в память)
	log.Printf("[DEBUG][Load History] Чат %d: Добавляю %d загруженных сообщений в контекст.", chatID, loadCount)
	b.storage.AddMessagesToContext(chatID, history) // Этот метод не должен вызывать автосохранение

	if b.config.Debug {
		log.Printf("[DEBUG][Load History] Чат %d: Загружено и добавлено в контекст %d сообщений.", chatID, loadCount)
	}

	b.sendReply(chatID, fmt.Sprintf("✅ Контекст загружен: %d сообщений. Я готов к работе!", loadCount))
}

// scheduleHistorySaving запускает периодическое сохранение истории
func (b *Bot) scheduleHistorySaving() {
	if b.config.HistorySaveInterval <= 0 {
		log.Println("[HistorySave Scheduler] Интервал сохранения истории не задан или <= 0. Периодическое сохранение отключено.")
		return
	}

	log.Printf("[HistorySave Scheduler] Запуск планировщика сохранения истории с интервалом: %v", b.config.HistorySaveInterval)
	ticker := time.NewTicker(b.config.HistorySaveInterval)

	go func() {
		for {
			select {
			case <-ticker.C:
				log.Printf("[HistorySave Scheduler] Тикер сработал. Вызов saveAllChatHistories...")
				b.saveAllChatHistories()
			case <-b.stop:
				ticker.Stop()
				log.Println("[HistorySave Scheduler] Планировщик сохранения истории остановлен.")
				return
			}
		}
	}()
}

// saveAllChatHistories сохраняет историю всех чатов
func (b *Bot) saveAllChatHistories() {
	log.Println("[Bot SaveAll] Начало сохранения всех историй чатов...")
	if err := b.storage.SaveAllChatHistories(); err != nil {
		log.Printf("[Bot SaveAll ERROR] Ошибка при сохранении всех историй: %v", err)
	} else {
		log.Println("[Bot SaveAll] Сохранение всех историй чатов завершено успешно.")
	}
}

// --- Конец восстановленных функций ---

// confirmSrachWithLLM проверяет конфликтность сообщения с помощью LLM
func (b *Bot) confirmSrachWithLLM(chatID int64, messageText string) bool {
	prompt := b.config.SRACH_CONFIRM_PROMPT
	if prompt == "" {
		log.Printf("[WARN] Чат %d: Промпт SRACH_CONFIRM_PROMPT пуст, LLM проверка отключена.", chatID)
		return false // Не можем проверить без промпта
	}

	fullPrompt := prompt + "\n" + messageText // Добавляем текст сообщения к промпту

	if b.config.Debug {
		log.Printf("[DEBUG] Чат %d: Запуск LLM для подтверждения срача. Сообщение: \"%s...\"", chatID, truncateString(messageText, 50))
	}

	// Используем GenerateArbitraryResponse без истории, только промпт + текст сообщения
	response, err := b.gemini.GenerateArbitraryResponse(fullPrompt, "") // Передаем пустой контекст, т.к. он уже в промпте
	if err != nil {
		log.Printf("[ERROR] Чат %d: Ошибка LLM при подтверждении срача: %v", chatID, err)
		return false // В случае ошибки считаем, что не срач
	}

	// Парсим ответ (ожидаем "true" или "false")
	responseLower := strings.ToLower(strings.TrimSpace(response))
	isSrach := responseLower == "true"

	if b.config.Debug {
		log.Printf("[DEBUG] Чат %d: Результат LLM подтверждения срача: %s (ответ LLM: \"%s\")", chatID, strconv.FormatBool(isSrach), response)
	}

	return isSrach
}

// Вспомогательная функция для max
func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
