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

	// --- Установка команд для кнопки "Меню" Telegram ---
	commands := []tgbotapi.BotCommand{
		{Command: "start", Description: "🚀 Запустить/перезапустить бота"},
		{Command: "menu", Description: "📖 Показать главное меню"},
		{Command: "settings", Description: "⚙️ Открыть настройки"},
		{Command: "summary", Description: "📊 Запросить саммари"},
		{Command: "stop", Description: "⏸️ Поставить бота на паузу"},
		{Command: "help", Description: "❓ Помощь"},
		{Command: "ping", Description: "🏓 Проверить доступность"},
	}
	setCommandsConfig := tgbotapi.NewSetMyCommands(commands...)
	if _, err := api.Request(setCommandsConfig); err != nil {
		log.Printf("[WARN] Не удалось установить команды бота: %v", err)
	} else {
		log.Println("Команды бота успешно установлены.")
	}
	// --- Конец установки команд ---

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
	// --- СНАЧАЛА проверяем CallbackQuery от Inline кнопок ---
	if update.CallbackQuery != nil {
		b.handleCallbackQuery(update.CallbackQuery)
		return // Завершаем обработку, т.к. это был callback
	}

	// --- ЕСЛИ НЕ CallbackQuery, ТО проверяем обычное сообщение ---
	if update.Message == nil { // Игнорируем остальные типы обновлений
		return
	}

	// --- Дальнейшая обработка update.Message --- (код остается как был)
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

	// --- НОВЫЙ БЛОК: Обработка лимита прямых ответов --- (ПЕРЕМЕЩЕНО СЮДА)
	if isDirectReply {
		// Логика проверки и обработки лимита...
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
			return // Выходим, так как обработали прямое обращение (с лимитом)
		} else {
			// Лимит не превышен, добавляем метку и генерируем обычный прямой ответ
			log.Printf("[Direct Reply] Чат %d, Пользователь %d: Лимит не превышен (%d/%d). Добавляем метку.",
				chatID, userID, len(b.directReplyTimestamps[chatID][userID]), b.config.DirectReplyRateLimitCount)
			b.directReplyTimestamps[chatID][userID] = append(b.directReplyTimestamps[chatID][userID], now)
			b.directReplyMutex.Unlock() // Разблокируем перед вызовом LLM

			// Генерируем ответ по промпту DIRECT_PROMPT (без контекста)
			b.sendDirectResponse(chatID, message) // Используем существующую функцию
			return                                // Выходим, так как обработали прямое обращение (без лимита)
		}
		// --- Конец обработки лимита ---
	}
	// --- КОНЕЦ НОВОГО БЛОКА ---

	// --- Логика Анализа Срачей --- (Теперь идет ПОСЛЕ обработки прямых ответов)
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
	// --- Обработка обычных сообщений ---
	// Сохраняем сообщение в общую историю (всегда)
	// b.storage.AddMessage(chatID, message) // УЖЕ СДЕЛАНО В НАЧАЛЕ ФУНКЦИИ

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
}

