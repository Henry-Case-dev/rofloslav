package bot

import (
	"fmt"
	"log"
	"math/rand"
	"strings"
	"sync"
	"time"

	"github.com/Henry-Case-dev/rofloslav/internal/config"
	"github.com/Henry-Case-dev/rofloslav/internal/deepseek"
	"github.com/Henry-Case-dev/rofloslav/internal/gemini"
	"github.com/Henry-Case-dev/rofloslav/internal/llm"
	"github.com/Henry-Case-dev/rofloslav/internal/storage"
	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	// Импортируем драйвер postgres, но не используем его напрямую здесь
	_ "github.com/lib/pq"
)

// Bot структура
type Bot struct {
	api                *tgbotapi.BotAPI
	llm                llm.LLMClient
	storage            storage.ChatHistoryStorage // Используем интерфейс
	config             *config.Config
	chatSettings       map[int64]*ChatSettings
	settingsMutex      sync.RWMutex
	stop               chan struct{}
	summaryMutex       sync.RWMutex
	lastSummaryRequest map[int64]time.Time
	autoSummaryTicker  *time.Ticker // Оставляем для авто-саммари
	randSource         *rand.Rand   // Источник случайных чисел
}

// New создает и инициализирует новый экземпляр бота
func New(cfg *config.Config) (*Bot, error) {
	log.Println("Инициализация Telegram API...")
	api, err := tgbotapi.NewBotAPI(cfg.TelegramToken)
	if err != nil {
		return nil, fmt.Errorf("ошибка инициализации Telegram API: %w", err)
	}

	api.Debug = cfg.Debug // Используем Debug флаг из конфига
	log.Printf("Авторизован как @%s", api.Self.UserName)
	if api.Debug {
		log.Println("Режим отладки Telegram API включен.")
	} else {
		log.Println("Режим отладки Telegram API выключен.")
	}

	// Инициализация LLM клиента
	log.Printf("Инициализация LLM клиента: %s", cfg.LLMProvider)
	var llmClient llm.LLMClient
	switch cfg.LLMProvider {
	case config.ProviderGemini:
		llmClient, err = gemini.New(cfg.GeminiAPIKey, cfg.GeminiModelName, cfg.Debug)
	case config.ProviderDeepSeek:
		llmClient, err = deepseek.New(cfg.DeepSeekAPIKey, cfg.DeepSeekModelName, cfg.DeepSeekBaseURL, cfg.Debug)
	default:
		return nil, fmt.Errorf("неизвестный LLM провайдер: %s", cfg.LLMProvider)
	}
	if err != nil {
		return nil, fmt.Errorf("ошибка инициализации LLM клиента %s: %w", cfg.LLMProvider, err)
	}
	log.Println("LLM клиент успешно инициализирован.")

	// Инициализация хранилища
	log.Printf("Инициализация хранилища: %s", cfg.StorageType)
	var chatStorage storage.ChatHistoryStorage
	switch cfg.StorageType {
	case config.StorageTypePostgres:
		log.Println("Попытка инициализации PostgreSQL хранилища...")
		pgStorage, pgErr := storage.NewPostgresStorage(
			cfg.PostgresqlHost,
			cfg.PostgresqlPort,
			cfg.PostgresqlUser,
			cfg.PostgresqlPassword,
			cfg.PostgresqlDbname,
			cfg.ContextWindow,
			cfg.Debug,
		)
		if pgErr != nil {
			log.Printf("[WARN] Ошибка инициализации PostgreSQL хранилища: %v. Переключение на файловое хранилище.", pgErr)
			// Fallback на файловое хранилище
			chatStorage = storage.NewFileStorage(cfg.ContextWindow, true) // true для autoSave, т.к. это fallback
			log.Println("Используется файловое хранилище (fallback).")
		} else {
			chatStorage = pgStorage
			log.Println("Хранилище PostgreSQL успешно инициализировано.")
		}
	case config.StorageTypeMongo:
		log.Println("Попытка инициализации MongoDB хранилища...")
		mongoStorage, mongoErr := storage.NewMongoStorage(
			cfg.MongoDbURI,
			cfg.MongoDbName,
			cfg.MongoDbMessagesCollection,
			cfg.MongoDbUserProfilesCollection,
			cfg,
		)
		if mongoErr != nil {
			log.Printf("[WARN] Ошибка инициализации MongoDB хранилища: %v. Переключение на файловое хранилище.", mongoErr)
			// Fallback на файловое хранилище
			chatStorage = storage.NewFileStorage(cfg.ContextWindow, true)
			log.Println("Используется файловое хранилище (fallback).")
		} else {
			chatStorage = mongoStorage
			log.Println("Хранилище MongoDB успешно инициализировано.")
		}
	case config.StorageTypeFile:
		log.Println("Используется файловое хранилище.")
		chatStorage = storage.NewFileStorage(cfg.ContextWindow, true) // true для autoSave
	default:
		log.Printf("[WARN] Неизвестный тип хранилища '%s', используется файловое хранилище по умолчанию.", cfg.StorageType)
		chatStorage = storage.NewFileStorage(cfg.ContextWindow, true) // true для autoSave
	}

	// Загрузка настроек чатов
	chatSettings, loadErr := loadAllChatSettings()
	if loadErr != nil {
		// Не фатальная ошибка, просто начинаем с настройками по умолчанию
		log.Printf("[WARN] Ошибка загрузки сохраненных настроек чатов: %v. Будут использоваться настройки по умолчанию.", loadErr)
		chatSettings = make(map[int64]*ChatSettings)
	}
	log.Printf("Загружено %d наборов настроек чатов.", len(chatSettings))

	// Создание экземпляра бота
	b := &Bot{
		api:                api,
		llm:                llmClient,
		storage:            chatStorage, // Назначаем выбранное хранилище
		config:             cfg,
		chatSettings:       chatSettings,
		settingsMutex:      sync.RWMutex{},
		stop:               make(chan struct{}),
		summaryMutex:       sync.RWMutex{},
		lastSummaryRequest: make(map[int64]time.Time),
		randSource:         rand.New(rand.NewSource(time.Now().UnixNano())),
	}

	log.Println("Инициализация бота завершена.")
	return b, nil
}

