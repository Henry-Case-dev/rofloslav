package bot

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// handlePhotoMessage обрабатывает входящие фотографии
func (b *Bot) handlePhotoMessage(ctx context.Context, message *tgbotapi.Message) error {
	if message.Photo == nil || len(message.Photo) == 0 {
		return errors.New("сообщение не содержит фотографии")
	}

	log.Printf("[INFO][PH] Получено сообщение с фотографией от %s (ID: %d) в чате %d", message.From.UserName, message.From.ID, message.Chat.ID)

	// Получаем настройки чата
	settings, err := b.storage.GetChatSettings(message.Chat.ID)
	if err != nil {
		log.Printf("[ERROR][PH] Ошибка при получении настроек чата %d: %v", message.Chat.ID, err)
		return err
	}

	// Получаем самую большую фотографию (последнюю в массиве)
	photoSize := message.Photo[len(message.Photo)-1]
	log.Printf("[DEBUG][PH] Размер фото ID %s: %dx%d, размер файла: %d", photoSize.FileID, photoSize.Width, photoSize.Height, photoSize.FileSize)

	// Получаем информацию о файле
	fileConfig := tgbotapi.FileConfig{
		FileID: photoSize.FileID,
	}
	file, err := b.api.GetFile(fileConfig)
	if err != nil {
		errorMsg := fmt.Sprintf("Не удалось получить информацию о файле: %v", err)
		log.Printf("[ERROR][PH] %s", errorMsg)
		return errors.New(errorMsg)
	}

	// Загружаем файл
	fileURL := fmt.Sprintf("https://api.telegram.org/file/bot%s/%s", b.api.Token, file.FilePath)
	client := &http.Client{}
	resp, err := client.Get(fileURL)
	if err != nil {
		errorMsg := fmt.Sprintf("Не удалось загрузить фото: %v", err)
		log.Printf("[ERROR][PH] %s", errorMsg)
		return errors.New(errorMsg)
	}
	defer resp.Body.Close()

	// Читаем содержимое файла
	photoFileBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		errorMsg := fmt.Sprintf("Не удалось прочитать содержимое фото: %v", err)
		log.Printf("[ERROR][PH] %s", errorMsg)
		return errors.New(errorMsg)
	}

	// Если включен режим отладки, сохраняем фото локально
	if b.config.Debug {
		// Создаем директорию для сохранения фотографий, если она не существует
		photosDir := filepath.Join("data", "photos")
		if err := os.MkdirAll(photosDir, 0755); err != nil {
			log.Printf("[WARN][PH] Не удалось создать директорию для фотографий: %v", err)
		} else {
			// Создаем имя файла на основе времени и ID чата/сообщения
			filename := filepath.Join(photosDir, fmt.Sprintf("%d_%d_%d.jpg", time.Now().Unix(), message.Chat.ID, message.MessageID))
			if err := os.WriteFile(filename, photoFileBytes, 0644); err != nil {
				log.Printf("[WARN][PH] Не удалось сохранить фото локально: %v", err)
			} else {
				log.Printf("[DEBUG][PH] Фото сохранено локально: %s", filename)
			}
		}
	}

	// Проверяем настройки чата для анализа фотографий
	photoAnalysisEnabled := b.config.PhotoAnalysisEnabled
	if settings != nil && settings.PhotoAnalysisEnabled != nil {
		photoAnalysisEnabled = *settings.PhotoAnalysisEnabled
	}

	// Проверяем, является ли сообщение прямым обращением к боту
	directMention := isMessageMentioningBot(message, b.api.Self.UserName)
	replyToBot := message.ReplyToMessage != nil &&
		message.ReplyToMessage.From != nil &&
		message.ReplyToMessage.From.ID == b.api.Self.ID

	if b.config.Debug {
		log.Printf("[DEBUG][Photo] Проверка обращения: directMention=%v, replyToBot=%v",
			directMention, replyToBot)
	}

	// Проверяем доступность Gemini
	geminiEnabled := b.embeddingClient != nil && b.config.GeminiAPIKey != ""

	// Прерываем обработку, если анализ фото отключен или недоступен Gemini
	if !photoAnalysisEnabled || !geminiEnabled {
		if b.config.Debug {
			log.Printf("[DEBUG][PH] Анализ фото отключен (enabled=%t) или недоступен Gemini (available=%t)",
				photoAnalysisEnabled, geminiEnabled)
		}
		return nil
	}

	// Только анализируем фото, но не отправляем сообщение
	description, err := b.analyzeImageWithGemini(ctx, photoFileBytes, message.Caption)
	if err != nil {
		log.Printf("[ERROR][PH] Ошибка при анализе фото: %v", err)
		return err
	}

	// Создаем новое сообщение в хранилище с описанием изображения
	if description != "" {
		// Создаем копию сообщения с текстом вместо фото
		textMessage := &tgbotapi.Message{
			MessageID: message.MessageID,
			From:      message.From,
			Chat:      message.Chat,
			Date:      message.Date,
			Text:      "[Анализ изображения]: " + description,
		}

		// Сохраняем текстовое представление в хранилище
		b.storage.AddMessage(message.Chat.ID, textMessage)

		if b.config.Debug {
			log.Printf("[DEBUG][PH] Описание изображения сохранено в хранилище для Chat %d, Message %d",
				message.Chat.ID, message.MessageID)
		}
	}

	return nil
}