// handleCallbackQuery обрабатывает нажатия на inline кнопки
func (b *Bot) handleCallbackQuery(callbackQuery *tgbotapi.CallbackQuery) {
	// 1. Отвечаем на CallbackQuery, чтобы убрать "часики" на кнопке
	callback := tgbotapi.NewCallback(callbackQuery.ID, "") // Можно добавить текст, который всплывет у пользователя
	if _, err := b.api.Request(callback); err != nil {
		log.Printf("Ошибка ответа на CallbackQuery: %v", err)
	}

	// 2. Получаем данные из callback
	data := callbackQuery.Data
	chatID := callbackQuery.Message.Chat.ID
	messageID := callbackQuery.Message.MessageID
	// userID := callbackQuery.From.ID // ID пользователя, который нажал кнопку

	log.Printf("Получен CallbackQuery: Data='%s', ChatID=%d, MessageID=%d, UserID=%d",
		data, chatID, messageID, callbackQuery.From.ID)

	// 3. Обрабатываем разные кнопки
	switch data {
	case "summary":
		// Вызываем обработчик команды саммари
		b.handleSummaryCommand(chatID)

	case "settings":
		// Отправляем клавиатуру настроек, редактируя исходное сообщение
		b.editToSettingsKeyboard(chatID, messageID)

	case "stop":
		// Ставим бота на паузу
		settings, _ := b.loadChatSettings(chatID)
		b.settingsMutex.Lock()
		settings.Active = false
		b.settingsMutex.Unlock()
		// Уведомляем и возвращаем главную клавиатуру
		b.editToMainKeyboard(chatID, messageID, "Бот поставлен на паузу.")

	case "back_to_main":
		// Возвращаемся к главной клавиатуре из настроек
		b.editToMainKeyboard(chatID, messageID, "Главное меню:")

	// --- Обработка кнопок изменения настроек ---
	case "set_min_messages", "set_max_messages", "set_daily_time", "set_summary_interval":
		b.handleSetNumericSettingCallback(chatID, messageID, data)

	case "toggle_srach_on", "toggle_srach_off":
		// Передаем ID callbackQuery
		b.handleToggleSrachCallback(chatID, messageID, data == "toggle_srach_on", callbackQuery.ID)

	default:
		log.Printf("Неизвестный CallbackQuery data: %s", data)
		// Можно отправить уведомление пользователю
		// answer := tgbotapi.NewCallbackWithAlert(callbackQuery.ID, "Неизвестное действие")
		// b.api.AnswerCallbackQuery(answer)
	}
}

// --- Новые вспомогательные функции для Callback ---

// editToMainKeyboard редактирует сообщение, чтобы показать главную клавиатуру
func (b *Bot) editToMainKeyboard(chatID int64, messageID int, text string) {
	keyboard := getMainKeyboard()
	editMsg := tgbotapi.NewEditMessageTextAndMarkup(chatID, messageID, text, keyboard)
	if _, err := b.api.Send(editMsg); err != nil {
		log.Printf("Ошибка редактирования сообщения на главную клавиатуру (ChatID: %d, MsgID: %d): %v", chatID, messageID, err)
	}
}

// editToSettingsKeyboard редактирует сообщение, чтобы показать клавиатуру настроек
func (b *Bot) editToSettingsKeyboard(chatID int64, messageID int) {
	settings, err := b.loadChatSettings(chatID)
	if err != nil {
		log.Printf("editToSettingsKeyboard: Не удалось загрузить настройки для чата %d: %v", chatID, err)
		b.sendReply(chatID, "Ошибка получения настроек.") // Отправляем новое сообщение, если редактирование невозможно
		return
	}
	b.settingsMutex.RLock()
	keyboard := getSettingsKeyboard(
		settings.MinMessages,
		settings.MaxMessages,
		settings.DailyTakeTime,
		settings.SummaryIntervalHours,
		settings.SrachAnalysisEnabled,
	)
	b.settingsMutex.RUnlock()

	editMsg := tgbotapi.NewEditMessageTextAndMarkup(chatID, messageID, "⚙️ Настройки:", keyboard)
	if _, err := b.api.Send(editMsg); err != nil {
		log.Printf("Ошибка редактирования сообщения на клавиатуру настроек (ChatID: %d, MsgID: %d): %v", chatID, messageID, err)
	}
}

// handleSetNumericSettingCallback обрабатывает нажатие кнопок для установки числовых настроек
func (b *Bot) handleSetNumericSettingCallback(chatID int64, messageID int, settingKey string) {
	settings, err := b.loadChatSettings(chatID)
	if err != nil {
		log.Printf("handleSetNumericSettingCallback: Не удалось загрузить настройки для чата %d: %v", chatID, err)
		return
	}

	var promptText string
	settingToSet := ""

	switch settingKey {
	case "set_min_messages":
		promptText = b.config.PromptEnterMinMessages
		settingToSet = "min_messages"
	case "set_max_messages":
		promptText = b.config.PromptEnterMaxMessages
		settingToSet = "max_messages"
	case "set_daily_time":
		promptText = fmt.Sprintf(b.config.PromptEnterDailyTime, b.config.TimeZone) // Добавляем таймзону в промпт
		settingToSet = "daily_time"
	case "set_summary_interval":
		promptText = b.config.PromptEnterSummaryInterval
		settingToSet = "summary_interval"
	default:
		log.Printf("Неизвестный ключ настройки: %s", settingKey)
		return
	}

	// Устанавливаем ожидание ввода
	b.settingsMutex.Lock()
	settings.PendingSetting = settingToSet
	b.settingsMutex.Unlock()

	// Отправляем сообщение с запросом ввода (не редактируем старое, а отправляем новое)
	b.sendReply(chatID, promptText+"\nИли введите /cancel для отмены.")
}

