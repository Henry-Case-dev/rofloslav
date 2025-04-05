package bot

import (
	"fmt"
	"io/ioutil"
	"log"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Henry-Case-dev/rofloslav/internal/config"
	"github.com/Henry-Case-dev/rofloslav/internal/gemini"
	"github.com/Henry-Case-dev/rofloslav/internal/storage"
	"github.com/Henry-Case-dev/rofloslav/internal/types"
	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// Bot представляет основной объект бота
type Bot struct {
	api                *tgbotapi.BotAPI
	gemini             *gemini.Client
	storage            storage.HistoryStorage // Основное хранилище (Qdrant или Local)
	localHistory       *storage.LocalStorage  // Дополнительное локальное хранилище для саммари
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

// New создает и инициализирует новый экземпляр бота
func New(cfg *config.Config) (*Bot, error) {
	// --- Инициализация Telegram API ---
	api, err := tgbotapi.NewBotAPI(cfg.TelegramToken)
	if err != nil {
		return nil, fmt.Errorf("ошибка инициализации Telegram API: %w", err)
	}
	api.Debug = cfg.Debug
	log.Printf("Авторизован как @%s", api.Self.UserName)

	// --- Инициализация Gemini Client ---
	geminiClient, err := gemini.New(cfg.GeminiAPIKey, cfg.GeminiModelName, cfg.Debug)
	if err != nil {
		return nil, fmt.Errorf("ошибка инициализации Gemini Client: %w", err)
	}

	// --- Инициализация Основного Хранилища (Qdrant или Local) ---
	// Используем фабрику, которая вернет Qdrant или Local в зависимости от cfg.StorageType
	// (Предполагаем, что NewHistoryStorage обновится или уже учитывает cfg.StorageType)
	historyStorage, err := storage.NewHistoryStorage(cfg, geminiClient)
	if err != nil {
		return nil, fmt.Errorf("ошибка инициализации основного хранилища: %w", err)
	}
	log.Printf("[Bot New] Основное хранилище (%T) инициализировано.", historyStorage)

	// --- !!! ВСЕГДА Инициализация Дополнительного LocalStorage для Сам мари !!! ---
	localHistoryStorage, err := storage.NewLocalStorage(cfg.ContextWindow) // Используем тот же ContextWindow
	if err != nil {
		// Если LocalStorage не создался, это проблема, так как саммари не будут работать
		log.Printf("[Bot New ERROR] Не удалось создать обязательное LocalStorage для саммари: %v", err)
		return nil, fmt.Errorf("ошибка инициализации локального хранилища для саммари: %w", err)
	}
	log.Printf("[Bot New] Дополнительное LocalStorage для саммари инициализировано.")

	b := &Bot{
		api:                   api,
		gemini:                geminiClient,
		storage:               historyStorage,      // Основное хранилище
		localHistory:          localHistoryStorage, // Дополнительное хранилище
		config:                cfg,
		chatSettings:          make(map[int64]*ChatSettings),
		settingsMutex:         sync.RWMutex{},
		stop:                  make(chan struct{}),
		lastSummaryRequest:    make(map[int64]time.Time),
		summaryMutex:          sync.RWMutex{},
		directReplyTimestamps: make(map[int64]map[int64][]time.Time),
		directReplyMutex:      sync.Mutex{},
		botID:                 api.Self.ID,
	}

	// Загрузка существующих настроек чатов
	// ... (код загрузки настроек без изменений)

	// Запуск планировщика ежедневных тейков
	// ... (код запуска планировщика без изменений)

	// Запуск периодического сохранения истории (если нужно, хотя Qdrant сохраняет сразу)
	// go b.periodicSave() // Возможно, не нужно для Qdrant?

	// Запуск периодической проверки и отправки авто-саммари
	if cfg.SummaryIntervalHours > 0 {
		b.schedulePeriodicSummary()
		log.Printf("Планировщик авто-саммари запущен с интервалом %d час(а).", cfg.SummaryIntervalHours)
	}

	// Запуск импорта старых данных, если включено
	if cfg.ImportOldDataOnStart {
		go b.importOldData(cfg.OldDataDir)
	}

	log.Println("Бот успешно инициализирован.")
	return b, nil
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

// handleUpdate обрабатывает входящие обновления от Telegram
func (b *Bot) handleUpdate(update tgbotapi.Update) {
	// Определяем тип хранилища В НАЧАЛЕ функции, чтобы избежать goto jump
	// var isQdrant bool // УДАЛЕНО - больше не нужно для этой логики
	// if _, ok := b.storage.(*storage.QdrantStorage); ok { // УДАЛЕНО
	// 	isQdrant = true // УДАЛЕНО
	// } // УДАЛЕНО

	// Переменные для логики обычного ответа
	shouldSendRegularResponse := false
	var currentMessageCount, minMessages, maxMessages, triggerResponse int

	// Объявляем переменные ДО блоков с goto, чтобы избежать ошибок
	var mentionsBot bool
	var isDirectReply bool
	var currentPendingSetting string // Для отслеживания ожидания ввода
	var isSrachUpdate bool = false   // Флаг, что обновление связано со срачем

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

	// --- Инициализация/Загрузка настроек чата --- (Используем loadChatSettings)
	settings, err := b.loadChatSettings(chatID)
	if err != nil {
		log.Printf("handleUpdate: Ошибка загрузки/создания настроек для чата %d: %v", chatID, err)
		return // Не можем обработать без настроек
	}
	// --- Конец инициализации ---

	// --- Обработка отмены ввода --- (остается без изменений)
	b.settingsMutex.RLock()
	currentPendingSetting = settings.PendingSetting
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
	if isDirectReply := (message.ReplyToMessage != nil && message.ReplyToMessage.From.ID == b.botID) ||
		mentionsBot; isDirectReply {
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
				settings.SrachStartTime = message.Time()
				settings.SrachMessages = []string{formatMessageForAnalysis(message)}
				settings.LastSrachTriggerTime = message.Time() // Запоминаем время триггера
				settings.SrachLlmCheckCounter = 0              // Сбрасываем счетчик LLM при старте
				// Разблокируем перед отправкой сообщения
				b.settingsMutex.Unlock()
				b.sendSrachWarning(chatID) // Объявляем начало
				log.Printf("Чат %d: Обнаружен потенциальный срач.", chatID)
				goto SaveMessage // Переходим к сохранению
			} else if settings.SrachState == "detected" {
				settings.SrachMessages = append(settings.SrachMessages, formatMessageForAnalysis(message))
				settings.LastSrachTriggerTime = message.Time() // Обновляем время последнего триггера
				settings.SrachLlmCheckCounter++                // Увеличиваем счетчик

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
			if !lastTriggerTime.IsZero() && message.Time().Sub(lastTriggerTime) > srachTimeout {
				log.Printf("Чат %d: Срач считается завершенным по тайм-ауту (%v).", chatID, srachTimeout)
				b.settingsMutex.Unlock()
				go b.analyseSrach(chatID) // Запускаем анализ в горутине
				goto SaveMessage          // Переходим к сохранению
			}
		}
		b.settingsMutex.Unlock() // Разблокируем, если не вышли раньше
	}
	// --- Конец Логики Анализа Срачей ---

	// Если это обновление для срача, переходим к проверке прямого ответа
	if isSrachUpdate {
		log.Printf("[DEBUG] Чат %d: Обновление для срача, переход к CheckDirectReply", chatID) // Убрано updateType
		goto CheckDirectReply
	}

CheckDirectReply:
	// Определяем mentionsBot и isDirectReply здесь
	mentionsBot = false
	if message.Entities != nil { // Добавлена проверка на nil
		for _, entity := range message.Entities {
			if entity.Type == "mention" {
				// Убедимся, что оффсеты не выходят за пределы строки
				if entity.Offset >= 0 && entity.Offset+entity.Length <= len(message.Text) {
					mentionText := message.Text[entity.Offset : entity.Offset+entity.Length]
					if mentionText == "@"+b.api.Self.UserName {
						mentionsBot = true
						break
					}
				}
			}
		}
	}
	isDirectReply = (message.ReplyToMessage != nil && message.ReplyToMessage.From.ID == b.botID) || mentionsBot

	// --- Логика ответа по накоплению сообщений (обычный ответ) ---
	// Отвечаем, только если это НЕ был прямой ответ И НЕ команда/ввод настроек
	// И если включен анализ срачей, только если срач не анализируется
	shouldSendRegularResponse = !isDirectReply && !message.IsCommand() && currentPendingSetting == ""
	if srachEnabled && settings.SrachState == "analyzing" {
		shouldSendRegularResponse = false // Не отвечаем, пока идет анализ срача
	}

	if shouldSendRegularResponse {
		b.settingsMutex.Lock() // Блокируем для инкремента счетчика
		settings.MessageCount++
		settings.LastMessageID = message.MessageID  // Обновляем ID последнего сообщения
		currentMessageCount = settings.MessageCount // Присваиваем значение ранее объявленной переменной
		minMessages = settings.MinMessages
		maxMessages = settings.MaxMessages
		b.settingsMutex.Unlock()

		// Проверяем, нужно ли генерировать ответ
		triggerResponse = 0 // Сбрасываем/инициализируем перед проверкой
		if currentMessageCount >= minMessages {
			if currentMessageCount >= maxMessages {
				triggerResponse = maxMessages
			} else {
				// Генерируем случайное число сообщений в диапазоне [minMessages, maxMessages]
				rand.Seed(time.Now().UnixNano()) // Устанавливаем сид
				triggerResponse = rand.Intn(maxMessages-minMessages+1) + minMessages
			}
		}

		// Если счетчик достиг или превысил порог
		if currentMessageCount >= triggerResponse {
			shouldSendRegularResponse = true
		}

		// Обновляем счетчик в настройках
		settings.MessageCount = currentMessageCount
		// Обновляем ID последнего сообщения (опционально, если используется)
		settings.LastMessageID = message.MessageID

		// Если пора отправлять обычный ответ
		if shouldSendRegularResponse {
			// !!! ВОЗВРАЩАЕМ КАК БЫЛО: Всегда вызываем sendAIResponse, если условия выполнены !!!
			b.sendAIResponse(chatID)  // Вызываем генерацию и отправку ответа
			settings.MessageCount = 0 // Сбрасываем счетчик после ответа
		}
	}
	// --- Конец логики обычного ответа ---

	// --- Логика ответа на прямое обращение/ответ ---
	if isDirectReply {
		// Проверяем лимит на частоту ответов на прямые сообщения для КОНКРЕТНОГО ПОЛЬЗОВАТЕЛЯ
		userID := message.From.ID // ID пользователя, который упомянул или ответил
		canReplyDirectly := true
		b.directReplyMutex.Lock()

		// Проверяем существование карт и среза
		if userTimestamps, chatExists := b.directReplyTimestamps[chatID]; chatExists {
			if timestamps, userExists := userTimestamps[userID]; userExists && len(timestamps) > 0 {
				// Берем последний timestamp из среза
				lastReplyTime := timestamps[len(timestamps)-1]
				if time.Since(lastReplyTime) < b.config.DirectReplyRateLimitWindow {
					canReplyDirectly = false
					log.Printf("[DEBUG] Чат %d, User %d: Пропуск прямого ответа из-за лимита (%v)", chatID, userID, b.config.DirectReplyRateLimitWindow)
				}
			}
		}
		b.directReplyMutex.Unlock()

		if canReplyDirectly {
			log.Printf("[DEBUG] Чат %d, User %d: Обнаружен прямой ответ/упоминание, генерация ответа...", chatID, userID)
			b.sendDirectResponse(chatID, message) // Используем ID последнего сообщения

			// Обновляем время последнего прямого ответа для этого пользователя
			b.directReplyMutex.Lock()
			// Обеспечиваем существование карты для чата
			if _, chatExists := b.directReplyTimestamps[chatID]; !chatExists {
				b.directReplyTimestamps[chatID] = make(map[int64][]time.Time)
			}
			// Обеспечиваем существование карты/среза для пользователя
			if _, userExists := b.directReplyTimestamps[chatID][userID]; !userExists {
				b.directReplyTimestamps[chatID][userID] = make([]time.Time, 0)
			}
			// Добавляем текущее время в срез для пользователя
			b.directReplyTimestamps[chatID][userID] = append(b.directReplyTimestamps[chatID][userID], time.Now())
			b.directReplyMutex.Unlock()
		}
	}
	// --- Конец логики прямого ответа ---

	// Метка SaveMessage: перенесена в самый конец функции, чтобы избежать ошибок goto
SaveMessage:
	// Это место теперь используется только как точка перехода для goto из анализа срачей.
	// Основная логика обработки сообщения завершена выше.

	// Здесь можно оставить код, который должен выполняться *всегда* в конце,
	// например, проверка обновления о входе/выходе бота (если такой код есть).

	// Пример: Проверяем, было ли это обновление о входе бота в чат
	if message.NewChatMembers != nil {
		for _, member := range message.NewChatMembers {
			if member.ID == b.api.Self.ID {
				log.Printf("Бот добавлен в новый чат: %s (ID: %d)", message.Chat.Title, chatID)
				// Можно отправить приветственное сообщение или настройки по умолчанию
				b.sendReplyWithKeyboard(chatID, "Привет! Я Рофлослав. Используйте /settings для настройки.", getMainKeyboard())
				// Обеспечим создание настроек для нового чата
				_, _ = b.loadChatSettings(chatID)
			}
		}
	}

	// Проверка выхода бота из чата
	if message.LeftChatMember != nil && message.LeftChatMember.ID == b.api.Self.ID {
		log.Printf("Бот удален из чата: %s (ID: %d)", message.Chat.Title, chatID)
		// Очищаем историю и настройки для этого чата
		b.storage.ClearChatHistory(chatID)
		b.settingsMutex.Lock()
		delete(b.chatSettings, chatID)
		b.settingsMutex.Unlock()
		b.directReplyMutex.Lock()
		delete(b.directReplyTimestamps, chatID)
		b.directReplyMutex.Unlock()
	}
} // <<< Конец функции handleUpdate >>>

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
		b.sendReplyWithKeyboard(chatID, "Бот активирован. Генерирую случайные ответы. Используйте /settings для настройки.", getMainKeyboard())

	// ДОБАВЛЕНО: Обработка /menu как алиаса для /start
	case "menu":
		settings, _ := b.loadChatSettings(chatID) // Загружаем или создаем настройки
		b.settingsMutex.Lock()
		settings.Active = true // Активируем бота, если он был неактивен
		b.settingsMutex.Unlock()
		// Загружаем историю при старте (если есть и она не загружена)
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
/import_history - Импортировать историю сообщений из файла (только Qdrant)
/help - Показать это сообщение`
		b.sendReply(chatID, helpText)

	case "import_history": // Новая команда
		b.handleImportHistoryCommand(chatID)

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

// sendAIResponse генерирует и отправляет AI ответ на основе истории чата
func (b *Bot) sendAIResponse(chatID int64) {
	log.Printf("[DEBUG] Генерация AI ответа (обычного) для чата %d", chatID)

	// --- Новая логика для контекста ---

	var contextMessages []types.Message
	var lastMessage *tgbotapi.Message

	// 1. Получаем ВСЕ сообщения из ЛОКАЛЬНОГО хранилища (оно содержит актуальную историю)
	localHistoryMessages := b.localHistory.GetMessages(chatID)

	if len(localHistoryMessages) == 0 {
		log.Printf("[DEBUG] sendAIResponse: Нет сообщений в localHistory для генерации ответа в чате %d", chatID)
		return
	}

	// 2. Берем самое последнее сообщение из локальной истории
	lastMessage = localHistoryMessages[len(localHistoryMessages)-1]
	lastMessageText := lastMessage.Text
	if lastMessageText == "" {
		lastMessageText = lastMessage.Caption // Используем caption если текст пустой
	}

	if lastMessageText == "" {
		log.Printf("[DEBUG] sendAIResponse: Последнее сообщение в чате %d пустое, ответ не генерируется.", chatID)
		return
	}

	// 3. Ищем релевантные сообщения в ОСНОВНОМ хранилище (Qdrant)
	//    используя текст ПОСЛЕДНЕГО сообщения как запрос.
	relevantMessages, err := b.storage.FindRelevantMessages(chatID, lastMessageText, b.config.ContextRelevantMessagesCount)
	if err != nil {
		log.Printf("[ERROR] sendAIResponse: Ошибка поиска релевантных сообщений в чате %d: %v", chatID, err)
		// Можно попробовать сгенерировать ответ только на основе локальной истории,
		// но пока просто выходим, чтобы избежать нерелевантного ответа.
		return
	}

	log.Printf("[DEBUG] sendAIResponse: Найдено %d релевантных сообщений в Qdrant для чата %d.", len(relevantMessages), chatID)

	// 4. Формируем КОНЕЧНЫЙ КОНТЕКСТ:
	//    - Сначала релевантные сообщения из Qdrant (они уже в формате types.Message)
	//    - Затем добавляем самое последнее сообщение (конвертировав его в types.Message)
	contextMessages = append(contextMessages, relevantMessages...)

	// Конвертируем последнее сообщение и добавляем, если его еще нет
	lastMessageConverted := convertTgBotMessageToTypesMessage(lastMessage)
	if lastMessageConverted != nil {
		isLastMessageAlreadyPresent := false
		for _, ctxMsg := range contextMessages {
			// Сравниваем по ID и ChatID (хотя ChatID должен быть одинаков)
			if ctxMsg.ID == lastMessageConverted.ID && ctxMsg.ChatID == lastMessageConverted.ChatID {
				isLastMessageAlreadyPresent = true
				break
			}
		}
		if !isLastMessageAlreadyPresent {
			contextMessages = append(contextMessages, *lastMessageConverted)
			log.Printf("[DEBUG] sendAIResponse: Добавлено последнее сообщение (ID: %d) в контекст.", lastMessageConverted.ID)
		}
	}

	// Опционально: Сортируем финальный контекст по Timestamp для лучшей подачи в Gemini
	sort.Slice(contextMessages, func(i, j int) bool {
		return contextMessages[i].Timestamp < contextMessages[j].Timestamp
	})

	// --- Конец новой логики для контекста ---

	if len(contextMessages) == 0 {
		log.Printf("[DEBUG] sendAIResponse: Итоговый контекст для Gemini пуст в чате %d.", chatID)
		return
	}

	// Определяем промпт (используем основной)
	settings, _ := b.loadChatSettings(chatID) // Загружаем настройки для кастомного промпта
	prompt := b.config.DefaultPrompt
	if settings != nil && settings.CustomPrompt != "" {
		prompt = settings.CustomPrompt
		log.Printf("[DEBUG] Чат %d: Используется кастомный промпт.", chatID)
	} else {
		log.Printf("[DEBUG] Чат %d: Используется стандартный промпт.", chatID)
	}

	log.Printf("[DEBUG] Чат %d: Отправка запроса в Gemini с %d сообщениями в контексте.", chatID, len(contextMessages))

	// Отправляем запрос в Gemini
	response, err := b.gemini.GenerateResponse(prompt, contextMessages)
	if err != nil {
		log.Printf("[ERROR] sendAIResponse: Ошибка генерации ответа от Gemini для чата %d: %v", chatID, err)
		// Не отправляем сообщение об ошибке пользователю, чтобы не спамить
		return
	}

	if response != "" {
		log.Printf("[DEBUG] Чат %d: Ответ от Gemini получен, отправка в чат...", chatID)
		b.sendReply(chatID, response)
	} else {
		log.Printf("[DEBUG] Чат %d: Получен пустой ответ от Gemini.", chatID)
	}
}

// sendDirectResponse отправляет ответ AI на прямое упоминание или ответ
func (b *Bot) sendDirectResponse(chatID int64, message *tgbotapi.Message) {
	settings, err := b.loadChatSettings(chatID)
	if err != nil {
		log.Printf("Ошибка загрузки настроек для чата %d: %v", chatID, err)
		return
	}
	if !settings.Active {
		return // Бот неактивен в этом чате
	}

	// userID := message.From.ID // <--- Удаляем неиспользуемую переменную

	// --- Проверка Rate Limit --- (логика остается той же)
	if b.config.DirectReplyRateLimitCount > 0 {
		// ... (проверка и обновление directReplyTimestamps) ...
	}
	// --- Конец Rate Limit --- (логика остается той же)

	prompt := b.config.DirectPrompt

	// Ищем релевантные сообщения, как в sendAIResponse
	relevantLimit := b.config.ContextWindow / 4 // Берем меньше контекста для прямого ответа
	queryText := message.Text
	if queryText == "" && message.Caption != "" {
		queryText = message.Caption
	}
	if queryText == "" {
		queryText = "пустое сообщение" // Запрос по умолчанию, если совсем пусто
	}

	log.Printf("[DEBUG] sendDirectResponse: Поиск релевантных сообщений для чата %d, лимит %d, запрос: '%s...'", chatID, relevantLimit, truncateString(queryText, 50))
	relevantMessages, err := b.storage.FindRelevantMessages(chatID, queryText, relevantLimit)
	if err != nil {
		log.Printf("Ошибка поиска релевантных сообщений для прямого ответа в чате %d: %v. Используем последние %d сообщений.", chatID, err, b.config.ContextWindow/5)
		// Fallback: если поиск не удался, берем недавние сообщения
		messagesTg := b.storage.GetMessages(chatID)
		// Берем только последние несколько для краткого контекста
		startIndex := max(0, len(messagesTg)-b.config.ContextWindow/5)
		relevantMessages = convertTgBotMessagesToTypesMessages(messagesTg[startIndex:])
	} else {
		log.Printf("[DEBUG] sendDirectResponse: Найдено %d релевантных сообщений для чата %d", len(relevantMessages), chatID)
	}

	// Создаем types.Message из текущего запроса пользователя
	currentUserMessage := convertTgBotMessageToTypesMessage(message)
	if currentUserMessage == nil {
		log.Printf("[ERROR] sendDirectResponse: Не удалось конвертировать текущее сообщение (ID: %d) в types.Message для чата %d", message.MessageID, chatID)
		return // Не можем продолжить без текущего сообщения
	}

	// Собираем полный контекст: отсортированные релевантные + текущее
	contextMessages := make([]types.Message, 0, len(relevantMessages)+1)
	contextMessages = append(contextMessages, relevantMessages...)

	// Добавляем текущее сообщение, если его еще нет
	foundCurrent := false
	for _, msg := range contextMessages {
		if msg.ID == currentUserMessage.ID {
			foundCurrent = true
			break
		}
	}
	if !foundCurrent {
		contextMessages = append(contextMessages, *currentUserMessage)
	}

	// Сортируем по Timestamp
	sort.Slice(contextMessages, func(i, j int) bool {
		return contextMessages[i].Timestamp < contextMessages[j].Timestamp // Исправление: сравниваем int
	})

	if b.config.Debug {
		log.Printf("[DEBUG] Генерация прямого ответа AI для чата %d", chatID)
		log.Printf("[DEBUG] Используется промпт: %s", prompt)
		log.Printf("[DEBUG] Количество сообщений в контексте: %d", len(contextMessages))
	}

	response, err := b.gemini.GenerateResponse(prompt, contextMessages)
	if err != nil {
		log.Printf("Ошибка генерации прямого ответа AI для чата %d: %v", chatID, err)
		return
	}

	if response != "" {
		// Отвечаем с reply на исходное сообщение пользователя
		replyMsg := tgbotapi.NewMessage(chatID, response)
		replyMsg.ReplyToMessageID = message.MessageID
		_, err = b.api.Send(replyMsg)
		if err != nil {
			log.Printf("Ошибка отправки прямого ответа в чат %d: %v", chatID, err)
		}
	}
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

// generateAndSendSummary генерирует и отправляет саммари для чата
func (b *Bot) generateAndSendSummary(chatID int64) {
	log.Printf("[Summary] Запрос на генерацию саммари для чата %d", chatID)

	// Получаем сообщения за последние 24 часа из ЛОКАЛЬНОГО хранилища
	cutoffTime := time.Now().Add(-24 * time.Hour)
	// !!! ИЗМЕНЕНИЕ: Используем b.localHistory !!!
	messages := b.localHistory.GetMessagesSince(chatID, cutoffTime)

	if len(messages) == 0 {
		log.Printf("[Summary] Чат %d: Нет сообщений за последние 24 часа для саммари.", chatID)
		b.sendReply(chatID, "Недостаточно сообщений за последние 24 часа для создания саммари.")
		return
	}

	log.Printf("[Summary] Чат %d: Найдено %d сообщений для саммари. Отправка в Gemini...", chatID, len(messages))

	// Конвертируем сообщения в формат для Gemini (types.Message)
	// Так как localHistory хранит []*tgbotapi.Message, нужна конвертация
	var contextMessages []types.Message
	for _, msg := range messages {
		convertedMsg := convertTgBotMessageToTypesMessage(msg)
		if convertedMsg != nil {
			contextMessages = append(contextMessages, *convertedMsg)
		}
	}

	if len(contextMessages) == 0 {
		log.Printf("[Summary ERROR] Чат %d: Не удалось конвертировать сообщения для Gemini.", chatID)
		b.sendReply(chatID, "Произошла внутренняя ошибка при подготовке данных для саммари.")
		return
	}

	// Формируем промпт для саммари, включая историю
	// Используем GenerateResponse, передавая промпт саммари как 'systemPrompt'
	// и сообщения как историю. Gemini сам разберется.
	response, err := b.gemini.GenerateResponse(b.config.SummaryPrompt, contextMessages)
	if err != nil {
		log.Printf("[Summary ERROR] Чат %d: Ошибка генерации саммари от Gemini: %v", chatID, err)
		b.sendReply(chatID, "Не удалось сгенерировать саммари. Попробуйте позже.")
		return
	}

	log.Printf("[Summary] Чат %d: Саммари успешно сгенерировано. Отправка в чат...", chatID)
	b.sendReply(chatID, response)
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

// --- НОВЫЕ ФУНКЦИИ ДЛЯ ИМПОРТА --- //

// importOldData сканирует директорию и импортирует JSON файлы в Qdrant
func (b *Bot) importOldData(dataDir string) {
	log.Printf("[Import] Начало сканирования директории '%s' для импорта старых данных...", dataDir)

	files, err := ioutil.ReadDir(dataDir)
	if err != nil {
		log.Printf("[Import ERROR] Не удалось прочитать директорию '%s': %v", dataDir, err)
		return
	}

	var wg sync.WaitGroup
	importedFiles := 0

	for _, file := range files {
		if !file.IsDir() && filepath.Ext(file.Name()) == ".json" {
			fileName := file.Name()
			filePath := filepath.Join(dataDir, fileName)

			// Извлекаем chatID из имени файла
			baseName := strings.TrimSuffix(fileName, ".json")
			chatID, err := strconv.ParseInt(baseName, 10, 64)
			if err != nil {
				log.Printf("[Import WARN] Не удалось извлечь chatID из имени файла '%s'. Пропускаем. Ошибка: %v", fileName, err)
				continue
			}

			log.Printf("[Import] Найден файл '%s' для импорта в чат %d", filePath, chatID)
			importedFiles++
			wg.Add(1)

			// Запускаем импорт для каждого файла в отдельной горутине, чтобы не блокировать друг друга
			go func(fp string, cid int64) {
				defer wg.Done()
				imported, skipped, importErr := b.storage.ImportMessagesFromJSONFile(cid, fp) // <--- Исправляем порядок аргументов
				if importErr != nil {
					log.Printf("[Import ERROR] Ошибка импорта файла %s для чата %d: %v", fp, cid, importErr)
				} else {
					log.Printf("[Import OK] Файл '%s' для чата %d: импортировано %d, пропущено %d сообщений.", fp, cid, imported, skipped)
				}
			}(filePath, chatID)
		}
	}

	if importedFiles == 0 {
		log.Printf("[Import] В директории '%s' не найдено файлов .json для импорта.", dataDir)
	}

	wg.Wait() // Ждем завершения всех горутин импорта
	log.Printf("[Import] Завершено сканирование и попытки импорта из директории '%s'. Обработано файлов: %d.", dataDir, importedFiles)
}

// --- КОНЕЦ ФУНКЦИЙ ИМПОРТА --- //

// --- ВСПОМОГАТЕЛЬНЫЕ ФУНКЦИИ КОНВЕРТАЦИИ --- //

// convertTgBotMessageToTypesMessage конвертирует одно *tgbotapi.Message в *types.Message
func convertTgBotMessageToTypesMessage(msg *tgbotapi.Message) *types.Message {
	if msg == nil {
		return nil
	}

	text := msg.Text
	if text == "" && msg.Caption != "" {
		text = msg.Caption // Используем подпись, если текст пуст
	}
	if text == "" {
		// Если и текст, и подпись пусты, пропускаем (или возвращаем nil)
		return nil
	}

	// Определяем роль (простая логика)
	role := "user" // По умолчанию
	if msg.From != nil && msg.From.IsBot {
		// Если сообщение от другого бота, считаем его 'model' или пропускаем?
		// Пока считаем 'user', т.к. нам важен диалог с основным ботом.
		// Можно добавить логику проверки ID бота, если это Rofloslav
		// if msg.From.ID == b.botID { role = "model" }
	}
	// Как определить, что это ответ нашего бота? Нужно сравнивать ID.
	// b.botID не доступен здесь статически. Передавать его?
	// Пока оставляем простую логику: не-боты = user.

	return &types.Message{
		ID:        int64(msg.MessageID),
		Timestamp: int(msg.Time().Unix()), // Исправление: конвертируем в int(Unix)
		Role:      role,
		Text:      text,
		// Embedding не заполняем здесь
	}
}

// convertTgBotMessagesToTypesMessages конвертирует срез []*tgbotapi.Message в []types.Message
func convertTgBotMessagesToTypesMessages(tgMessages []*tgbotapi.Message) []types.Message {
	typesMessages := make([]types.Message, 0, len(tgMessages))
	for _, tgMsg := range tgMessages {
		converted := convertTgBotMessageToTypesMessage(tgMsg)
		if converted != nil {
			typesMessages = append(typesMessages, *converted)
		}
	}
	return typesMessages
}

// --- КОНЕЦ ВСПОМОГАТЕЛЬНЫХ ФУНКЦИЙ --- //

// --- НОВАЯ ФУНКЦИЯ: Обработчик команды /import_history ---
func (b *Bot) handleImportHistoryCommand(chatID int64) {
	// Проверяем, используется ли Qdrant
	qStorage, ok := b.storage.(*storage.QdrantStorage)
	if !ok {
		b.sendReply(chatID, "Команда импорта доступна только при использовании хранилища Qdrant.")
		return
	}

	// Формируем путь к файлу
	// Ожидаем файл в <DataDir>/old/<chatID>.json
	fileName := fmt.Sprintf("%d.json", chatID)
	filePath := filepath.Join(b.config.DataDir, "old", fileName)

	log.Printf("[ImportCmd] Чат %d: Попытка импорта из файла: %s", chatID, filePath)

	// Проверяем, существует ли файл
	_, err := os.Stat(filePath)
	if os.IsNotExist(err) {
		log.Printf("[ImportCmd WARN] Чат %d: Файл для импорта не найден: %s", chatID, filePath)
		b.sendReply(chatID, fmt.Sprintf("Файл истории '%s' не найден. Убедитесь, что он находится в папке '%s/old/'", fileName, b.config.DataDir))
		return
	} else if err != nil {
		log.Printf("[ImportCmd ERROR] Чат %d: Ошибка проверки файла %s: %v", chatID, filePath, err)
		b.sendReply(chatID, "Произошла ошибка при доступе к файлу истории.")
		return
	}

	// Запускаем импорт в горутине
	b.sendReply(chatID, fmt.Sprintf("Начинаю импорт истории из файла '%s'. Это может занять некоторое время...", fileName))
	go func() {
		startTime := time.Now()
		importedCount, skippedCount, importErr := qStorage.ImportMessagesFromJSONFile(chatID, filePath)
		duration := time.Since(startTime)

		if importErr != nil {
			log.Printf("[ImportCmd ERROR Result] Чат %d: Ошибка импорта из %s: %v", chatID, filePath, importErr)
			b.sendReply(chatID, fmt.Sprintf("Ошибка во время импорта из '%s': %v", fileName, importErr))
		} else {
			log.Printf("[ImportCmd OK Result] Чат %d: Импорт из %s завершен за %v. Импортировано: %d, Пропущено: %d", chatID, filePath, duration, importedCount, skippedCount)
			b.sendReply(chatID, fmt.Sprintf("Импорт из '%s' завершен за %s.\nИмпортировано/Обновлено: %d\nПропущено (дубликаты/ошибки): %d", fileName, duration.Round(time.Second), importedCount, skippedCount))
		}
	}()
}

// --- Конец новой функции ---
