package bot

import (
	"fmt"
	"log"
	"math/rand"
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
	api                   *tgbotapi.BotAPI
	llm                   llm.LLMClient
	storage               storage.ChatHistoryStorage // Используем интерфейс
	config                *config.Config
	chatSettings          map[int64]*ChatSettings         // Настройки чатов (в памяти)
	pendingSettings       map[int64]string                // Отслеживание ожидаемого ввода настроек [chatID]settingKey
	directReplyTimestamps map[int64]map[int64][]time.Time // Временные метки прямых ответов [chatID][userID]timestamps
	settingsMutex         sync.RWMutex
	stop                  chan struct{}
	summaryMutex          sync.RWMutex
	lastSummaryRequest    map[int64]time.Time
	autoSummaryTicker     *time.Ticker // Оставляем для авто-саммари
	randSource            *rand.Rand   // Источник случайных чисел
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
	var storageImpl storage.ChatHistoryStorage
	var initErr error // Используем initErr для ошибок инициализации хранилища
	switch cfg.StorageType {
	case config.StorageTypeFile:
		storageImpl = storage.NewFileStorage(cfg.ContextWindow, true) // Используем =
		log.Println("Используется файловое хранилище")
	case config.StorageTypePostgres:
		// Используем = для storageImpl и initErr
		storageImpl, initErr = storage.NewPostgresStorage(
			cfg.PostgresqlHost,
			cfg.PostgresqlPort,
			cfg.PostgresqlUser,
			cfg.PostgresqlPassword,
			cfg.PostgresqlDbname,
			cfg.ContextWindow,
			cfg.Debug,
		)
		if initErr != nil {
			return nil, fmt.Errorf("ошибка создания PostgreSQL хранилища: %w", initErr)
		}
		log.Println("Используется PostgreSQL хранилище")
	case config.StorageTypeMongo:
		log.Println("Попытка инициализации MongoDB хранилища...")
		// Используем = для storageImpl и initErr
		storageImpl, initErr = storage.NewMongoStorage(
			cfg.MongoDbURI,
			cfg.MongoDbName,
			cfg.MongoDbMessagesCollection,
			cfg.MongoDbUserProfilesCollection,
			cfg, // Передаем весь конфиг
		)
		if initErr != nil {
			log.Printf("[WARN] Ошибка инициализации MongoDB хранилища: %v. Переключение на файловое хранилище.", initErr)
			// Fallback на файловое хранилище
			storageImpl = storage.NewFileStorage(cfg.ContextWindow, true)
			log.Println("Используется файловое хранилище (fallback).")
		} else {
			log.Println("Хранилище MongoDB успешно инициализировано.")
		}
	default:
		return nil, fmt.Errorf("неизвестный тип хранилища: %s", cfg.StorageType)
	}

	// Инициализация источника случайных чисел
	source := rand.NewSource(time.Now().UnixNano())
	randGen := rand.New(source)

	// Инициализация настроек в памяти
	chatSettings := make(map[int64]*ChatSettings)

	// Создание экземпляра бота
	b := &Bot{
		api:                   api,
		llm:                   llmClient,
		storage:               storageImpl, // Назначаем выбранное хранилище
		config:                cfg,
		chatSettings:          chatSettings,
		pendingSettings:       make(map[int64]string),
		directReplyTimestamps: make(map[int64]map[int64][]time.Time),
		settingsMutex:         sync.RWMutex{},
		stop:                  make(chan struct{}),
		summaryMutex:          sync.RWMutex{},
		lastSummaryRequest:    make(map[int64]time.Time),
		autoSummaryTicker:     nil,
		randSource:            randGen,
	}

	// Загрузка всех настроек чатов при старте
	b.loadAllChatSettingsFromStorage()

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
	var msg *tgbotapi.Message

	if update.Message != nil {
		chatID = update.Message.Chat.ID
		msg = update.Message
	} else if update.CallbackQuery != nil {
		chatID = update.CallbackQuery.Message.Chat.ID
	} else {
		// Неизвестный тип обновления, игнорируем
		return nil, false
	}

	b.settingsMutex.Lock()
	defer b.settingsMutex.Unlock()

	settings, exists := b.chatSettings[chatID]
	justInitialized := !exists

	if !exists {
		log.Printf("Инициализация нового чата: %d", chatID)

		// Загружаем настройки из БД, чтобы получить специфичные для чата значения
		dbSettings, err := b.storage.GetChatSettings(chatID)
		if err != nil {
			log.Printf("[ERROR][ensureChatInitialized] Ошибка получения настроек из DB для чата %d: %v. Будут использованы дефолты из config.", chatID, err)
			dbSettings = &storage.ChatSettings{} // Используем пустые, чтобы не было паники
		}

		// Создаем настройки в памяти, используя глобальные и загруженные из БД
		r := b.randSource // Получаем источник случайных чисел
		settings = &ChatSettings{
			Active:               true,
			MinMessages:          b.config.MinMessages,
			MaxMessages:          b.config.MaxMessages,
			DailyTakeTime:        b.config.DailyTakeTime,
			SummaryIntervalHours: b.config.SummaryIntervalHours,
			MessageCount:         r.Intn(b.config.MaxMessages-b.config.MinMessages+1) + b.config.MinMessages, // Случайное значение
			SrachState:           "none",
			SrachAnalysisEnabled: b.getSrachEnabledFromDbOrDefault(dbSettings), // Получаем из БД или дефолт конфига
		}
		b.chatSettings[chatID] = settings
		// Инициализируем map для лимитов этого чата
		b.directReplyTimestamps[chatID] = make(map[int64][]time.Time)

		// Отправляем приветственное сообщение в новом чате
		if msg != nil && msg.NewChatMembers != nil { // Только если добавили новых участников
			welcomePrompt := b.config.WelcomePrompt
			welcomeMsg, genErr := b.llm.GenerateArbitraryResponse(welcomePrompt, "")
			if genErr != nil {
				log.Printf("[ERROR][ensureChatInitialized] Ошибка генерации приветствия для чата %d: %v", chatID, genErr)
				welcomeMsg = "Всем привет! Я Рофлослав, готов общаться."
			}
			// Отправляем приветствие без сохранения в историю или обработки
			b.sendReply(chatID, welcomeMsg)
		}

		// Загружаем историю чата в фоне (после инициализации настроек)
		go b.loadChatHistory(chatID)
	}

	return settings, justInitialized
}