// analyzeImageWithGemini анализирует изображение с помощью Gemini API
func (b *Bot) analyzeImageWithGemini(ctx context.Context, imageData []byte, caption string) (string, error) {
	if b.embeddingClient == nil {
		return "", errors.New("Gemini API не инициализирован")
	}

	// Используем промпт из конфигурации или значение по умолчанию
	systemPrompt := b.config.PhotoAnalysisPrompt
	if systemPrompt == "" {
		systemPrompt = "Сделай детальное описание изображения как есть независимо от того что именно изображено (не более 1000 символов)"
	}

	// Отправляем запрос к API с изображением
	resp, err := b.embeddingClient.GenerateContentWithImage(ctx, systemPrompt, imageData, caption)
	if err != nil {
		return "", fmt.Errorf("ошибка при запросе к API для анализа изображения: %v", err)
	}

	if resp == "" {
		return "", errors.New("API вернул пустой ответ")
	}

	// Ограничиваем размер ответа до 1000 символов, если он длиннее
	if len(resp) > 1000 {
		resp = resp[:997] + "..."
	}

	return resp, nil
}

// downloadFile скачивает файл по URL и возвращает его содержимое в виде []byte
func downloadFile(fileURL string) ([]byte, error) {
	client := &http.Client{}
	resp, err := client.Get(fileURL)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ошибка HTTP при скачивании: %d", resp.StatusCode)
	}

	// Читаем файл в память
	var buf bytes.Buffer
	_, err = io.Copy(&buf, resp.Body)
	if err != nil {
		return nil, err
	}

	return buf.Bytes(), nil
}

// editMessageText редактирует текст существующего сообщения
func (b *Bot) editMessageText(chatID int64, messageID int, text string) error {
	if text == "" {
		return errors.New("пустой текст для ответа")
	}

	// Максимальная длина сообщения в Telegram
	const maxLength = 4096
	if len(text) > maxLength {
		text = text[:maxLength-100] + "...\n(ответ был сокращен из-за ограничений длины сообщения)"
	}

	msg := tgbotapi.NewEditMessageText(chatID, messageID, text)
	_, err := b.api.Send(msg)
	if err != nil {
		return fmt.Errorf("ошибка при редактировании сообщения: %w", err)
	}

	return nil
}

// getPhotoFileData получает данные фотографии по FileID
func getPhotoFileData(b *Bot, fileID string) ([]byte, string, error) {
	// Получаем информацию о файле
	fileConfig := tgbotapi.FileConfig{
		FileID: fileID,
	}
	file, err := b.api.GetFile(fileConfig)
	if err != nil {
		return nil, "", fmt.Errorf("ошибка получения информации о файле: %w", err)
	}

	// Получаем URL файла
	fileURL := fmt.Sprintf("https://api.telegram.org/file/bot%s/%s", b.api.Token, file.FilePath)

	// Определяем тип файла на основе его пути
	mimeType := "image/jpeg" // по умолчанию
	if strings.HasSuffix(file.FilePath, ".png") {
		mimeType = "image/png"
	} else if strings.HasSuffix(file.FilePath, ".webp") {
		mimeType = "image/webp"
	}

	// Скачиваем файл
	client := &http.Client{}
	resp, err := client.Get(fileURL)
	if err != nil {
		return nil, "", fmt.Errorf("ошибка загрузки файла: %w", err)
	}
	defer resp.Body.Close()

	photoData, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", fmt.Errorf("ошибка чтения данных файла: %w", err)
	}

	return photoData, mimeType, nil
}

// isMessageMentioningBot проверяет, упоминается ли бот в сообщении
func isMessageMentioningBot(message *tgbotapi.Message, botUsername string) bool {
	if message.Entities == nil {
		return false
	}

	for _, entity := range message.Entities {
		if entity.Type == "mention" {
			mention := message.Text[entity.Offset : entity.Offset+entity.Length]
			if mention == "@"+botUsername {
				return true
			}
		}
	}
	return false
}