// handleToggleSrachCallback обрабатывает нажатие кнопки включения/выключения анализа срачей
func (b *Bot) handleToggleSrachCallback(chatID int64, messageID int, enable bool, callbackQueryID string) {
	settings, err := b.loadChatSettings(chatID)
	if err != nil {
		log.Printf("handleToggleSrachCallback: Не удалось загрузить настройки для чата %d: %v", chatID, err)
		return
	}

	// Обновляем настройку
	b.settingsMutex.Lock()
	settings.SrachAnalysisEnabled = enable
	b.settingsMutex.Unlock()

	// Обновляем клавиатуру в сообщении
	b.editToSettingsKeyboard(chatID, messageID) // Эта функция перерисует клавиатуру с новым состоянием

	// Отправляем уведомление (можно через AnswerCallbackQuery с show_alert=true)
	alertText := "Анализ срачей включен 🔥"
	if !enable {
		alertText = "Анализ срачей выключен 💀"
	}
	alertCallback := tgbotapi.NewCallbackWithAlert(callbackQueryID, alertText)
	if _, err := b.api.Request(alertCallback); err != nil {
		log.Printf("Ошибка ответа на CallbackQuery (toggle srach): %v", err)
	}
}

// --- Обработка команд ---

// handleCommand обрабатывает команды, начинающиеся с "/"
func (b *Bot) handleCommand(message *tgbotapi.Message) {
	chatID := message.Chat.ID
	command := message.Command()
	args := message.CommandArguments() // Аргументы после команды

	log.Printf("Получена команда '%s' с аргументами '%s' от %s в чате %d", command, args, message.From.UserName, chatID)

	switch command {
	case "start":
		settings, _ := b.loadChatSettings(chatID) // Загружаем или создаем настройки
		b.settingsMutex.Lock()
		settings.Active = true // Активируем бота по умолчанию при /start
		b.settingsMutex.Unlock()
		// Загружаем историю при старте (если есть)
		go b.loadChatHistory(chatID) // ВКЛЮЧЕНО
		b.sendReplyWithKeyboard(chatID, "Бот активирован. Генерирую случайные ответы. Используйте /settings для настройки.", getMainKeyboard())

	// ДОБАВЛЕНО: Обработка /menu как алиаса для /start
	case "menu":
		settings, _ := b.loadChatSettings(chatID) // Загружаем или создаем настройки
		b.settingsMutex.Lock()
		settings.Active = true // Активируем бота, если он был неактивен
		b.settingsMutex.Unlock()
		// Загружаем историю при старте (если есть и она не загружена)
		go b.loadChatHistory(chatID)
		// Отправляем главную клавиатуру
		b.sendReplyWithKeyboard(chatID, "Главное меню:", getMainKeyboard())

	case "stop":
		settings, _ := b.loadChatSettings(chatID)
		b.settingsMutex.Lock()
		settings.Active = false
		b.settingsMutex.Unlock()
		b.sendReply(chatID, "Бот деактивирован. Не буду отвечать на сообщения.")

	case "settings":
		b.sendSettingsKeyboard(chatID) // Отправляем меню настроек

	case "summary":
		b.handleSummaryCommand(chatID)

	case "ping":
		b.sendReply(chatID, "Pong!")

	case "help":
		helpText := `Доступные команды:
/start - Активировать бота в чате
/stop - Деактивировать бота в чате
/settings - Открыть меню настроек
/summary - Запросить саммари последних сообщений
/ping - Проверить доступность бота
/help - Показать это сообщение`
		b.sendReply(chatID, helpText)

	default:
		b.sendReply(chatID, "Неизвестная команда.")
	}
}

