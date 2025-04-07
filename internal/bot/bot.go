package bot

import (
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/Henry-Case-dev/rofloslav/internal/config"
	"github.com/Henry-Case-dev/rofloslav/internal/deepseek"
	"github.com/Henry-Case-dev/rofloslav/internal/gemini"
	"github.com/Henry-Case-dev/rofloslav/internal/llm"
	"github.com/Henry-Case-dev/rofloslav/internal/storage"
	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// Bot представляет Telegram бота
type Bot struct {
	api                *tgbotapi.BotAPI
	llm                llm.LLMClient
	storage            *storage.Storage
	config             *config.Config
	chatSettings       map[int64]*ChatSettings
	settingsMutex      sync.RWMutex
	stop               chan struct{}
	summaryMutex       sync.RWMutex
	lastSummaryRequest map[int64]time.Time
	autoSummaryTicker  *time.Ticker
	bot                *Bot
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

	// --- Инициализация LLM клиента ---
	var llmClient llm.LLMClient // Используем тип интерфейса
	var llmErr error

	log.Printf("Выбранный LLM провайдер: %s", cfg.LLMProvider)

	switch cfg.LLMProvider {
	case config.ProviderGemini:
		llmClient, llmErr = gemini.New(cfg.GeminiAPIKey, cfg.GeminiModelName, cfg.Debug)
		if llmErr != nil {
			return nil, fmt.Errorf("ошибка инициализации Gemini Client: %w", llmErr)
		}
	case config.ProviderDeepSeek:
		llmClient, llmErr = deepseek.New(cfg.DeepSeekAPIKey, cfg.DeepSeekModelName, cfg.DeepSeekBaseURL, cfg.Debug)
		if llmErr != nil {
			return nil, fmt.Errorf("ошибка инициализации DeepSeek Client: %w", llmErr)
		}
	default:
		// По идее, сюда не должны попасть из-за валидации в config.Load, но на всякий случай
		return nil, fmt.Errorf("неизвестный LLM провайдер в конфигурации: %s", cfg.LLMProvider)
	}
	log.Println("--- LLM Client Initialized ---")
	// --- Конец инициализации LLM клиента ---

	storage := storage.New(cfg.ContextWindow, true) // Включаем автосохранение истории

	bot := &Bot{
		api:                api,
		llm:                llmClient,
		storage:            storage,
		config:             cfg,
		chatSettings:       make(map[int64]*ChatSettings),
		stop:               make(chan struct{}),
		lastSummaryRequest: make(map[int64]time.Time),
		// autoSummaryTicker инициализируется в schedulePeriodicSummary
	}

	// ПРИСВАИВАЕМ bot *после* создания структуры
	bot.bot = bot

	// Загрузка истории для всех чатов при старте (опционально, может занять время)
	// bot.loadAllChatHistoriesOnStart() // Раскомментируйте, если нужно

	// --- ПЕРЕМЕЩЕННЫЙ БЛОК: Запуск планировщиков ---
	// Запускаем планировщики *после* создания экземпляра bot
	go bot.scheduleDailyTake(bot.config.DailyTakeTime, bot.config.TimeZone)
	go bot.scheduleHistorySaving()
	go bot.scheduleAutoSummary()
	// --- КОНЕЦ ПЕРЕМЕЩЕННОГО БЛОКА ---

	log.Println("Бот успешно инициализирован (с автосохранением истории)") // Обновляем лог
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

	// Закрываем LLM клиент
	if b.llm != nil { // Проверяем, что клиент был инициализирован
		if err := b.llm.Close(); err != nil {
			log.Printf("Ошибка при закрытии LLM клиента: %v", err)
		}
	}

	// Сохраняем все истории перед выходом (ВОССТАНОВЛЕНО)
	b.saveAllChatHistories()

	log.Println("Бот остановлен.")
}

// ensureChatInitializedAndWelcome проверяет, инициализирован ли чат,
// создает настройки при необходимости и приветствует, если бот только что добавлен.
// Возвращает настройки чата и флаг, нужно ли продолжать обработку обновления.
func (b *Bot) ensureChatInitializedAndWelcome(update tgbotapi.Update) (*ChatSettings, bool) {
	message := update.Message
	if message == nil {
		return nil, false // Не сообщение, пропускаем
	}
	chatID := message.Chat.ID

	// --- Проверка на добавление бота в чат ---
	if message.NewChatMembers != nil {
		for _, member := range message.NewChatMembers {
			if member.ID == b.api.Self.ID {
				log.Printf("Бот добавлен в чат: %d (%s)", chatID, message.Chat.Title)

				// Создаем настройки сразу при добавлении
				b.settingsMutex.Lock()
				settings, exists := b.chatSettings[chatID]
				if !exists {
					settings = &ChatSettings{
						Active:               true,                 // Используем правильное имя поля
						MinMessages:          b.config.MinMessages, // Используем правильное имя поля
						MaxMessages:          b.config.MaxMessages, // Используем правильное имя поля
						DailyTakeTime:        b.config.DailyTakeTime,
						SummaryIntervalHours: b.config.SummaryIntervalHours,
						SrachAnalysisEnabled: false,
						SrachState:           "none", // Используем правильное имя поля
						MessageCount:         0,      // Используем правильное имя поля
					}
					b.chatSettings[chatID] = settings
					log.Printf("Созданы настройки по умолчанию для чата %d при добавлении бота.", chatID)
				}
				b.settingsMutex.Unlock()

				go b.loadChatHistory(chatID) // Загрузка истории
				b.sendReplyWithKeyboard(chatID, "Привет! Я готов к работе. Используйте /settings для настройки.", getMainKeyboard())
				return settings, false // Обработка завершена, приветствие отправлено
			}
		}
	}

	// --- Получение или создание настроек чата для обычных сообщений ---
	b.settingsMutex.Lock()
	settings, exists := b.chatSettings[chatID]
	if !exists {
		settings = &ChatSettings{
			Active:               true,                 // Используем правильное имя поля
			MinMessages:          b.config.MinMessages, // Используем правильное имя поля
			MaxMessages:          b.config.MaxMessages, // Используем правильное имя поля
			DailyTakeTime:        b.config.DailyTakeTime,
			SummaryIntervalHours: b.config.SummaryIntervalHours,
			SrachAnalysisEnabled: false,
			SrachState:           "none", // Используем правильное имя поля
			MessageCount:         0,      // Используем правильное имя поля
		}
		b.chatSettings[chatID] = settings
		log.Printf("Созданы настройки по умолчанию для чата %d (обычное сообщение).", chatID)
	}
	b.settingsMutex.Unlock()

	return settings, true // Настройки получены/созданы, продолжаем обработку
}

// handleUpdate обрабатывает входящие обновления
func (b *Bot) handleUpdate(update tgbotapi.Update) {
	// --- Начальные проверки и извлечение данных ---
	if update.Message == nil && update.CallbackQuery == nil {
		return // Игнорируем обновления без сообщения или колбэка
	}

	var chatID int64
	var message *tgbotapi.Message

	if update.Message != nil {
		message = update.Message
		chatID = message.Chat.ID

		// Игнорируем сообщения от других ботов
		if message.From != nil && message.From.IsBot && message.From.ID != b.api.Self.ID {
			return
		}

		// Игнорируем не-групповые чаты
		if !message.Chat.IsGroup() && !message.Chat.IsSuperGroup() {
			return
		}

		// --- Инициализация чата и проверка настроек ---
		settings, proceed := b.ensureChatInitializedAndWelcome(update)
		if !proceed {
			return // Бот был только что добавлен, обработка не нужна
		}

		// Если бот неактивен в этом чате, но пришла команда /start, обработаем ее
		if !settings.Active && message.IsCommand() && message.Command() == "start" { // Оставляем Active
			b.handleCommand(message) // Вызываем обработчик команд
			return                   // Выходим, т.к. команда обработана
		} else if !settings.Active { // Оставляем Active
			// Если бот не активен и это не /start, игнорируем
			return
		}

		// Сохраняем сообщение ДО остальной обработки
		b.storage.AddMessage(chatID, update.Message)

		// --- Обработка команд ---
		if update.Message.IsCommand() {
			b.handleCommand(update.Message)
			return
		}

		// --- Обработка обычных сообщений (вызов нового хендлера) ---
		b.handleMessage(update)

	} else if update.CallbackQuery != nil {
		// --- Обработка кнопок --- (Вызов перенесенного хендлера)
		b.handleCallback(update.CallbackQuery)
	}
}
