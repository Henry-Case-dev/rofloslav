package bot

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Henry-Case-dev/rofloslav/internal/storage"
	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// scheduleDailyTake запускает планировщик для ежедневного тейка
func (b *Bot) scheduleDailyTake(dailyTakeTime int, timeZone string) {
	log.Println("[Scheduler DEBUG] Запущен цикл планировщика DailyTake.")
	// Получаем локацию из конфига
	loc, err := time.LoadLocation(timeZone)
	if err != nil {
		log.Printf("Ошибка загрузки часового пояса, используем UTC: %v", err)
		loc = time.UTC
	}

	for {
		// Проверяем, не остановлен ли бот
		select {
		case <-b.stop:
			log.Println("Остановка планировщика DailyTake...")
			return
		default:
			// Продолжаем работу
		}

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
		log.Printf("Следующий тейк запланирован через %v (в %s по %s)",
			sleepDuration.Round(time.Second), targetTime.Format("15:04"), timeZone)

		// Используем time.After для ожидания, чтобы можно было прервать через b.stop
		timer := time.NewTimer(sleepDuration)
		select {
		case <-timer.C:
			// Время пришло, отправляем тейк
			b.sendDailyTakeToAllChats()
		case <-b.stop:
			// Остановка во время ожидания
			timer.Stop() // Останавливаем таймер
			log.Println("Планировщик DailyTake остановлен во время ожидания.")
			return
		}
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
	take, err := b.llm.GenerateArbitraryResponse(dailyTakePrompt, "")
	if err != nil {
		if b.config.Debug {
			log.Printf("[DEBUG] Ошибка при генерации ежедневного тейка: %v. Полный текст ошибки: %s", err, err.Error())
		} else {
			log.Printf("Ошибка при генерации ежедневного тейка: %v", err)
		}
		return
	}

	message := "🔥 *Тема дня:*\n\n" + take

	// Отправляем во все активные чаты
	b.settingsMutex.RLock()
	activeChatIDs := make([]int64, 0, len(b.chatSettings))
	for chatID, settings := range b.chatSettings {
		if settings.Active {
			activeChatIDs = append(activeChatIDs, chatID)
		}
	}
	b.settingsMutex.RUnlock()

	activeCount := len(activeChatIDs)
	if b.config.Debug {
		log.Printf("[DEBUG] Найдено %d активных чатов для отправки тейка.", activeCount)
	}

	var wg sync.WaitGroup
	for _, chatID := range activeChatIDs {
		wg.Add(1)
		go func(cid int64) {
			defer wg.Done()
			b.sendReply(cid, message)
		}(chatID)
	}
	wg.Wait() // Ждем завершения отправки во все чаты

	if b.config.Debug {
		log.Printf("[DEBUG] Тема дня отправлена в %d активных чатов.", activeCount)
	}
}

// scheduleAutoSummary запускает планировщик для автоматического саммари
func (b *Bot) scheduleAutoSummary() {
	log.Println("[Scheduler DEBUG] Запущен цикл планировщика AutoSummary.")
	// Используем общий тикер, чтобы проверять все чаты раз в час (например)
	// Точность до секунды тут не нужна
	checkInterval := time.Hour
	ticker := time.NewTicker(checkInterval)
	defer ticker.Stop()

	log.Printf("Планировщик AutoSummary запущен с интервалом проверки: %v", checkInterval)

	for {
		select {
		case <-ticker.C:
			b.runAutoSummaryForAllChats()
		case <-b.stop:
			log.Println("Остановка планировщика AutoSummary...")
			return
		}
	}
}

// scheduleDonate запускает планировщик для отправки сообщений о донате
func (b *Bot) scheduleDonate() {
	if b.config.DonateTimeHours <= 0 {
		log.Println("[Scheduler] Планировщик донатов не запущен (DonateTimeHours <= 0).")
		return
	}

	log.Println("[Scheduler DEBUG] Запущен цикл планировщика Donate.")

	// Период отправки сообщений о донате (из конфига)
	donateInterval := time.Duration(b.config.DonateTimeHours) * time.Hour

	// Отправляем первое сообщение о донате через 30 секунд после запуска
	initialDelay := 30 * time.Second
	time.Sleep(initialDelay)

	// Отправляем первое сообщение о донате
	b.sendDonateMessageToAllChats()

	// Основной цикл планировщика
	for {
		// Проверяем, не остановлен ли бот
		select {
		case <-b.stop:
			log.Println("Остановка планировщика Donate...")
			return
		default:
			// Продолжаем работу
		}

		// Ожидаем до следующей отправки
		timer := time.NewTimer(donateInterval)
		select {
		case <-timer.C:
			// Время пришло, отправляем сообщение о донате
			b.sendDonateMessageToAllChats()
		case <-b.stop:
			// Остановка во время ожидания
			timer.Stop() // Останавливаем таймер
			log.Println("Планировщик Donate остановлен во время ожидания.")
			return
		}
	}
}

// sendDonateMessageToAllChats отправляет сообщение о донате с фотографией во все активные чаты
func (b *Bot) sendDonateMessageToAllChats() {
	if b.config.Debug {
		log.Printf("[DEBUG] Отправка сообщений о донате во все активные чаты")
	}

	// Проверяем, есть ли промпт для доната
	if b.config.DonatePrompt == "" {
		log.Printf("[WARNING] DonatePrompt не задан в конфигурации, сообщения о донате не будут отправлены")
		return
	}

	// Генерируем сообщение о донате
	donateMessage, err := b.llm.GenerateArbitraryResponse(b.config.DonatePrompt, "")
	if err != nil {
		log.Printf("[ERROR] Ошибка при генерации сообщения о донате: %v", err)
		return
	}

	// Форматируем сообщение с добавлением статичной фразы
	message := donateMessage + "\n\n[Подкинуть деньжат](https://donate.stream/rofloslav)"

	// Выбираем случайное изображение из папки donate_images
	imageFile, err := b.getRandomDonateImage()
	if err != nil {
		log.Printf("[ERROR] Ошибка при выборе изображения для доната: %v", err)
		// Если не удалось получить изображение, отправляем только текст
		b.sendDonateTextToAllChats(message)
		return
	}

	// Отправляем во все активные чаты
	b.settingsMutex.RLock()
	activeChatIDs := make([]int64, 0, len(b.chatSettings))
	for chatID, settings := range b.chatSettings {
		if settings.Active {
			activeChatIDs = append(activeChatIDs, chatID)
		}
	}
	b.settingsMutex.RUnlock()

	activeCount := len(activeChatIDs)
	if b.config.Debug {
		log.Printf("[DEBUG] Найдено %d активных чатов для отправки сообщения о донате.", activeCount)
	}

	var wg sync.WaitGroup
	for _, chatID := range activeChatIDs {
		wg.Add(1)
		go func(cid int64) {
			defer wg.Done()
			err := b.sendPhotoWithCaption(cid, imageFile, message)
			if err != nil {
				log.Printf("[ERROR] Ошибка отправки фото с сообщением о донате в чат %d: %v", cid, err)
				// При ошибке пробуем отправить только текст
				b.sendReplyMarkdown(cid, "💰 "+message)
			}
		}(chatID)
	}
	wg.Wait() // Ждем завершения отправки во все чаты

	if b.config.Debug {
		log.Printf("[DEBUG] Сообщение о донате отправлено в %d активных чатов.", activeCount)
	}
}

// getRandomDonateImage возвращает путь к случайному изображению из папки donate_images
func (b *Bot) getRandomDonateImage() (string, error) {
	// Открываем директорию с изображениями для донатов
	donateDir := "donate_images"
	files, err := os.ReadDir(donateDir)
	if err != nil {
		return "", fmt.Errorf("ошибка чтения директории %s: %w", donateDir, err)
	}

	// Фильтруем только PNG и JPG файлы
	var imageFiles []string
	for _, file := range files {
		if file.IsDir() {
			continue
		}

		name := file.Name()
		if strings.HasSuffix(strings.ToLower(name), ".png") ||
			strings.HasSuffix(strings.ToLower(name), ".jpg") ||
			strings.HasSuffix(strings.ToLower(name), ".jpeg") {
			imageFiles = append(imageFiles, filepath.Join(donateDir, name))
		}
	}

	if len(imageFiles) == 0 {
		return "", fmt.Errorf("в директории %s нет подходящих изображений (PNG или JPG)", donateDir)
	}

	// Выбираем случайное изображение
	randomIndex := b.randSource.Intn(len(imageFiles))
	return imageFiles[randomIndex], nil
}

// sendPhotoWithCaption отправляет фото с указанной подписью в чат
func (b *Bot) sendPhotoWithCaption(chatID int64, imagePath string, caption string) error {
	photo := tgbotapi.NewPhoto(chatID, tgbotapi.FilePath(imagePath))
	photo.Caption = caption
	photo.ParseMode = "Markdown"

	_, err := b.api.Send(photo)
	return err
}

// sendDonateTextToAllChats отправляет только текстовое сообщение о донате во все активные чаты
func (b *Bot) sendDonateTextToAllChats(message string) {
	b.settingsMutex.RLock()
	activeChatIDs := make([]int64, 0, len(b.chatSettings))
	for chatID, settings := range b.chatSettings {
		if settings.Active {
			activeChatIDs = append(activeChatIDs, chatID)
		}
	}
	b.settingsMutex.RUnlock()

	var wg sync.WaitGroup
	for _, chatID := range activeChatIDs {
		wg.Add(1)
		go func(cid int64) {
			defer wg.Done()
			b.sendReplyMarkdown(cid, "💰 "+message)
		}(chatID)
	}
	wg.Wait()
}

// runAutoSummaryForAllChats проверяет и запускает авто-саммари для всех чатов, если пришло время
func (b *Bot) runAutoSummaryForAllChats() {
	now := time.Now()
	if b.config.Debug {
		log.Printf("[DEBUG][AutoSummary] Проверка необходимости авто-саммари для всех чатов (%s)...", now.Format(time.Kitchen))
	}

	b.settingsMutex.Lock() // Блокируем на время итерации и возможного изменения LastAutoSummaryTime
	defer b.settingsMutex.Unlock()

	chatsToCheck := make([]int64, 0, len(b.chatSettings))
	for chatID := range b.chatSettings {
		chatsToCheck = append(chatsToCheck, chatID)
	}

	triggeredCount := 0
	for _, chatID := range chatsToCheck {
		settings, exists := b.chatSettings[chatID] // Получаем настройки внутри блокировки
		if !exists || !settings.Active || settings.SummaryIntervalHours <= 0 {
			continue // Пропускаем неактивные чаты или чаты с выключенным авто-саммари
		}

		interval := time.Duration(settings.SummaryIntervalHours) * time.Hour
		// Если время последнего саммари не установлено или прошло достаточно времени
		if settings.LastAutoSummaryTime.IsZero() || now.Sub(settings.LastAutoSummaryTime) >= interval {
			if b.config.Debug {
				log.Printf("[DEBUG][AutoSummary] Запускаю авто-саммари для чата %d (Интервал: %dh, Последний: %v)",
					chatID, settings.SummaryIntervalHours, settings.LastAutoSummaryTime)
			}
			settings.LastAutoSummaryTime = now // Обновляем время последнего запуска СРАЗУ
			triggeredCount++
			// Запускаем генерацию в горутине, чтобы не блокировать проверку других чатов
			log.Printf("[DEBUG][AutoSummary] -> Запуск горутины createAndSendSummary для чата %d", chatID)
			go b.createAndSendSummary(chatID)
		}
	}

	if triggeredCount > 0 && b.config.Debug {
		log.Printf("[DEBUG][AutoSummary] Запущено авто-саммари для %d чатов.", triggeredCount)
	}
}

// --- НОВЫЙ Планировщик для Auto Bio Analysis ---

// runAutoBioAnalysisForChat запускает анализ профилей для всех пользователей в указанном чате.
func (b *Bot) runAutoBioAnalysisForChat(chatID int64) {
	if !b.config.AutoBioEnabled {
		return // Не запускаем, если выключено
	}

	// Получаем список пользователей в этом чате
	profiles, err := b.storage.GetAllUserProfiles(chatID)
	if err != nil {
		log.Printf("[AutoBio ERROR] Чат %d: Не удалось получить профили пользователей: %v", chatID, err)
		return // Выходим, если не удалось получить профили
	}

	if b.config.Debug {
		log.Printf("[AutoBio DEBUG] Чат %d: Найдено %d профилей для анализа.", chatID, len(profiles))
	}

	// Для каждого пользователя запускаем анализ в горутине
	for _, profile := range profiles {
		// Проверяем стоп-сигнал перед запуском горутины
		select {
		case <-b.stop:
			log.Println("[AutoBio] Остановка во время итерации по пользователям чата %d.", chatID)
			return
		default:
		}

		b.autoBioSemaphore <- struct{}{}  // Захватываем семафор перед запуском горутины
		go func(p *storage.UserProfile) { // Передаем копию указателя в горутину
			defer func() {
				<-b.autoBioSemaphore // Освобождаем семафор после завершения
			}()
			b.analyzeAndUpdateProfile(p.ChatID, p.UserID)
		}(profile)

		// Небольшая задержка, чтобы не перегружать API/DB
		// Если семафор используется, можно сделать меньше или убрать
		time.Sleep(100 * time.Millisecond)
	}

	if b.config.Debug {
		log.Printf("[AutoBio DEBUG] Чат %d: Завершение запуска анализа для всех профилей чата.", chatID)
	}
}

// runAutoBioAnalysisForAllUsers запускает анализ профилей для всех пользователей во всех активных чатах.
func (b *Bot) runAutoBioAnalysisForAllUsers() {
	if !b.config.AutoBioEnabled {
		return // Не запускаем, если выключено
	}
	log.Printf("[AutoBio Scheduler] Начало цикла анализа профилей для ВСЕХ чатов...")
	if b.config.Debug {
		log.Printf("[AutoBio Scheduler DEBUG] Запуск runAutoBioAnalysisForAllUsers...")
	}

	// Получаем список активных чатов
	b.settingsMutex.RLock()
	activeChatIDs := make([]int64, 0, len(b.chatSettings))
	for chatID, settings := range b.chatSettings {
		if settings.Active {
			activeChatIDs = append(activeChatIDs, chatID)
		}
	}
	b.settingsMutex.RUnlock()

	if b.config.Debug {
		log.Printf("[AutoBio Scheduler DEBUG] Найдено %d активных чатов для анализа профилей.", len(activeChatIDs))
	}

	// Для каждого активного чата
	for _, chatID := range activeChatIDs {
		// Проверяем стоп-сигнал перед обработкой следующего чата
		select {
		case <-b.stop:
			log.Println("[AutoBio Scheduler] Остановка во время итерации по чатам.")
			return
		default:
		}

		if b.config.Debug {
			log.Printf("[AutoBio Scheduler DEBUG] Запуск анализа для чата %d...", chatID)
		}
		// Вызываем новую функцию для анализа конкретного чата
		b.runAutoBioAnalysisForChat(chatID)

		// Небольшая задержка между чатами
		time.Sleep(5 * time.Second) // Увеличим задержку между чатами
	}

	if b.config.Debug {
		log.Printf("[AutoBio Scheduler DEBUG] Завершение цикла runAutoBioAnalysisForAllUsers.")
	}
	log.Printf("[AutoBio Scheduler] Завершение цикла анализа профилей для ВСЕХ чатов.")
}

// scheduleAutoBioAnalysis запускает планировщик для анализа профилей пользователей
func (b *Bot) scheduleAutoBioAnalysis() {
	if !b.config.AutoBioEnabled {
		log.Println("[AutoBio Scheduler] Анализ профилей отключен (AUTO_BIO_ENABLED=false).")
		return
	}

	log.Println("[AutoBio Scheduler] Запуск планировщика AutoBio Analysis.")

	// Запускаем первый анализ вскоре после старта бота
	initialDelay := 2 * time.Minute // Например, через 2 минуты после старта
	go func() {
		time.Sleep(initialDelay)
		select {
		case <-b.stop: // Проверяем, не остановили ли бота до первого запуска
			return
		default:
			log.Println("[AutoBio Scheduler] Запуск первичного анализа профилей...")
			b.runAutoBioAnalysisForAllUsers()
		}
	}()

	// Создаем тикер для периодического запуска
	interval := time.Duration(b.config.AutoBioIntervalHours) * time.Hour
	if interval <= 0 {
		log.Printf("[AutoBio Scheduler WARN] Интервал AUTO_BIO_INTERVAL_HOURS (%d) некорректен, периодический запуск невозможен.", b.config.AutoBioIntervalHours)
		return
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	log.Printf("[AutoBio Scheduler] Периодический анализ профилей настроен с интервалом %v.", interval)

	// Основной цикл планировщика
	for {
		select {
		case <-ticker.C:
			if b.config.Debug {
				log.Printf("[AutoBio Scheduler DEBUG] Сработал тикер, запуск runAutoBioAnalysisForAllUsers...")
			}
			b.runAutoBioAnalysisForAllUsers()
		case <-b.stop:
			log.Println("[AutoBio Scheduler] Остановка планировщика анализа профилей...")
			return
		}
	}
}
