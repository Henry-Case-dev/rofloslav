package bot

import (
	"fmt"
	"log"
	"sync"
	"time"
)

// loadChatHistory загружает историю сообщений для указанного чата
func (b *Bot) loadChatHistory(chatID int64) {
	if b.config.Debug {
		log.Printf("[DEBUG][Load History] Чат %d: Начинаю загрузку истории.", chatID)
	}

	initialStatus := b.storage.GetStatus(chatID) // Получаем статус ДО загрузки
	b.sendReply(chatID, fmt.Sprintf("⏳ Загружаю историю чата...\nСтатус хранилища: %s", initialStatus))

	// Загружаем историю из файла
	history, err := b.storage.LoadChatHistory(chatID)
	if err != nil {
		// Логируем ошибку, но не останавливаемся, просто начинаем без истории
		log.Printf("[ERROR][Load History] Чат %d: Ошибка загрузки истории: %v", chatID, err)
		finalStatus := b.storage.GetStatus(chatID) // Статус ПОСЛЕ ошибки
		b.sendReply(chatID, fmt.Sprintf("⚠️ Не удалось загрузить историю чата.\nСтатус хранилища: %s", finalStatus))
		// Убедимся, что старая история в памяти очищена, если была ошибка загрузки
		_ = b.storage.ClearChatHistory(chatID) // Используем существующий метод, игнорируем ошибку тут
		return
	}

	loadedCount := 0
	messageText := ""

	if history == nil { // LoadChatHistory теперь возвращает nil, nil если файла нет
		if b.config.Debug {
			log.Printf("[DEBUG][Load History] Чат %d: История не найдена или файл не существует.", chatID)
		}
		messageText = "✅ История чата не найдена."
	} else if len(history) == 0 {
		if b.config.Debug {
			log.Printf("[DEBUG][Load History] Чат %d: Загружена пустая история (файл был пуст или содержал []).", chatID)
		}
		messageText = "✅ История чата пуста."
	} else {
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
		loadedCount = loadCount
		messageText = fmt.Sprintf("✅ Контекст загружен: %d сообщений.", loadedCount)

		if b.config.Debug {
			log.Printf("[DEBUG][Load History] Чат %d: Загружено и добавлено в контекст %d сообщений.", chatID, loadedCount)
		}
	}

	// Отправляем итоговое сообщение со статусом
	finalStatus := b.storage.GetStatus(chatID) // Статус ПОСЛЕ загрузки
	b.sendReply(chatID, fmt.Sprintf("%s\nСтатус хранилища: %s", messageText, finalStatus))
}

// scheduleHistorySaving запускает планировщик для периодического сохранения истории
func (b *Bot) scheduleHistorySaving() {
	ticker := time.NewTicker(30 * time.Minute) // Сохраняем каждые 30 минут
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			b.saveAllChatHistories()
		case <-b.stop:
			// При остановке бота сохраняем все истории
			b.saveAllChatHistories()
			return
		}
	}
}

// saveAllChatHistories сохраняет историю всех активных чатов
func (b *Bot) saveAllChatHistories() {
	b.settingsMutex.RLock()
	chats := make([]int64, 0, len(b.chatSettings))
	for chatID := range b.chatSettings {
		chats = append(chats, chatID)
	}
	b.settingsMutex.RUnlock()

	log.Printf("[Save All] Начинаю сохранение истории для %d чатов...", len(chats))
	var wg sync.WaitGroup
	for _, chatID := range chats {
		wg.Add(1)
		go func(cid int64) {
			defer wg.Done()
			if err := b.storage.SaveChatHistory(cid); err != nil {
				log.Printf("[Save All ERROR] Ошибка сохранения истории для чата %d: %v", cid, err)
			}
		}(chatID)
	}
	wg.Wait() // Ждем завершения всех сохранений
	log.Printf("[Save All] Сохранение истории для всех чатов завершено.")
}