// handleSummaryCommand обрабатывает команду /summary
func (b *Bot) handleSummaryCommand(chatID int64) {
	b.summaryMutex.Lock()
	lastReq, ok := b.lastSummaryRequest[chatID]
	now := time.Now()
	// Лимит запросов саммари - 10 минут
	limitDuration := 10 * time.Minute
	if ok && now.Sub(lastReq) < limitDuration {
		// Лимит превышен
		remainingTime := limitDuration - now.Sub(lastReq)
		remainingStr := remainingTime.Round(time.Second).String()

		// Генерируем оскорбление с помощью Gemini
		insultPrompt := b.config.SummaryRateLimitInsultPrompt
		insult, err := b.gemini.GenerateArbitraryResponse(insultPrompt, "") // Без контекста
		if err != nil {
			log.Printf("[Summary Rate Limit ERROR] Чат %d: Ошибка генерации оскорбления: %v", chatID, err)
			// Отправляем только статичную часть в случае ошибки
			errorMessage := fmt.Sprintf("%s %s",
				b.config.SummaryRateLimitStaticPrefix,
				fmt.Sprintf(b.config.SummaryRateLimitStaticSuffix, remainingStr))
			b.summaryMutex.Unlock() // Разблокируем перед отправкой
			b.sendReply(chatID, errorMessage)
			return
		}

		// Собираем полное сообщение
		fullMessage := fmt.Sprintf("%s %s %s",
			b.config.SummaryRateLimitStaticPrefix,
			insult,
			fmt.Sprintf(b.config.SummaryRateLimitStaticSuffix, remainingStr))

		b.summaryMutex.Unlock() // Разблокируем перед отправкой
		b.sendReply(chatID, fullMessage)
		return
	}
	// Лимит не превышен, обновляем время последнего запроса
	b.lastSummaryRequest[chatID] = now
	b.summaryMutex.Unlock() // Разблокируем перед выполнением саммари

	b.sendReply(chatID, "Генерирую саммари за последние 24 часа...")
	go b.generateAndSendSummary(chatID) // Запускаем в горутине
}

// --- Управление настройками чата ---

// loadChatSettings загружает настройки для чата или создает новые по умолчанию
func (b *Bot) loadChatSettings(chatID int64) (*ChatSettings, error) {
	b.settingsMutex.RLock()
	settings, exists := b.chatSettings[chatID]
	b.settingsMutex.RUnlock()

	if exists {
		return settings, nil
	}

	// Если настроек нет, создаем по умолчанию
	log.Printf("Создаю новые настройки по умолчанию для чата %d", chatID)
	b.settingsMutex.Lock()
	defer b.settingsMutex.Unlock()

	// Перепроверяем на случай, если другой поток создал настройки, пока мы ждали Lock
	settings, exists = b.chatSettings[chatID]
	if exists {
		return settings, nil
	}

	newSettings := &ChatSettings{
		Active:               true, // Бот активен по умолчанию
		MinMessages:          b.config.MinMessages,
		MaxMessages:          b.config.MaxMessages,
		DailyTakeTime:        b.config.DailyTakeTime,
		SummaryIntervalHours: b.config.SummaryIntervalHours, // Используем значение из Config
		SrachAnalysisEnabled: true,                          // Анализ срачей включен по умолчанию
		SrachState:           "none",
	}
	b.chatSettings[chatID] = newSettings
	return newSettings, nil
}

// sendSettingsKeyboard отправляет клавиатуру настроек
func (b *Bot) sendSettingsKeyboard(chatID int64) {
	settings, err := b.loadChatSettings(chatID)
	if err != nil {
		log.Printf("sendSettingsKeyboard: Не удалось загрузить настройки для чата %d: %v", chatID, err)
		b.sendReply(chatID, "Ошибка получения настроек.")
		return
	}
	b.settingsMutex.RLock()
	keyboard := getSettingsKeyboard(
		settings.MinMessages,
		settings.MaxMessages,
		settings.DailyTakeTime,
		settings.SummaryIntervalHours,
		settings.SrachAnalysisEnabled,
	)
	b.settingsMutex.RUnlock()
	b.sendReplyWithKeyboard(chatID, "⚙️ Настройки:", keyboard)
}

// --- Отправка ответов AI ---

