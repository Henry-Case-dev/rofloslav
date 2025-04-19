package bot

import (
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/Henry-Case-dev/rofloslav/internal/config"
	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// loadChatHistory загружает историю сообщений для указанного чата
func (b *Bot) loadChatHistory(chatID int64) {
	if b.config.Debug {
		log.Printf("[DEBUG][Load History] Чат %d: Начинаю загрузку истории.", chatID)
	}

	// 1. Отправляем ПЕРВОЕ сообщение и сохраняем его ID
	initialStatus := b.storage.GetStatus(chatID) // Получаем статус ДО загрузки
	initialMsgText := fmt.Sprintf("⏳ Загружаю историю чата...\nСтатус хранилища: %s", initialStatus)
	initialMsg, errInit := b.sendReplyReturnMsg(chatID, initialMsgText)
	var initialMsgID int
	if errInit != nil {
		log.Printf("[WARN][Load History] Чат %d: Не удалось отправить начальное сообщение: %v", chatID, errInit)
	} else if initialMsg != nil {
		initialMsgID = initialMsg.MessageID
	}

	// 2. Загружаем историю (специфично для FileStorage)
	var history []*tgbotapi.Message
	var loadErr error
	if b.config.StorageType == config.StorageTypeFile {
		history, loadErr = b.storage.LoadChatHistory(chatID)
		if loadErr != nil {
			// Логируем ошибку, но не останавливаемся, просто начинаем без истории
			log.Printf("[ERROR][Load History] Чат %d: Ошибка загрузки истории из файла: %v", chatID, loadErr)
			finalStatus := b.storage.GetStatus(chatID) // Статус ПОСЛЕ ошибки
			finalMsgText := fmt.Sprintf("⚠️ Не удалось загрузить историю чата из файла.\nСтатус хранилища: %s", finalStatus)
			// Отправляем финальное сообщение и удаляем начальное
			b.sendReplyAndDeleteInitial(chatID, finalMsgText, initialMsgID)
			_ = b.storage.ClearChatHistory(chatID) // Очищаем память на всякий случай
			return
		}
	}

	// 3. Формируем итоговый текст сообщения
	finalMsgText := ""
	loadedCount := 0

	if b.config.StorageType == config.StorageTypeFile {
		// Логика для FileStorage (как раньше)
		if history == nil { // Файл не найден
			if b.config.Debug {
				log.Printf("[DEBUG][Load History] Чат %d: История не найдена или файл не существует.", chatID)
			}
			finalMsgText = "✅ История чата не найдена в файле."
		} else if len(history) == 0 { // Файл пуст
			if b.config.Debug {
				log.Printf("[DEBUG][Load History] Чат %d: Загружена пустая история (файл был пуст или содержал []).", chatID)
			}
			finalMsgText = "✅ История чата в файле пуста."
		} else { // История из файла загружена
			loadCount := len(history)
			if loadCount > b.config.ContextWindow {
				log.Printf("[DEBUG][Load History] Чат %d: История из файла (%d) длиннее окна (%d), обрезаю.", chatID, loadCount, b.config.ContextWindow)
				history = history[loadCount-b.config.ContextWindow:]
				loadCount = len(history)
			}
			log.Printf("[DEBUG][Load History] Чат %d: Добавляю %d загруженных сообщений из файла в контекст.", chatID, loadCount)
			b.storage.AddMessagesToContext(chatID, history)
			loadedCount = loadCount
			finalMsgText = fmt.Sprintf("✅ Контекст загружен из файла: %d сообщений.", loadedCount)
		}
	} else {
		// Логика для MongoDB/PostgreSQL
		// История всегда "найдена", если есть подключение.
		// Просто выводим актуальный статус хранилища.
		finalMsgText = "✅ Инициализация хранилища завершена."
		// Загрузка последних N сообщений в память не требуется для БД,
		// но можно запросить текущие GetMessages для логов
		if b.config.Debug {
			currentMsgs, errGet := b.storage.GetMessages(chatID, b.config.ContextWindow) // Используем ContextWindow как лимит
			if errGet != nil {
				log.Printf("[DEBUG][Load History] Чат %d: Ошибка при вызове GetMessages для лога: %v", chatID, errGet)
			} else {
				log.Printf("[DEBUG][Load History] Чат %d: Хранилище (%s) инициализировано. Текущий контекст (из GetMessages): %d сообщ.", chatID, b.config.StorageType, len(currentMsgs))
			}
		}
	}

	// 4. Отправляем итоговое сообщение со статусом и удаляем начальное
	finalStatus := b.storage.GetStatus(chatID) // Статус ПОСЛЕ загрузки/инициализации
	b.sendReplyAndDeleteInitial(chatID, fmt.Sprintf("%s\nСтатус хранилища: %s", finalMsgText, finalStatus), initialMsgID)
}

// sendReplyReturnMsg - вспомогательная функция для отправки сообщения и возврата его объекта
func (b *Bot) sendReplyReturnMsg(chatID int64, text string) (*tgbotapi.Message, error) {
	msg := tgbotapi.NewMessage(chatID, text)
	msg.ParseMode = "Markdown" // Можно убрать, если тут не нужен Markdown
	sentMsg, err := b.api.Send(msg)
	if err != nil {
		log.Printf("Ошибка отправки сообщения в чат %d: %v", chatID, err)
		return nil, err
	}
	return &sentMsg, nil
}

// sendReplyAndDeleteInitial - отправляет итоговое сообщение и удаляет исходное
func (b *Bot) sendReplyAndDeleteInitial(chatID int64, finalMsgText string, initialMsgID int) {
	// Отправляем итоговое сообщение
	b.sendReply(chatID, finalMsgText) // sendReply уже существует

	// Удаляем начальное сообщение, если его ID был сохранен
	if initialMsgID != 0 {
		b.deleteMessage(chatID, initialMsgID)
	}
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
