package bot

import (
	"fmt"
	"io"
	"log"
	"net/http"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// handleVoiceMessage обрабатывает входящее голосовое сообщение: скачивает, транскрибирует,
// форматирует, ОТПРАВЛЯЕТ В ЧАТ и сохраняет текстовую версию. Возвращает только ошибку.
func (b *Bot) handleVoiceMessage(message *tgbotapi.Message) error {
	chatID := message.Chat.ID
	messageID := message.MessageID
	userID := message.From.ID

	log.Printf("[INFO][VoiceHandler] Chat %d: Handling voice message ID %d from user %d", chatID, messageID, userID)

	// 0. Проверяем, включена ли транскрипция для этого чата
	settings, err := b.storage.GetChatSettings(chatID)
	if err != nil {
		log.Printf("[ERROR][VoiceHandler] Chat %d: Ошибка получения настроек чата: %v", chatID, err)
		return fmt.Errorf("ошибка получения настроек чата: %w", err)
	}
	voiceEnabled := b.config.VoiceTranscriptionEnabledDefault
	if settings.VoiceTranscriptionEnabled != nil {
		voiceEnabled = *settings.VoiceTranscriptionEnabled
	}
	if !voiceEnabled {
		log.Printf("Chat %d: Транскрипция голоса отключена в настройках.", chatID)
		return nil
	}

	// --- Конец проверки ---
	// Удалена переменная startDownloadTime
	// 1. Получаем FileID голосового сообщения
	fileID := message.Voice.FileID
	log.Printf("Chat %d: Получено голосовое сообщение с FileID: %s", chatID, fileID)

	// 2. Получаем информацию о файле для скачивания
	fileConfig := tgbotapi.FileConfig{FileID: fileID}
	fileInfo, err := b.api.GetFile(fileConfig)
	if err != nil {
		log.Printf("Chat %d: Ошибка получения информации о файле %s: %v", chatID, fileID, err)
		return fmt.Errorf("ошибка получения информации о файле: %w", err)
	}
	log.Printf("Chat %d: Информация о файле получена: Path=%s, Size=%d", chatID, fileInfo.FilePath, fileInfo.FileSize)

	// 3. Формируем URL для скачивания
	fileURL := fileInfo.Link(b.api.Token)
	log.Printf("Chat %d: URL для скачивания файла: %s", chatID, fileURL)

	// 4. Скачиваем файл
	resp, err := http.Get(fileURL)
	if err != nil {
		log.Printf("Chat %d: Ошибка скачивания файла %s: %v", chatID, fileID, err)
		return fmt.Errorf("ошибка скачивания файла: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body) // Читаем тело для лога
		log.Printf("Chat %d: Не удалось скачать файл %s, статус: %d, тело: %s", chatID, fileID, resp.StatusCode, string(bodyBytes))
		return fmt.Errorf("не удалось скачать файл, статус: %d", resp.StatusCode)
	}

	// 5. Читаем содержимое файла
	voiceData, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Printf("Chat %d: Ошибка чтения тела ответа для файла %s: %v", chatID, fileID, err)
		return fmt.Errorf("ошибка чтения тела ответа: %w", err)
	}
	log.Printf("Chat %d: Файл %s успешно скачан (%d байт).", chatID, fileID, len(voiceData))

	// 6. Пытаемся транскрибировать аудио
	mimeType := message.Voice.MimeType // Используем MIME-тип из сообщения
	if mimeType == "" {
		mimeType = "audio/ogg" // Значение по умолчанию, если MIME-тип не указан
		log.Printf("Chat %d: MIME-тип не указан для файла %s, используется '%s'", chatID, fileID, mimeType)
	}

	log.Printf("Chat %d: Запуск транскрибации для файла %s (MIME: %s)...", chatID, fileID, mimeType)
	// Используем КЛИЕНТ ДЛЯ ЭМБЕДДИНГОВ (Gemini) для транскрипции!
	transcribedText, err := b.embeddingClient.TranscribeAudio(voiceData, mimeType)
	if err != nil {
		// Логируем ошибку транскрипции, указывая, что она произошла через embeddingClient
		log.Printf("Chat %d: Ошибка транскрибации аудио из файла %s через embeddingClient (Gemini): %v", chatID, fileID, err)
		errMsg := fmt.Sprintf("❌ Ошибка транскрибации (Gemini): %v", err)
		log.Printf("[ERROR] %s", errMsg)
		b.sendReply(message.Chat.ID, errMsg)
		return fmt.Errorf("ошибка транскрипции (Gemini): %w", err)
	}

	if transcribedText == "" {
		log.Printf("[WARN][VoiceHandler] Chat %d: Получена пустая транскрипция для сообщения %d. (File: %s)", chatID, messageID, fileID)
		// Не отправляем сообщение пользователю, просто игнорируем
		return nil
	}

	log.Printf("[INFO][VoiceHandler] Chat %d: Voice message %d transcribed: \"%s\"", chatID, messageID, transcribedText)

	// 7. Форматируем текст с помощью LLM (расстановка знаков препинания, абзацы)
	formattedText := transcribedText // Используем исходный текст как fallback
	if b.config.VoiceFormatPrompt != "" {
		log.Printf("[DEBUG][VoiceHandler] Chat %d: Formatting transcribed text using LLM (%s)...", chatID, b.config.LLMProvider)
		// Используем основной b.llm для форматирования
		formatted, errFormat := b.llm.GenerateArbitraryResponse(b.config.VoiceFormatPrompt, transcribedText)
		if errFormat != nil {
			log.Printf("[WARN][VoiceHandler] Chat %d: Ошибка форматирования текста LLM (%s): %v. Используем неформатированный текст.", chatID, b.config.LLMProvider, errFormat)
			// Ошибку форматирования не считаем критичной, используем исходный текст
		} else if formatted != "" {
			formattedText = formatted // Используем отформатированный текст
			log.Printf("[INFO][VoiceHandler] Chat %d: Text formatted by LLM: \"%s\"", chatID, formattedText)
		} else {
			log.Printf("[WARN][VoiceHandler] Chat %d: LLM (%s) вернул пустой результат форматирования. Используем неформатированный текст.", chatID, b.config.LLMProvider)
		}
	} else {
		log.Printf("[DEBUG][VoiceHandler] Chat %d: VoiceFormatPrompt пуст, пропускаем форматирование LLM.", chatID)
	}

	// 8. Формируем текст для отправки в чат

	// --- Получаем алиас пользователя ---
	userAlias := "Кто-то" // Дефолтное значение
	if message.From != nil {
		// Сначала пытаемся получить профиль из хранилища
		userProfile, errProfile := b.storage.GetUserProfile(chatID, message.From.ID)
		if errProfile != nil {
			log.Printf("[WARN][VoiceHandler] Chat %d: Ошибка получения профиля для UserID %d: %v. Используем данные из сообщения.", chatID, message.From.ID, errProfile)
		} else if userProfile != nil && userProfile.Alias != "" {
			userAlias = userProfile.Alias
		} else {
			// Если профиля нет или алиас пуст, используем FirstName или UserName
			if message.From.FirstName != "" {
				userAlias = message.From.FirstName
			} else if message.From.UserName != "" {
				userAlias = message.From.UserName
			}
		}
	}
	// --- Конец получения алиаса ---

	// Форматируем текст с алиасом и курсивом
	finalText := fmt.Sprintf("🗣️ (%s) базарит:\n_%s_", userAlias, formattedText)

	// 9. ОТПРАВЛЯЕМ отформатированное сообщение в чат
	// Используем sendReplyMarkdown для поддержки курсива
	b.sendReplyMarkdown(chatID, finalText)
	log.Printf("[INFO][VoiceHandler] Chat %d: Sent formatted transcription for message %d.", chatID, messageID)

	// 10. Сохраняем оригинальное сообщение (с флагом IsVoice) в БД (это делается в handleMessage)
	// Вызов AddMessage здесь УДАЛЕН.

	// 11. Возвращаем nil, так как сообщение успешно обработано и отправлено
	return nil
}