// getSrachEnabledFromDbOrDefault возвращает значение SrachAnalysisEnabled из dbSettings или из config.
func (b *Bot) getSrachEnabledFromDbOrDefault(dbSettings *storage.ChatSettings) bool {
	// TODO: Когда SrachAnalysisEnabled будет в storage.ChatSettings, использовать его:
	// if dbSettings != nil && dbSettings.SrachAnalysisEnabled != nil {
	// 	 return *dbSettings.SrachAnalysisEnabled
	// }
	// Пока что всегда берем из config
	return b.config.SrachAnalysisEnabled
}

// handleUpdate обрабатывает входящие обновления
func (b *Bot) handleUpdate(update tgbotapi.Update) {
	startTime := time.Now()

	// Гарантируем, что для чата существуют настройки в памяти
	_, justInitialized := b.ensureChatInitializedAndWelcome(update)
	// Если чат только что инициализирован, вероятно, не нужно обрабатывать сообщение дальше
	// (кроме CallbackQuery, которые могут прийти из старых сообщений)
	if justInitialized && update.Message != nil && update.Message.NewChatMembers == nil {
		chatID := update.Message.Chat.ID
		if b.config.Debug {
			log.Printf("[DEBUG][handleUpdate] Чат %d только что инициализирован, сообщение ID %d не обрабатывается (кроме приветствия).", chatID, update.Message.MessageID)
		}
		return
	}

	// Обработка CallbackQuery (нажатия кнопок)
	if update.CallbackQuery != nil {
		go b.handleCallback(update.CallbackQuery)
		return // Выходим после обработки колбэка
	}

	// Обработка обычных сообщений
	if update.Message != nil {
		// Обработка команд
		if update.Message.IsCommand() {
			go b.handleCommand(update.Message)
		} else {
			// Обработка обычных текстовых сообщений
			go b.handleMessage(update) // handleMessage теперь принимает Update
		}
	} else if update.MyChatMember != nil {
		// Обработка изменений статуса бота в чате (например, удаление)
		go b.handleChatMemberUpdate(update.MyChatMember)
	}

	// Логирование времени обработки для не-CallbackQuery
	if update.Message != nil {
		processingTime := time.Since(startTime)
		if b.config.Debug {
			log.Printf("[DEBUG][Timing] Обработка Message (ID: %d) заняла %s", update.Message.MessageID, processingTime.Round(time.Millisecond))
		}
	}
}