// Start запускает основного бота
func (b *Bot) Start() error {
	log.Println("Запуск бота...")
	b.stop = make(chan struct{}) // Пересоздаем канал при старте

	// Загрузка истории для существующих чатов при старте
	log.Println("Начинаю загрузку истории для существующих чатов...")
	// Загрузка ID чатов из хранилища
	chatIDsToLoad, err := b.storage.GetAllChatIDs()
	if err != nil {
		log.Printf("[ERROR] Не удалось получить список ChatID из хранилища для загрузки истории: %v", err)
	} else {
		log.Printf("Найдено %d чатов в хранилище для загрузки истории.", len(chatIDsToLoad))
		for _, chatID := range chatIDsToLoad {
			// Проверяем, есть ли настройки для этого чата (могли быть удалены)
			b.settingsMutex.RLock()
			_, settingsExist := b.chatSettings[chatID]
			b.settingsMutex.RUnlock()
			if settingsExist {
				go b.loadChatHistory(chatID) // Запускаем загрузку только если есть настройки
			} else {
				log.Printf("[WARN] Пропуск загрузки истории для чата %d: настройки не найдены.", chatID)
			}
		}
		log.Printf("Запущена фоновая загрузка истории для %d чатов (с существующими настройками).", len(chatIDsToLoad))
	}

	// Запуск планировщиков
	go b.scheduleDailyTake(b.config.DailyTakeTime, b.config.TimeZone)
	go b.scheduleAutoSummary()

	// Настройка получения обновлений
	u := tgbotapi.NewUpdate(0)
	u.Timeout = 60
	updates := b.api.GetUpdatesChan(u)

	log.Println("Бот начал слушать обновления...")

	// Основной цикл обработки обновлений
	for {
		select {
		case update := <-updates:
			go b.handleUpdate(update)
		case <-b.stop:
			log.Println("Получен сигнал остановки, завершение работы...")
			return nil // Выход из функции Start
		}
	}
}

