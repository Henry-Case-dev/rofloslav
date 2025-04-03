package bot

import (
	"fmt"
	"strconv"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// getMainKeyboard возвращает основную клавиатуру
func getMainKeyboard() tgbotapi.InlineKeyboardMarkup {
	return tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("📊 Саммари", "summary"),
			tgbotapi.NewInlineKeyboardButtonData("⚙️ Настройки", "settings"),
		),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("⏸️ Пауза", "stop"),
		),
	)
}

// getSettingsKeyboard возвращает клавиатуру настроек
func getSettingsKeyboard(minMessages, maxMessages, dailyTakeTime, summaryInterval int, srachEnabled bool) tgbotapi.InlineKeyboardMarkup {
	minStr := strconv.Itoa(minMessages)
	maxStr := strconv.Itoa(maxMessages)
	timeStr := strconv.Itoa(dailyTakeTime)
	summaryIntervalStr := "Выкл."
	if summaryInterval > 0 {
		summaryIntervalStr = strconv.Itoa(summaryInterval) + " ч."
	}

	// Текст и callback для кнопки анализа срачей
	srachButtonText := "🔥 Анализ срачей: Вкл"
	srachCallbackData := "toggle_srach_off"
	if !srachEnabled {
		srachButtonText = "💀 Анализ срачей: Выкл"
		srachCallbackData = "toggle_srach_on"
	}

	return tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(fmt.Sprintf("Мин. интервал: %s", minStr), "set_min_messages"),
		),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(fmt.Sprintf("Макс. интервал: %s", maxStr), "set_max_messages"),
		),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(fmt.Sprintf("Время темы дня: %s:00", timeStr), "set_daily_time"),
		),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(fmt.Sprintf("Интервал авто-саммари: %s", summaryIntervalStr), "set_summary_interval"),
		),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(srachButtonText, srachCallbackData),
		),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("🔙 Назад", "back_to_main"),
		),
	)
}
