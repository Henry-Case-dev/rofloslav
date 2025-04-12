package bot

import (
	"fmt"
	"log"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// sendSettingsKeyboard отправляет клавиатуру настроек, удаляя предыдущую (если есть)
// и сохраняя ID нового сообщения.
func (b *Bot) sendSettingsKeyboard(chatID int64, lastSettingsMsgID int) {
	// Удаляем предыдущее сообщение настроек, если оно есть
	if lastSettingsMsgID != 0 {
		b.deleteMessage(chatID, lastSettingsMsgID)
	}

	// Получаем актуальные настройки для отображения
	b.settingsMutex.RLock()
	settings, exists := b.chatSettings[chatID]
	if !exists {
		b.settingsMutex.RUnlock()
		log.Printf("sendSettingsKeyboard: Настройки для чата %d не найдены!", chatID)
		// Можно отправить сообщение об ошибке, но лучше просто не отправлять клавиатуру
		return
	}
	b.settingsMutex.RUnlock() // Разблокируем после получения указателя на settings в памяти

	// Загружаем настройки из БД для клавиатуры
	dbSettings, err := b.storage.GetChatSettings(chatID)
	if err != nil {
		log.Printf("[ERROR][sendSettingsKeyboard] Чат %d: Ошибка получения настроек из DB: %v", chatID, err)
		// Продолжаем без dbSettings, клавиатура покажет дефолты
	}

	// Формируем текст сообщения (используем настройки из памяти, если они есть)
	msgText := `⚙️ *Настройки чата:*`
	if settings != nil {
		msgText += fmt.Sprintf("\nИнтервал ответа: %d-%d сообщ.", settings.MinMessages, settings.MaxMessages) // Предполагаем, что это берется из cfg, но settings хранит в памяти
		msgText += fmt.Sprintf("\nВремя 'темы дня': %02d:00", settings.DailyTakeTime)
		msgText += fmt.Sprintf("\nИнтервал авто-саммари: %s", formatSummaryInterval(settings.SummaryIntervalHours))
	} else {
		msgText += "\n(Не удалось загрузить текущие настройки из памяти)"
	}
	// Добавляем отображение настроек из dbSettings
	// TODO: Добавить отображение SrachAnalysisEnabled из dbSettings, когда поле появится
	voiceStatus := b.config.VoiceTranscriptionEnabledDefault // Используем b.config
	if dbSettings != nil && dbSettings.VoiceTranscriptionEnabled != nil {
		voiceStatus = *dbSettings.VoiceTranscriptionEnabled
	}
	msgText += fmt.Sprintf("\n🎤 Распознавание голоса: %s", getEnabledStatusText(voiceStatus))

	// Получаем клавиатуру настроек, передавая dbSettings и cfg
	keyboard := getSettingsKeyboard(dbSettings, b.config) // Передаем b.config

	// Отправляем новое сообщение с клавиатурой
	msg := tgbotapi.NewMessage(chatID, msgText)
	msg.ReplyMarkup = keyboard
	msg.ParseMode = "Markdown"

	sentMsg, err := b.api.Send(msg)
	if err != nil {
		log.Printf("Ошибка отправки клавиатуры настроек в чат %d: %v", chatID, err)
		return
	}

	// Сохраняем ID нового сообщения настроек
	b.settingsMutex.Lock()
	if settings, exists := b.chatSettings[chatID]; exists { // Проверяем еще раз на всякий случай
		settings.LastSettingsMessageID = sentMsg.MessageID
		if b.config.Debug {
			log.Printf("[DEBUG] Сохранен новый LastSettingsMessageID: %d для чата %d", sentMsg.MessageID, chatID)
		}
	} else {
		log.Printf("[WARN] Настройки для чата %d не найдены при попытке сохранить новый LastSettingsMessageID.", chatID)
	}
	b.settingsMutex.Unlock()
}

// updateSettingsKeyboard обновляет существующее сообщение настроек
func (b *Bot) updateSettingsKeyboard(query *tgbotapi.CallbackQuery) {
	chatID := query.Message.Chat.ID
	messageID := query.Message.MessageID

	// Получаем актуальные настройки
	b.settingsMutex.RLock()
	settings, exists := b.chatSettings[chatID]
	if !exists {
		b.settingsMutex.RUnlock()
		log.Printf("updateSettingsKeyboard: Настройки для чата %d не найдены!", chatID)
		b.answerCallback(query.ID, "Ошибка: настройки чата не найдены.")
		return
	}

	// Важно: Мы под мьютексом RLock для settings (настройки в памяти)
	// Нам нужны настройки из БД (dbSettings) для передачи в getSettingsKeyboard
	dbSettings, err := b.storage.GetChatSettings(chatID)
	if err != nil {
		log.Printf("[ERROR][updateSettingsKeyboard] Чат %d: Ошибка получения настроек из DB: %v", chatID, err)
		// Продолжаем без dbSettings, клавиатура покажет дефолты
	}

	// Формируем новый текст сообщения (используем settings из памяти, т.к. они могли только что обновиться)
	msgText := `⚙️ *Настройки чата:*`
	msgText += fmt.Sprintf("\nИнтервал ответа: %d-%d сообщ.", settings.MinMessages, settings.MaxMessages)
	msgText += fmt.Sprintf("\nВремя 'темы дня': %02d:00", settings.DailyTakeTime)
	msgText += fmt.Sprintf("\nИнтервал авто-саммари: %s", formatSummaryInterval(settings.SummaryIntervalHours))
	// Добавляем отображение настроек из dbSettings
	// TODO: Добавить отображение SrachAnalysisEnabled из dbSettings, когда поле появится
	voiceStatus := b.config.VoiceTranscriptionEnabledDefault // Используем b.config
	if dbSettings != nil && dbSettings.VoiceTranscriptionEnabled != nil {
		voiceStatus = *dbSettings.VoiceTranscriptionEnabled // Получаем актуальное из БД
	}
	msgText += fmt.Sprintf("\n🎤 Распознавание голоса: %s", getEnabledStatusText(voiceStatus))

	// Получаем обновленную клавиатуру, передавая dbSettings и cfg
	keyboard := getSettingsKeyboard(dbSettings, b.config) // Передаем dbSettings и b.config
	b.settingsMutex.RUnlock()                             // Теперь можно разблокировать

	// Создаем конфиг для редактирования сообщения
	editMsg := tgbotapi.NewEditMessageText(chatID, messageID, msgText)
	editMsg.ReplyMarkup = &keyboard
	editMsg.ParseMode = "Markdown"

	_, err = b.api.Send(editMsg)
	if err != nil {
		log.Printf("Ошибка обновления клавиатуры настроек в чате %d: %v", chatID, err)
		b.answerCallback(query.ID, "Ошибка обновления настроек.")
	} else {
		b.answerCallback(query.ID, "Настройки обновлены.") // Отвечаем на колбэк
	}
}

// setSrachAnalysis включает или выключает анализ срачей для чата
func (b *Bot) setSrachAnalysis(chatID int64, enabled bool) {
	b.settingsMutex.Lock()
	defer b.settingsMutex.Unlock()

	if settings, exists := b.chatSettings[chatID]; exists {
		if settings.SrachAnalysisEnabled != enabled {
			settings.SrachAnalysisEnabled = enabled
			log.Printf("Чат %d: Анализ срачей %s.", chatID, getEnabledStatusText(settings.SrachAnalysisEnabled))
			// Сбрасываем состояние срача при изменении настройки
			settings.SrachState = "none"
			settings.SrachMessages = nil
		} else {
			log.Printf("Чат %d: Анализ срачей уже был %s.", chatID, getEnabledStatusText(settings.SrachAnalysisEnabled))
		}
	} else {
		log.Printf("setSrachAnalysis: Настройки для чата %d не найдены!", chatID)
	}
}
