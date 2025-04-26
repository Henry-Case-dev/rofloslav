package bot

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/Henry-Case-dev/rofloslav/internal/utils"
)

// analyzeAndUpdateProfile анализирует сообщения пользователя и обновляет поле AutoBio в его профиле.
// TODO: Реализовать полную логику
func (b *Bot) analyzeAndUpdateProfile(chatID int64, userID int64) error {
	if !b.config.AutoBioEnabled {
		return nil // Функция выключена
	}

	if b.config.Debug {
		log.Printf("[AutoBio DEBUG] Chat %d, User %d: Начало анализа профиля...", chatID, userID)
	}

	// 1. Получаем профиль пользователя
	profile, err := b.storage.GetUserProfile(chatID, userID)
	if err != nil {
		log.Printf("[AutoBio ERROR] Chat %d, User %d: Не удалось получить профиль: %v", chatID, userID, err)
		return err
	}
	if profile == nil {
		// Это может произойти, если пользователь покинул чат или была ошибка при создании
		if b.config.Debug {
			log.Printf("[AutoBio DEBUG] Chat %d, User %d: Профиль не найден, анализ пропущен.", chatID, userID)
		}
		return nil
	}

	// 2. Определяем временные рамки для выборки сообщений
	var sinceTime time.Time
	lookbackDuration := time.Duration(b.config.AutoBioMessagesLookbackDays) * 24 * time.Hour
	// Проверяем, есть ли автобио ИЛИ время последнего обновления валидно И не слишком давно
	needsFullAnalysis := profile.AutoBio == "" || profile.LastAutoBioUpdate.IsZero() || time.Since(profile.LastAutoBioUpdate) > lookbackDuration*2 // Двойной интервал как порог для полного переанализа

	if needsFullAnalysis {
		sinceTime = time.Now().Add(-lookbackDuration)
		if b.config.Debug {
			log.Printf("[AutoBio DEBUG] Chat %d, User %d: Требуется полный/первичный анализ (AutoBio пусто: %t, LastUpdate: %v). Смотрим сообщения с %v",
				chatID, userID, profile.AutoBio == "", profile.LastAutoBioUpdate, sinceTime.Format(time.RFC3339))
		}
	} else {
		sinceTime = profile.LastAutoBioUpdate
		if b.config.Debug {
			log.Printf("[AutoBio DEBUG] Chat %d, User %d: Требуется инкрементальный анализ. Смотрим сообщения с %v", chatID, userID, sinceTime.Format(time.RFC3339))
		}
	}

	// 3. Получаем сообщения пользователя за период
	// Используем контекст с таймаутом, чтобы избежать зависания
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute) // Таймаут на получение сообщений
	defer cancel()

	// Вызываем обновленный GetMessagesSince с userID и лимитом
	allMessagesSince, err := b.storage.GetMessagesSince(ctx, chatID, userID, sinceTime, b.config.AutoBioMaxMessagesForAnalysis)
	if err != nil {
		log.Printf("[AutoBio ERROR] Chat %d, User %d: Ошибка получения сообщений с %v (лимит %d): %v", chatID, userID, sinceTime.Format(time.RFC3339), b.config.AutoBioMaxMessagesForAnalysis, err)
		return err
	}

	// userMessages := []*storage.MongoMessage{} // Больше не нужно

	// 4. Проверяем минимальное количество сообщений
	if len(allMessagesSince) < b.config.AutoBioMinMessagesForAnalysis {
		if b.config.Debug {
			log.Printf("[AutoBio DEBUG] Chat %d, User %d: Найдено %d сообщений (лимит %d), что меньше минимума для анализа (%d). Анализ пропущен.",
				chatID, userID, len(allMessagesSince), b.config.AutoBioMaxMessagesForAnalysis, b.config.AutoBioMinMessagesForAnalysis)
		}
		return nil // Не ошибка, просто пропускаем
	}

	// 5. Форматируем сообщения для LLM
	var messagesTextBuilder strings.Builder
	for _, msg := range allMessagesSince { // Используем напрямую allMessagesSince
		text := msg.Text
		if text == "" {
			text = msg.Caption
		}
		if text != "" { // Добавляем только непустые
			// TODO: Возможно, добавить дату/время сообщения для лучшего контекста?
			messagesTextBuilder.WriteString(text + "\n")
		}
	}
	messagesText := messagesTextBuilder.String()

	// Если после фильтрации пустых сообщений текста не осталось
	if messagesText == "" {
		if b.config.Debug {
			log.Printf("[AutoBio DEBUG] Chat %d, User %d: Не найдено непустых текстовых сообщений для анализа.", chatID, userID)
		}
		return nil
	}

	// 6. Выбираем промпт и готовим данные
	var prompt string
	var llmInputText string
	userDisplayName := profile.Alias // Используем Alias как основное имя для промпта
	if userDisplayName == "" {
		userDisplayName = profile.Username
	}
	if userDisplayName == "" {
		userDisplayName = fmt.Sprintf("User %d", profile.UserID)
	}

	if needsFullAnalysis {
		prompt = b.config.AutoBioInitialAnalysisPrompt
		llmInputText = fmt.Sprintf(prompt, userDisplayName, messagesText)
	} else {
		prompt = b.config.AutoBioUpdatePrompt
		llmInputText = fmt.Sprintf(prompt, userDisplayName, profile.AutoBio, messagesText)
	}

	// 7. Вызываем LLM
	if b.config.Debug {
		log.Printf("[AutoBio DEBUG] Chat %d, User %d: Вызов LLM для %s анализа. Промпт: %s... Input: %s...",
			chatID, userID, map[bool]string{true: "полного", false: "обновления"}[needsFullAnalysis],
			utils.TruncateString(prompt, 50),
			utils.TruncateString(llmInputText, 100))
	}

	// TODO: Добавить контекст в интерфейс LLMClient
	var newBio string // Объявляем переменную до цикла
	maxRetries := 3
	retryDelay := 5 * time.Second
	for i := 0; i < maxRetries; i++ {
		newBio, err = b.llm.GenerateArbitraryResponse("", llmInputText) // Используем = вместо :=
		if err == nil {
			break // Успех, выходим из цикла
		}

		// Проверяем на ошибку rate limit (429)
		if strings.Contains(err.Error(), "429") || strings.Contains(strings.ToLower(err.Error()), "rate limit") {
			log.Printf("[AutoBio WARN] Chat %d, User %d: Ошибка Rate Limit (429) при генерации AutoBio (Попытка %d/%d). Ожидание %v...", chatID, userID, i+1, maxRetries, retryDelay)
			time.Sleep(retryDelay)
			retryDelay *= 2 // Экспоненциальная задержка
		} else {
			// Другая ошибка, прекращаем попытки
			log.Printf("[AutoBio ERROR] Chat %d, User %d: Неисправимая ошибка LLM при генерации AutoBio: %v", chatID, userID, err)
			return err // Возвращаем последнюю ошибку
		}
	}

	// Если после всех попыток ошибка все еще есть (значит, это была 429)
	if err != nil {
		log.Printf("[AutoBio ERROR] Chat %d, User %d: Превышен лимит попыток (%d) для ошибки Rate Limit при генерации AutoBio.", chatID, userID, maxRetries)
		return err // Возвращаем ошибку 429
	}

	if newBio == "" {
		log.Printf("[AutoBio WARN] Chat %d, User %d: LLM вернул пустое AutoBio.", chatID, userID)
		// Можно пропустить обновление или записать как есть
		return nil
	}

	// 8. Обновляем профиль
	profile.AutoBio = strings.TrimSpace(newBio)
	profile.LastAutoBioUpdate = time.Now()

	// 9. Сохраняем профиль
	err = b.storage.SetUserProfile(profile)
	if err != nil {
		log.Printf("[AutoBio ERROR] Chat %d, User %d: Ошибка сохранения обновленного профиля: %v", chatID, userID, err)
		return err
	}

	if b.config.Debug {
		log.Printf("[AutoBio SUCCESS] Chat %d, User %d: Профиль успешно проанализирован и обновлен. Новое AutoBio: %s",
			chatID, userID, utils.TruncateString(profile.AutoBio, 100))
	}

	return nil
}
