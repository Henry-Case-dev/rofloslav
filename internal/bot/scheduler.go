package bot

import (
	"log"
	"sync"
	"time"
)

// scheduleDailyTake запускает планировщик для ежедневного тейка
func (b *Bot) scheduleDailyTake(dailyTakeTime int, timeZone string) {
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
			go b.createAndSendSummary(chatID)
		}
	}

	if triggeredCount > 0 && b.config.Debug {
		log.Printf("[DEBUG][AutoSummary] Запущено авто-саммари для %d чатов.", triggeredCount)
	}
}