// sendAIResponse генерирует и отправляет ответ с помощью Gemini
func (b *Bot) sendAIResponse(chatID int64) {
	log.Printf("[DEBUG] Генерация AI ответа для чата %d", chatID)
	settings, _ := b.loadChatSettings(chatID) // Настройки должны быть уже загружены

	// Получаем историю сообщений
	messages := b.storage.GetMessages(chatID)
	if len(messages) == 0 {
		log.Printf("[DEBUG] Нет сообщений в истории для чата %d, ответ не генерируется", chatID)
		return
	}

	// Выбираем промпт
	b.settingsMutex.RLock()
	prompt := settings.CustomPrompt
	if prompt == "" {
		prompt = b.config.DefaultPrompt
	}
	b.settingsMutex.RUnlock()

	log.Printf("[DEBUG] Используется промпт: %s...", truncateString(prompt, 50))
	log.Printf("[DEBUG] Количество сообщений в контексте: %d", len(messages))

	response, err := b.gemini.GenerateResponse(prompt, messages)
	if err != nil {
		log.Printf("Ошибка генерации ответа AI для чата %d: %v", chatID, err)
		// Можно отправить сообщение об ошибке пользователю или просто пропустить
		// b.sendReply(chatID, "Извините, не могу сейчас ответить.")
		return
	}

	b.sendReply(chatID, response)
	log.Printf("[DEBUG] Успешно отправлен AI ответ в чат %d", chatID)
}

// sendDirectResponse генерирует ответ на прямое обращение
func (b *Bot) sendDirectResponse(chatID int64, message *tgbotapi.Message) {
	// Эта функция теперь вызывается только когда лимит НЕ превышен.
	// Она использует DIRECT_PROMPT без контекста истории.
	log.Printf("[DEBUG] Генерация ПРЯМОГО ответа для чата %d (лимит не превышен)", chatID)

	response, err := b.gemini.GenerateArbitraryResponse(b.config.DirectPrompt, "") // Без контекста
	if err != nil {
		log.Printf("Ошибка генерации прямого ответа для чата %d: %v", chatID, err)
		return
	}

	b.sendReply(chatID, response)
	log.Printf("[DEBUG] Успешно отправлен ПРЯМОЙ ответ в чат %d", chatID)
}

// --- Планировщики ---

// scheduleDailyTake запускает ежедневную отправку "темы дня"
func (b *Bot) scheduleDailyTake(hour int, timeZone string) {
	loc, err := time.LoadLocation(timeZone)
	if err != nil {
		log.Printf("Ошибка загрузки временной зоны '%s': %v, использую UTC", timeZone, err)
		loc = time.UTC
	}

	now := time.Now().In(loc)
	nextTake := time.Date(now.Year(), now.Month(), now.Day(), hour, 0, 0, 0, loc)
	if now.After(nextTake) {
		nextTake = nextTake.Add(24 * time.Hour) // Если время уже прошло, планируем на завтра
	}

	duration := nextTake.Sub(now)
	log.Printf("Запланирован тейк через %v (в %d:00 по %s)", duration, hour, loc.String())

	timer := time.NewTimer(duration)

	for {
		select {
		case <-timer.C:
			log.Println("Время ежедневного тейка!")
			b.sendDailyTakeToAllActiveChats()
			// Перепланируем на следующий день
			now = time.Now().In(loc)
			nextTake = time.Date(now.Year(), now.Month(), now.Day(), hour, 0, 0, 0, loc).Add(24 * time.Hour)
			duration = nextTake.Sub(now)
			timer.Reset(duration)
			log.Printf("Следующий тейк запланирован через %v", duration)
		case <-b.stop:
			log.Println("Остановка планировщика тейков.")
			timer.Stop()
			return
		}
	}
}

