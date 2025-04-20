package bot

import (
	"log"
)

// toggleSrachAnalysis переключает состояние анализа срачей для чата
func (b *Bot) toggleSrachAnalysis(chatID int64) (bool, error) {
	// 1. Получаем текущие настройки из БД
	dbSettings, err := b.storage.GetChatSettings(chatID)
	if err != nil {
		log.Printf("[ERROR][toggleSrachAnalysis] Chat %d: Не удалось получить настройки: %v", chatID, err)
		return false, err
	}

	// 2. Определяем текущее и новое состояние
	currentEnabled := b.config.SrachAnalysisEnabled // Дефолт из конфига
	if dbSettings.SrachAnalysisEnabled != nil {
		currentEnabled = *dbSettings.SrachAnalysisEnabled
	}
	newEnabled := !currentEnabled

	// 3. Обновляем настройку в хранилище
	errUpdate := b.storage.UpdateSrachAnalysisEnabled(chatID, newEnabled)
	if errUpdate != nil {
		log.Printf("[ERROR][toggleSrachAnalysis] Chat %d: Не удалось обновить настройку: %v", chatID, errUpdate)
		return currentEnabled, errUpdate // Возвращаем старое значение и ошибку
	}

	log.Printf("Чат %d: Анализ срачей переключен на %s", chatID, getEnabledStatusText(newEnabled))

	// 4. Сбрасываем состояние срача в памяти, если настройка была изменена
	b.settingsMutex.Lock()
	if settings, exists := b.chatSettings[chatID]; exists {
		settings.SrachAnalysisEnabled = newEnabled // Обновляем и в памяти для консистентности
		settings.SrachState = "none"
		settings.SrachMessages = nil
	}
	b.settingsMutex.Unlock()

	return newEnabled, nil
}