// Stop gracefully stops the bot
func (b *Bot) Stop() {
	log.Println("Получен сигнал остановки бота...")

	// Сигнализируем всем горутинам о необходимости остановиться
	close(b.stop) // Закрытие канала stop сигнализирует всем слушателям

	// --- Сохранение истории и настроек ---
	// Сохранение истории теперь управляется планировщиком и вызывается при закрытии канала stop
	// saveAllChatHistories() вызывается внутри scheduleHistorySaving при получении сигнала <-b.stop

	log.Println("Сохранение всех настроек чатов...")
	b.settingsMutex.RLock()
	settingsToSave := make(map[int64]*ChatSettings, len(b.chatSettings))
	for id, settings := range b.chatSettings {
		// Копируем настройки, чтобы избежать гонки данных при сохранении в горутинах (если бы оно было)
		copiedSettings := *settings
		settingsToSave[id] = &copiedSettings
	}
	b.settingsMutex.RUnlock()

	var wg sync.WaitGroup
	for chatID, settings := range settingsToSave {
		wg.Add(1)
		go func(cid int64, s *ChatSettings) {
			defer wg.Done()
			if err := saveChatSettings(cid, s); err != nil { // Вызываем как обычную функцию
				log.Printf("Ошибка сохранения настроек для чата %d при остановке: %v", cid, err)
			}
		}(chatID, settings)
	}
	wg.Wait()
	log.Println("Сохранение настроек чатов завершено.")

	// Закрываем LLM клиент
	if b.llm != nil {
		log.Println("Закрытие LLM клиента...")
		if err := b.llm.Close(); err != nil {
			log.Printf("Ошибка при закрытии LLM клиента: %v", err)
		} else {
			log.Println("LLM клиент успешно закрыт.")
		}
	}

	// Закрываем хранилище (важно для PostgreSQL)
	if b.storage != nil {
		log.Println("Закрытие хранилища...")
		if err := b.storage.Close(); err != nil {
			log.Printf("Ошибка при закрытии хранилища: %v", err)
		} else {
			log.Println("Хранилище успешно закрыто.")
		}
	}

	log.Println("Бот успешно остановлен.")
}

// ensureChatInitializedAndWelcome проверяет, инициализирован ли чат, и отправляет приветственное сообщение при необходимости.
// Возвращает текущие настройки чата и флаг, был ли чат только что инициализирован.
func (b *Bot) ensureChatInitializedAndWelcome(update tgbotapi.Update) (*ChatSettings, bool) {
	var chatID int64
	var chatName string
	if update.Message != nil {
		chatID = update.Message.Chat.ID
		chatName = update.Message.Chat.Title
	} else if update.CallbackQuery != nil {
		chatID = update.CallbackQuery.Message.Chat.ID
		chatName = update.CallbackQuery.Message.Chat.Title
	} else {
		// Неизвестный тип обновления, пропускаем инициализацию
		return nil, false
	}

	// Сначала проверяем с RLock для скорости
	b.settingsMutex.RLock()
	settings, exists := b.chatSettings[chatID]
	b.settingsMutex.RUnlock()

	if !exists {
		var settings *ChatSettings // Объявляем settings здесь
		b.settingsMutex.Lock()     // Блокируем для проверки и возможного создания

		// Повторно проверяем существование чата под полным мьютексом (Double-Checked Locking)
		if _, exists := b.chatSettings[chatID]; exists {
			settings, _ = b.chatSettings[chatID] // Присваиваем существующие настройки
			b.settingsMutex.Unlock()
			return settings, false // Другая горутина могла создать его
		}

		// --- Инициализация нового чата ---
		log.Printf("Чат %d (%s) не найден. Инициализация настроек...", chatID, chatName)
		// Создаем новые настройки для этого чата
		newSettings := &ChatSettings{
			Active:               true,
			MinMessages:          b.config.MinMessages,
			MaxMessages:          b.config.MaxMessages,
			MessageCount:         0,
			DailyTakeTime:        b.config.DailyTakeTime,
			SummaryIntervalHours: b.config.SummaryIntervalHours,
			LastAutoSummaryTime:  time.Time{},                   // Инициализируем пустым временем
			SrachAnalysisEnabled: b.config.SrachAnalysisEnabled, // Берем значение по умолчанию из конфига
			SrachState:           "none",
			SrachMessages:        make([]string, 0),
		}
		b.chatSettings[chatID] = newSettings
		isNewChat := true        // Устанавливаем флаг
		b.settingsMutex.Unlock() // --- ОСВОБОЖДАЕМ МЬЮТЕКС ЗДЕСЬ ---

		log.Printf("Чат %d (%s) успешно инициализирован.", chatID, chatName)

		// --- Действия ПОСЛЕ освобождения мьютекса ---
		if isNewChat {
			// --- Динамическое приветствие --- (Вынесено из-под мьютекса)
			welcomeText := ""
			if b.config.WelcomePrompt != "" {
				generatedWelcome, err := b.llm.GenerateArbitraryResponse(b.config.WelcomePrompt, "")
				if err != nil {
					log.Printf("[WARN][ensureChatInitialized] Chat %d: Ошибка генерации приветствия: %v. Использую стандартное.", chatID, err)
					welcomeText = fmt.Sprintf("Привет, чат %s! Я Рофлослав. Теперь я с вами.", chatName)
				} else {
					welcomeText = generatedWelcome
				}
			} else {
				welcomeText = fmt.Sprintf("Привет, чат %s! Я Рофлослав. Теперь я с вами.", chatName) // Стандартное, если промпт пуст
			}
			b.sendReply(chatID, welcomeText) // Отправляем приветствие

			// --- Загрузка истории (асинхронно) ---
			go b.loadChatHistory(chatID)

			// Отправляем главное меню после приветствия и начала загрузки истории
			b.sendMainMenu(chatID, 0) // 0 т.к. нет старого меню для удаления
		}
		return newSettings, isNewChat // Возвращаем созданные настройки и флаг нового чата
	}

	return settings, false // Чат уже был инициализирован
}