// sendDailyTakeToAllActiveChats отправляет "тему дня" во все активные чаты
func (b *Bot) sendDailyTakeToAllActiveChats() {
	b.settingsMutex.RLock()
	// Копируем ID активных чатов, чтобы не держать мьютекс во время отправки
	activeChatIDs := make([]int64, 0)
	for chatID, settings := range b.chatSettings {
		if settings.Active {
			activeChatIDs = append(activeChatIDs, chatID)
		}
	}
	b.settingsMutex.RUnlock()

	if len(activeChatIDs) == 0 {
		log.Println("Нет активных чатов для отправки тейка.")
		return
	}

	log.Printf("Генерация тейка дня для %d чатов...", len(activeChatIDs))
	takePrompt := b.config.DailyTakePrompt
	dailyTake, err := b.gemini.GenerateArbitraryResponse(takePrompt, "") // Без контекста
	if err != nil {
		log.Printf("Ошибка генерации тейка дня: %v", err)
		return
	}

	log.Printf("Тейк дня сгенерирован: \"%s\"", dailyTake)
	for _, chatID := range activeChatIDs {
		b.sendReply(chatID, "🔥 Тема дня 🔥\n\n"+dailyTake)
		time.Sleep(1 * time.Second) // Небольшая задержка между отправками
	}
	log.Println("Тейк дня отправлен во все активные чаты.")
}

// schedulePeriodicSummary запускает периодическую генерацию саммари
func (b *Bot) schedulePeriodicSummary() {
	// Используем общий ticker, проверяем интервал для каждого чата индивидуально
	// Частота тикера может быть, например, раз в час или чаще
	tickerInterval := 1 * time.Hour // Проверяем каждый час
	log.Printf("Запуск планировщика автоматического саммари с интервалом проверки %v...", tickerInterval)
	ticker := time.NewTicker(tickerInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			b.checkAndSendAutoSummaries()
		case <-b.stop:
			log.Println("Остановка планировщика авто-саммари.")
			return
		}
	}
}

// checkAndSendAutoSummaries проверяет и отправляет авто-саммари для чатов
func (b *Bot) checkAndSendAutoSummaries() {
	b.settingsMutex.RLock()
	// Копируем настройки чатов для проверки
	chatsToCheck := make(map[int64]*ChatSettings)
	for id, s := range b.chatSettings {
		// Копируем структуру, чтобы избежать гонки при чтении/записи
		copiedSettings := *s
		chatsToCheck[id] = &copiedSettings
	}
	b.settingsMutex.RUnlock()

	now := time.Now()
	for chatID, settings := range chatsToCheck {
		if settings.Active && settings.SummaryIntervalHours > 0 {
			intervalDuration := time.Duration(settings.SummaryIntervalHours) * time.Hour
			// Проверяем, прошло ли достаточно времени с последнего авто-саммари
			if settings.LastAutoSummaryTime.IsZero() || now.Sub(settings.LastAutoSummaryTime) >= intervalDuration {
				log.Printf("Авто-саммари для чата %d (интервал %d ч). Последнее: %v", chatID, settings.SummaryIntervalHours, settings.LastAutoSummaryTime)

				// Обновляем время ДО запуска генерации, чтобы избежать повторной отправки при ошибке
				b.settingsMutex.Lock()
				// Перепроверяем существование чата на случай удаления
				if currentSettings, ok := b.chatSettings[chatID]; ok {
					currentSettings.LastAutoSummaryTime = now
					b.settingsMutex.Unlock()            // Разблокируем перед генерацией
					go b.generateAndSendSummary(chatID) // Запускаем генерацию
				} else {
					b.settingsMutex.Unlock()
					log.Printf("Чат %d был удален, пропускаем авто-саммари.", chatID)
				}
			}
		}
	}
}

// generateAndSendSummary генерирует и отправляет саммари чата
func (b *Bot) generateAndSendSummary(chatID int64) {
	log.Printf("Генерация саммари для чата %d", chatID)
	// Получаем сообщения за последние 24 часа
	since := time.Now().Add(-24 * time.Hour)
	messages := b.storage.GetMessagesSince(chatID, since)

	if len(messages) < 5 { // Не генерируем саммари, если сообщений мало
		log.Printf("Слишком мало сообщений (%d) для саммари в чате %d", len(messages), chatID)
		// Можно отправить сообщение "Недостаточно сообщений для саммари"
		return
	}

	// Преобразуем сообщения в текст для Gemini
	var contextText strings.Builder
	for _, msg := range messages {
		contextText.WriteString(formatMessageForAnalysis(msg)) // Используем ту же функцию форматирования
		contextText.WriteString("\n")
	}

	summary, err := b.gemini.GenerateArbitraryResponse(b.config.SummaryPrompt, contextText.String())
	if err != nil {
		log.Printf("Ошибка генерации саммари для чата %d: %v", chatID, err)
		b.sendReply(chatID, "Не удалось сгенерировать саммари. Попробуйте позже.")
		return
	}

	b.sendReply(chatID, "📊 Саммари за последние 24 часа 📊\n\n"+summary)
	log.Printf("Саммари успешно отправлено в чат %d", chatID)
}

