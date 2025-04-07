package bot

import (
	"fmt"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// ChatSettings содержит настройки для каждого чата
// Используем оригинальные имена полей для совместимости с bot.go
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

// formatSummaryInterval форматирует интервал саммари для отображения
func formatSummaryInterval(intervalHours int) string {
	if intervalHours <= 0 {
		return "Выкл."
	}
	return fmt.Sprintf("%d ч.", intervalHours)
}

// formatEnabled форматирует текст и callback кнопки в зависимости от статуса (включено/выключено)
// Используем IsEnabled для совместимости с getSettingsKeyboard ниже
func formatEnabled(textEnabled, textDisabled string, isEnabled bool, callbackEnabled, callbackDisabled string) (string, string) {
	if isEnabled {
		return fmt.Sprintf("%s: Вкл ✅", textEnabled), callbackEnabled
	}
	return fmt.Sprintf("%s: Выкл ❌", textDisabled), callbackDisabled
}

// getEnabledStatusText возвращает текстовое представление статуса (Вкл/Выкл)
func getEnabledStatusText(enabled bool) string {
	if enabled {
		return "Вкл ✅"
	}
	return "Выкл ❌"
}

// getSettingsKeyboard создает клавиатуру с текущими настройками чата
// Используем актуальные имена полей из структуры ChatSettings выше
func getSettingsKeyboard(settings *ChatSettings) tgbotapi.InlineKeyboardMarkup {
	rows := [][]tgbotapi.InlineKeyboardButton{}

	// 1. Статус бота (Вкл/Выкл) - Используем поле Active
	// УБИРАЕМ ЭТУ КНОПКУ ИЗ НАСТРОЕК
	/*
		statusText, statusCallback := formatEnabled("Бот", "Бот", settings.Active, "toggle_active", "toggle_active")
		statusRow := []tgbotapi.InlineKeyboardButton{
			tgbotapi.NewInlineKeyboardButtonData(statusText, statusCallback),
		}
		rows = append(rows, statusRow)
	*/

	// 2. Интервал сообщений - Используем MinMessages и MaxMessages
	intervalText := fmt.Sprintf("Интервал: %d-%d сообщ.", settings.MinMessages, settings.MaxMessages)
	intervalRow := []tgbotapi.InlineKeyboardButton{
		tgbotapi.NewInlineKeyboardButtonData(intervalText, "change_interval"),
	}
	rows = append(rows, intervalRow)

	// 3. Время темы дня
	dailyTakeText := fmt.Sprintf("Тема дня: %02d:00", settings.DailyTakeTime)
	dailyTakeRow := []tgbotapi.InlineKeyboardButton{
		tgbotapi.NewInlineKeyboardButtonData(dailyTakeText, "change_daily_time"),
	}
	rows = append(rows, dailyTakeRow)

	// 4. Интервал авто-саммари
	summaryIntervalText := fmt.Sprintf("Авто-саммари: %s", formatSummaryInterval(settings.SummaryIntervalHours))
	summaryIntervalRow := []tgbotapi.InlineKeyboardButton{
		tgbotapi.NewInlineKeyboardButtonData(summaryIntervalText, "change_summary_interval"),
	}
	rows = append(rows, summaryIntervalRow)

	// 5. Анализ срачей (Вкл/Выкл)
	srachText, srachCallback := formatEnabled("Анализ срачей", "Анализ срачей", settings.SrachAnalysisEnabled, "toggle_srach_analysis", "toggle_srach_analysis")
	srachRow := []tgbotapi.InlineKeyboardButton{
		tgbotapi.NewInlineKeyboardButtonData(srachText, srachCallback),
	}
	rows = append(rows, srachRow)

	return tgbotapi.NewInlineKeyboardMarkup(rows...)
}