// loadAllChatSettingsFromStorage загружает настройки для всех известных чатов из хранилища.
func (b *Bot) loadAllChatSettingsFromStorage() {
	chatIDs, err := b.storage.GetAllChatIDs()
	if err != nil {
		log.Printf("[ERROR][LoadAllSettings] Ошибка получения списка ChatID из хранилища: %v", err)
		return
	}

	if len(chatIDs) == 0 {
		log.Println("[LoadAllSettings] Не найдено чатов в хранилище для загрузки настроек.")
		return
	}

	log.Printf("[LoadAllSettings] Загрузка настроек для %d чатов...", len(chatIDs))
	loadedCount := 0
	failedCount := 0

	b.settingsMutex.Lock()
	defer b.settingsMutex.Unlock()

	for _, chatID := range chatIDs {
		_, memoryExists := b.chatSettings[chatID]
		if memoryExists {
			if b.config.Debug {
				log.Printf("[DEBUG][LoadAllSettings] Настройки для чата %d уже в памяти, пропускаем загрузку из хранилища.", chatID)
			}
			loadedCount++
			continue
		}

		// Загружаем настройки из хранилища (не из памяти)
		dbSettings, err := b.storage.GetChatSettings(chatID)
		if err != nil {
			log.Printf("[ERROR][LoadAllSettings] Ошибка загрузки настроек для чата %d из хранилища: %v", chatID, err)
			failedCount++
			continue
		}

		// Создаем ChatSettings в памяти на основе глобальных настроек и специфичных для чата из БД
		r := b.randSource // Получаем источник случайных чисел
		newSettings := &ChatSettings{
			Active:               true, // Считаем активным по умолчанию при загрузке
			MinMessages:          b.config.MinMessages,
			MaxMessages:          b.config.MaxMessages,
			DailyTakeTime:        b.config.DailyTakeTime,
			SummaryIntervalHours: b.config.SummaryIntervalHours,
			MessageCount:         r.Intn(b.config.MaxMessages-b.config.MinMessages+1) + b.config.MinMessages, // Случайное значение при инициализации
			SrachState:           "none",
			SrachAnalysisEnabled: b.getSrachEnabledFromDbOrDefault(dbSettings), // Получаем из БД или дефолт конфига
			// ID сообщений (LastMenuMessageID и т.д.) инициализируются нулями
		}

		b.chatSettings[chatID] = newSettings
		// Инициализируем map для лимитов этого чата
		b.directReplyTimestamps[chatID] = make(map[int64][]time.Time)
		loadedCount++
	}

	log.Printf("[LoadAllSettings] Загрузка настроек завершена. Загружено: %d, Ошибок: %d", loadedCount, failedCount)
}

// handleChatMemberUpdate обрабатывает изменения статуса бота в чате
func (b *Bot) handleChatMemberUpdate(update *tgbotapi.ChatMemberUpdated) {
	chatID := update.Chat.ID
	myStatus := update.NewChatMember.Status
	userName := update.From.UserName

	if update.NewChatMember.User.ID == b.api.Self.ID {
		log.Printf("Статус бота в чате %d изменен на '%s' пользователем @%s", chatID, myStatus, userName)
		b.settingsMutex.Lock()
		defer b.settingsMutex.Unlock()

		if myStatus == "left" || myStatus == "kicked" {
			// Бот удален или кикнут
			log.Printf("Бот удален из чата %d. Удаляю настройки из памяти.", chatID)
			delete(b.chatSettings, chatID)
			delete(b.pendingSettings, chatID)
			delete(b.directReplyTimestamps, chatID)
			b.summaryMutex.Lock()
			delete(b.lastSummaryRequest, chatID)
			b.summaryMutex.Unlock()
			// TODO: Опционально: Очистить историю в хранилище? Или оставить?
			// err := b.storage.ClearChatHistory(chatID)
			// if err != nil {
			// 	 log.Printf("[WARN] Не удалось очистить историю для чата %d после удаления бота: %v", chatID, err)
			// }
		} else if myStatus == "member" {
			// Бот добавлен или вернулся
			log.Printf("Бот добавлен или вернулся в чат %d.", chatID)
			// Настройки должны были быть созданы в ensureChatInitializedAndWelcome
		}
	} else {
		// Изменение статуса другого пользователя (не бота)
		if b.config.Debug {
			log.Printf("[DEBUG] Статус пользователя %d (@%s) в чате %d изменен на '%s' пользователем @%s",
				update.NewChatMember.User.ID, update.NewChatMember.User.UserName, chatID, myStatus, userName)
		}
		// TODO: Возможно, обновлять профиль пользователя (например, если он покинул чат)
	}
}