// --- Управление историей ---

// saveAllChatHistories вызывает метод сохранения у текущего storage implementation
func (b *Bot) saveAllChatHistories() {
	if err := b.storage.SaveAllChatHistories(); err != nil {
		log.Printf("Ошибка при сохранении истории всех чатов: %v", err)
	} else {
		log.Println("История всех чатов успешно сохранена.")
	}
}

// loadChatHistory загружает историю для одного чата (используется при /start или добавлении бота)
func (b *Bot) loadChatHistory(chatID int64) {
	log.Printf("Загрузка истории для чата %d...", chatID)
	_, err := b.storage.LoadChatHistory(chatID) // LoadChatHistory в реализации Local/S3 обновит кеш
	if err != nil {
		log.Printf("Ошибка загрузки истории для чата %d: %v", chatID, err)
	} else {
		log.Printf("История для чата %d успешно загружена (или не найдена).", chatID)
	}
}

// --- Анализ срачей ---

// formatMessageForAnalysis форматирует сообщение для передачи в LLM для анализа
func formatMessageForAnalysis(msg *tgbotapi.Message) string {
	userName := "UnknownUser"
	if msg.From != nil {
		userName = msg.From.UserName
		if userName == "" {
			userName = msg.From.FirstName
		}
	}
	return fmt.Sprintf("%s (%d): %s", userName, msg.From.ID, msg.Text)
}

// isPotentialSrachTrigger проверяет, может ли сообщение быть триггером срача
func (b *Bot) isPotentialSrachTrigger(message *tgbotapi.Message) bool {
	textLower := strings.ToLower(message.Text)

	// 1. Проверка на ключевые слова
	for _, keyword := range b.config.SrachKeywords {
		if strings.Contains(textLower, keyword) {
			log.Printf("[Srach Detect] Чат %d: Найдено ключевое слово '%s' в сообщении ID %d", message.Chat.ID, keyword, message.MessageID)
			return true
		}
	}

	// 2. Проверка на ответ (reply)
	if message.ReplyToMessage != nil {
		log.Printf("[Srach Detect] Чат %d: Обнаружен ответ (reply) в сообщении ID %d", message.Chat.ID, message.MessageID)
		return true
	}

	// 3. Проверка на упоминание (mention) другого пользователя (не бота)
	if message.Entities != nil {
		for _, entity := range message.Entities {
			if entity.Type == "mention" {
				mention := message.Text[entity.Offset : entity.Offset+entity.Length]
				if mention != "@"+b.api.Self.UserName { // Игнорируем упоминания самого бота
					log.Printf("[Srach Detect] Чат %d: Обнаружено упоминание '%s' в сообщении ID %d", message.Chat.ID, mention, message.MessageID)
					return true
				}
			}
		}
	}

	return false
}

// sendSrachWarning отправляет предупреждение о начале срача
func (b *Bot) sendSrachWarning(chatID int64) {
	// Отправляем текст напрямую из конфигурации
	if b.config.SRACH_WARNING_PROMPT != "" {
		b.sendReply(chatID, b.config.SRACH_WARNING_PROMPT)
	} else {
		// Отправляем статичное сообщение по умолчанию, если в конфиге пусто
		log.Printf("[WARN] Чат %d: SRACH_WARNING_PROMPT не задан в конфигурации, используется стандартное сообщение.", chatID)
		b.sendReply(chatID, "🚨 Внимание! Обнаружен потенциальный срач! 🚨")
	}
}