// handleUpdate обрабатывает входящие обновления
func (b *Bot) handleUpdate(update tgbotapi.Update) {
	startTime := time.Now()

	// Проверяем инициализацию чата и отправляем приветствие/загружаем историю при необходимости
	settings, initialized := b.ensureChatInitializedAndWelcome(update)
	if settings == nil {
		// Не удалось определить chatID или обработать обновление
		return
	}
	if initialized {
		// Если чат только что инициализирован, дальнейшая обработка этого обновления не нужна
		return
	}

	// Проверяем, активен ли бот для этого чата (кроме команды /start)
	b.settingsMutex.RLock()
	isActive := settings.Active
	b.settingsMutex.RUnlock()

	isStartCommand := update.Message != nil && update.Message.IsCommand() && update.Message.Command() == "start"
	isSettingsCallback := update.CallbackQuery != nil &&
		(strings.HasPrefix(update.CallbackQuery.Data, "settings") ||
			strings.HasPrefix(update.CallbackQuery.Data, "change_") ||
			strings.HasPrefix(update.CallbackQuery.Data, "toggle_") ||
			update.CallbackQuery.Data == "back_to_main")

	if !isActive && !isStartCommand && !isSettingsCallback {
		// Бот неактивен, и это не команда /start и не колбэк настроек - игнорируем
		return
	}

	// Обработка разных типов обновлений
	if update.Message != nil {
		if update.Message.IsCommand() {
			b.handleCommand(update.Message)
		} else {
			b.handleMessage(update) // Передаем весь update в handleMessage
		}
	} else if update.CallbackQuery != nil {
		b.handleCallback(update.CallbackQuery)
	}

	// Логируем время обработки, если включен Debug
	if b.config.Debug {
		duration := time.Since(startTime)
		var updateType string
		var updateID int
		if update.Message != nil {
			updateType = "Message"
			updateID = update.Message.MessageID
		} else if update.CallbackQuery != nil {
			updateType = "Callback"
			updateID = update.CallbackQuery.Message.MessageID // Используем ID исходного сообщения
		} else {
			updateType = "Unknown"
			updateID = update.UpdateID
		}
		log.Printf("[DEBUG][Timing] Обработка %s (ID: %d) заняла %s", updateType, updateID, formatDuration(duration))
	}
}