// confirmSrachWithLLM проверяет сообщение через LLM на принадлежность к срачу
func (b *Bot) confirmSrachWithLLM(chatID int64, messageText string) bool {
	log.Printf("[DEBUG] Чат %d: Запуск LLM для подтверждения срача. Сообщение: \"%s...\"", chatID, truncateString(messageText, 20))
	prompt := b.config.SRACH_CONFIRM_PROMPT + " " + messageText // Добавляем само сообщение к промпту

	response, err := b.gemini.GenerateArbitraryResponse(prompt, "") // Используем Arbitrary без доп. контекста
	if err != nil {
		log.Printf("[LLM Srach Confirm ERROR] Чат %d: Ошибка генерации ответа LLM: %v", chatID, err)
		return false // Считаем, что не срач, если ошибка
	}

	// Очищаем ответ и проверяем на "true"
	cleanResponse := strings.TrimSpace(strings.ToLower(response))
	isConfirmed := cleanResponse == "true"
	log.Printf("[DEBUG] Чат %d: Результат LLM подтверждения срача: %t (ответ LLM: \"%s\")", chatID, isConfirmed, strings.TrimSpace(response))
	return isConfirmed
}

// analyseSrach анализирует собранные сообщения срача
func (b *Bot) analyseSrach(chatID int64) {
	b.settingsMutex.Lock()
	settings, exists := b.chatSettings[chatID]
	if !exists || settings.SrachState != "detected" {
		// Срач уже анализируется или был сброшен
		b.settingsMutex.Unlock()
		return
	}
	// Копируем сообщения и меняем статус перед разблокировкой
	messagesToAnalyse := make([]string, len(settings.SrachMessages))
	copy(messagesToAnalyse, settings.SrachMessages)
	settings.SrachState = "analyzing" // Меняем статус
	b.settingsMutex.Unlock()

	// Уведомляем чат о начале анализа
	analysisNotification, err := b.gemini.GenerateArbitraryResponse(b.config.SRACH_ANALYSIS_PROMPT, "")
	if err != nil {
		log.Printf("[Srach Analysis Start ERROR] Чат %d: Ошибка генерации уведомления об анализе: %v", chatID, err)
		b.sendReply(chatID, "🔍 Начинаю анализ прошедшего срача...")
	} else {
		b.sendReply(chatID, analysisNotification)
	}

	// Готовим контекст для LLM
	contextText := "История сообщений конфликта:\n" + strings.Join(messagesToAnalyse, "\n")

	// Запускаем LLM для анализа
	analysisResult, err := b.gemini.GenerateArbitraryResponse(b.config.SRACH_ANALYSIS_PROMPT, contextText)
	if err != nil {
		log.Printf("[Srach Analysis ERROR] Чат %d: Ошибка генерации результата анализа: %v", chatID, err)
		b.sendReply(chatID, "Не удалось проанализировать срач. Слишком сложно.")
	} else {
		b.sendReply(chatID, "📜 Результаты анализа срача 📜\n\n"+analysisResult)
		log.Printf("[Srach Analysis OK] Чат %d: Анализ завершен и отправлен.", chatID)
	}

	// Сбрасываем состояние срача после анализа
	b.settingsMutex.Lock()
	if settings, ok := b.chatSettings[chatID]; ok {
		settings.SrachState = "none"
		settings.SrachMessages = nil
		settings.SrachStartTime = time.Time{}
		settings.LastSrachTriggerTime = time.Time{}
		settings.SrachLlmCheckCounter = 0
	}
	b.settingsMutex.Unlock()
}

// --- Вспомогательные функции ---

// truncateString обрезает строку до указанной длины
func truncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	// Обрезаем по рунам, чтобы не повредить многобайтовые символы
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	return string(runes[:maxLen])
}

// --- Вспомогательные функции отправки сообщений (ВОССТАНОВЛЕНО) ---

// sendReply отправляет простое текстовое сообщение в чат
func (b *Bot) sendReply(chatID int64, text string) {
	msg := tgbotapi.NewMessage(chatID, text)
	if _, err := b.api.Send(msg); err != nil {
		log.Printf("Ошибка отправки сообщения в чат %d: %v", chatID, err)
	}
}

// sendReplyWithKeyboard отправляет сообщение с клавиатурой
func (b *Bot) sendReplyWithKeyboard(chatID int64, text string, keyboard tgbotapi.InlineKeyboardMarkup) {
	msg := tgbotapi.NewMessage(chatID, text)
	msg.ReplyMarkup = keyboard
	if _, err := b.api.Send(msg); err != nil {
		log.Printf("Ошибка отправки сообщения с клавиатурой в чат %d: %v", chatID, err)
	}
}
