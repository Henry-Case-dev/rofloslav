package gemini

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/google/generative-ai-go/genai"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/option"
)

var botUserID int64

// Client для взаимодействия с Gemini API
type Client struct {
	genaiClient        *genai.Client
	modelName          string
	embeddingModelName string
	debug              bool
}

// New создает и инициализирует новый клиент Gemini.
// Принимает API ключ, имя основной модели, имя модели для эмбеддингов и флаг отладки.
func New(apiKey, modelName, embeddingModelName string, debug bool) (*Client, error) {
	if apiKey == "" {
		return nil, fmt.Errorf("API ключ Gemini не предоставлен")
	}
	if modelName == "" {
		return nil, fmt.Errorf("имя модели Gemini не предоставлено")
	}

	// Получаем ID бота из переменной окружения
	botIDStr := os.Getenv("BOT_USER_ID")
	if botIDStr != "" {
		var err error
		botUserID, err = strconv.ParseInt(botIDStr, 10, 64)
		if err != nil {
			log.Printf("[WARN] Не удалось преобразовать BOT_USER_ID ('%s') в int64: %v", botIDStr, err)
		} else {
			log.Printf("[INFO] ID бота загружен из переменной окружения: %d", botUserID)
		}
	} else {
		log.Printf("[WARN] Переменная окружения BOT_USER_ID не установлена. Определение сообщений бота может быть неточным.")
	}

	ctx := context.Background()
	genaiClient, err := genai.NewClient(ctx, option.WithAPIKey(apiKey))
	if err != nil {
		return nil, fmt.Errorf("ошибка создания клиента genai: %w", err)
	}

	log.Printf("Клиент Gemini инициализирован для модели: %s", modelName)

	return &Client{
		genaiClient:        genaiClient,
		modelName:          modelName,
		embeddingModelName: embeddingModelName,
		debug:              debug,
	}, nil
}

// Close закрывает клиент Gemini
func (c *Client) Close() error {
	if c.genaiClient != nil {
		return c.genaiClient.Close()
	}
	return nil
}

// GenerateResponse генерирует ответ с использованием Gemini API
// history - сообщения ДО lastMessage
// lastMessage - сообщение, на которое отвечаем
func (c *Client) GenerateResponse(systemPrompt string, history []*tgbotapi.Message, lastMessage *tgbotapi.Message) (string, error) {
	ctx := context.Background()
	model := c.genaiClient.GenerativeModel(c.modelName)

	// Настройки модели
	model.SetTemperature(1)
	model.SetTopP(0.95)
	model.SetMaxOutputTokens(8192)

	// Устанавливаем SystemInstruction
	if systemPrompt != "" {
		model.SystemInstruction = &genai.Content{
			Parts: []genai.Part{genai.Text(systemPrompt)},
		}
		if c.debug {
			log.Printf("[DEBUG] Gemini Запрос: Установлен SystemInstruction: %s...", truncateString(systemPrompt, 100))
		}
	}

	// Начинаем чат сессию
	session := model.StartChat()

	// Формируем историю для API (только history, без lastMessage)
	preparedHistory := c.prepareChatHistory(history) // Функция теперь принимает только историю ДО последнего сообщения

	// Устанавливаем подготовленную историю
	session.History = preparedHistory

	// Формируем контент для отправки из lastMessage
	var contentToSend genai.Part
	lastMessageText := "" // Текст последнего сообщения
	if lastMessage != nil {
		lastMessageText = lastMessage.Text // Используем основной текст
		if lastMessageText == "" && lastMessage.Caption != "" {
			lastMessageText = lastMessage.Caption // Или caption
		}
	}

	if lastMessageText != "" {
		contentToSend = genai.Text(lastMessageText)
		if c.debug {
			log.Printf("[DEBUG] Gemini Запрос: Текст lastMessage для отправки: %s...", truncateString(lastMessageText, 50))
		}
	} else {
		// Если lastMessage пустой (например, стикер или медиа без текста/caption), что отправлять?
		// Отправка пустого текста после истории все еще может вызвать 400.
		// Возможно, стоит передать плейсхолдер или описание медиа, если это важно.
		// Пока отправляем плейсхолдер, чтобы избежать пустой строки.
		contentToSend = genai.Text("[сообщение без текста]")
		if c.debug {
			log.Printf("[DEBUG] Gemini Запрос: lastMessage был пуст, отправляется плейсхолдер.")
		}
	}

	if c.debug {
		log.Printf("[DEBUG] Gemini Запрос: Подготовленная история содержит %d сообщений.", len(preparedHistory))
		log.Printf("[DEBUG] Gemini Запрос: Модель %s, Temp: %+v, TopP: %+v, MaxTokens: %+v",
			c.modelName, model.Temperature, model.TopP, model.MaxOutputTokens)
	}

	// Отправляем сообщение
	resp, err := session.SendMessage(ctx, contentToSend)
	if err != nil {
		if c.debug {
			log.Printf("[DEBUG] Gemini Ошибка отправки: %v", err)
		}
		return "", fmt.Errorf("ошибка отправки сообщения в Gemini: %w", err)
	}

	// Извлекаем ответ
	var responseText strings.Builder
	if len(resp.Candidates) > 0 && resp.Candidates[0].Content != nil {
		for _, part := range resp.Candidates[0].Content.Parts {
			responseText.WriteString(fmt.Sprintf("%s", part))
		}
	} else {
		if c.debug {
			log.Printf("[DEBUG] Gemini Ответ: Получен пустой ответ или нет кандидатов.")
		}
		return "", fmt.Errorf("Gemini не вернул валидный ответ")
	}

	finalResponse := responseText.String()
	if c.debug {
		log.Printf("[DEBUG] Gemini Ответ: %s...", truncateString(finalResponse, 100))
	}

	return finalResponse, nil
}

// prepareChatHistory подготавливает историю сообщений для Gemini API.
// Конвертирует и объединяет роли.
// НЕ включает последнее сообщение, т.к. оно передается отдельно.
func (c *Client) prepareChatHistory(messages []*tgbotapi.Message) []*genai.Content {
	if len(messages) == 0 {
		return []*genai.Content{}
	}

	// 1. Конвертируем все сообщения в genai.Content с ролями
	fullHistoryWithRoles := []*genai.Content{}
	for _, msg := range messages {
		content := c.convertMessageToGenaiContent(msg)
		if content != nil {
			fullHistoryWithRoles = append(fullHistoryWithRoles, content)
		}
	}

	if len(fullHistoryWithRoles) == 0 {
		return []*genai.Content{}
	}

	// 2. Объединяем последовательные сообщения с одинаковой ролью
	mergedHistory := []*genai.Content{fullHistoryWithRoles[0]}
	for i := 1; i < len(fullHistoryWithRoles); i++ {
		lastMerged := mergedHistory[len(mergedHistory)-1]
		current := fullHistoryWithRoles[i]

		if current.Role == lastMerged.Role {
			// Объединяем текст
			var combinedText strings.Builder
			for _, p := range lastMerged.Parts {
				combinedText.WriteString(fmt.Sprintf("%s", p))
			}
			combinedText.WriteString("\n") // Добавляем разделитель
			for _, p := range current.Parts {
				combinedText.WriteString(fmt.Sprintf("%s", p))
			}
			// Обновляем Parts последнего элемента в mergedHistory
			lastMerged.Parts = []genai.Part{genai.Text(combinedText.String())}
		} else {
			// Если роли разные, просто добавляем
			mergedHistory = append(mergedHistory, current)
		}
	}

	// 3. Убедимся, что история не заканчивается сообщением модели (если она не пустая)
	//    Если заканчивается, API может ожидать сообщение пользователя.
	//    Однако, передавая lastMessage отдельно, это должно решиться.
	//    Просто возвращаем объединенную историю.

	if c.debug {
		log.Printf("[DEBUG][prepareChatHistory] Исходных сообщений для истории: %d", len(messages))
		log.Printf("[DEBUG][prepareChatHistory] Сообщений после конвертации: %d", len(fullHistoryWithRoles))
		log.Printf("[DEBUG][prepareChatHistory] Финальная история для API после слияния содержит %d сообщений.", len(mergedHistory))
	}

	return mergedHistory
}

// convertMessageToGenaiContent преобразует одно сообщение Telegram
func (c *Client) convertMessageToGenaiContent(msg *tgbotapi.Message) *genai.Content {
	if msg == nil {
		return nil
	}
	// Считаем и пустые сообщения, если они не от бота (важно для сохранения чередования)
	// if msg.Text == "" && (msg.From == nil || !msg.From.IsBot){
	// 	return nil // Пропускаем пустые сообщения от пользователей (или без отправителя)
	// }

	// Определяем текст сообщения, учитывая подписи к медиа
	textContent := msg.Text
	if textContent == "" && msg.Caption != "" {
		textContent = msg.Caption // Используем подпись, если текста нет
	}

	// Если текста все равно нет, пропускаем сообщение (или можно вернуть пустой контент?)
	// Пока пропускаем, чтобы не отправлять пустоту в API
	if textContent == "" {
		return nil
	}

	role := "user"
	// Проверяем ID бота, если он загружен
	if botUserID != 0 {
		if msg.From != nil && msg.From.ID == botUserID {
			role = "model"
		}
	} else if msg.From != nil && msg.From.IsBot {
		// Fallback на IsBot, если ID не загружен
		role = "model"
	}

	return &genai.Content{
		Parts: []genai.Part{genai.Text(textContent)}, // Используем textContent
		Role:  role,
	}
}

// GenerateArbitraryResponse генерирует ответ на основе системного промпта и произвольного текстового контекста
func (c *Client) GenerateArbitraryResponse(systemPrompt string, contextText string) (string, error) {
	ctx := context.Background()
	model := c.genaiClient.GenerativeModel(c.modelName)

	// Используем те же настройки модели, что и в GenerateResponse
	model.SetTemperature(1)
	// Убираем TopK
	// model.SetTopK(40)
	model.SetTopP(0.95)
	model.SetMaxOutputTokens(8192)

	// Устанавливаем системный промпт через специальное поле
	if systemPrompt != "" {
		model.SystemInstruction = &genai.Content{
			Parts: []genai.Part{genai.Text(systemPrompt)},
		}
		if c.debug {
			log.Printf("[DEBUG] Gemini Запрос (Arbitrary): Установлен SystemInstruction: %s...", truncateString(systemPrompt, 100))
		}
	}

	// Убираем формирование fullPrompt
	// fullPrompt := systemPrompt + "\n\nКонтекст для анализа:\n" + contextText
	contentToSend := genai.Text(contextText) // Формируем контент только из contextText

	if c.debug {
		log.Printf("[DEBUG] Gemini Запрос (Arbitrary): Текст для отправки: %s...", truncateString(contextText, 150))
		log.Printf("[DEBUG] Gemini Запрос (Arbitrary): Модель %s, Temp: %v, TopP: %v, MaxTokens: %v",
			c.modelName, model.Temperature, model.TopP, model.MaxOutputTokens)
	}

	// Отправляем сообщение (без истории чата), только контекст
	resp, err := model.GenerateContent(ctx, contentToSend)
	if err != nil {
		if c.debug {
			log.Printf("[DEBUG] Gemini Ошибка генерации (Arbitrary): %v", err)
		}
		return "", fmt.Errorf("ошибка генерации контента в Gemini: %w", err)
	}

	// Извлекаем ответ (аналогично GenerateResponse)
	var responseText strings.Builder
	if resp != nil && len(resp.Candidates) > 0 && resp.Candidates[0].Content != nil {
		for _, part := range resp.Candidates[0].Content.Parts {
			responseText.WriteString(fmt.Sprintf("%s", part))
		}
	} else {
		if c.debug {
			log.Printf("[DEBUG] Gemini Ответ (Arbitrary): Получен пустой ответ или нет кандидатов.")
		}
		return "", fmt.Errorf("Gemini не вернул валидный ответ")
	}

	finalResponse := responseText.String()
	if c.debug {
		log.Printf("[DEBUG] Gemini Ответ (Arbitrary): %s...", truncateString(finalResponse, 100))
	}

	return finalResponse, nil
}

// GenerateResponseFromTextContext генерирует ответ на основе промпта и готового текстового контекста
func (c *Client) GenerateResponseFromTextContext(systemPrompt string, contextText string) (string, error) {
	ctx := context.Background()
	model := c.genaiClient.GenerativeModel(c.modelName)

	// Настраиваем генерацию
	model.SetTemperature(1.0)      // Можно сделать настраиваемым
	model.SetMaxOutputTokens(8192) // Максимум для Gemini 1.5 Flash/Pro

	// Формируем контент для Gemini: системный промпт и контекст как единый текст
	fullText := systemPrompt + "\n\n---\n\n" + contextText
	part := genai.Text(fullText)

	if c.debug {
		log.Printf("[DEBUG] Gemini Запрос (Text Context): Модель %s", c.modelName)
	}

	resp, err := model.GenerateContent(ctx, part) // Передаем одну часть
	if err != nil {
		log.Printf("[ERROR] Gemini Ошибка API (Text Context): %v", err)
		// Добавляем парсинг специфичной ошибки Gemini, если возможно
		if genErr, ok := err.(*googleapi.Error); ok { // <-- Убедимся, что google.golang.org/api/googleapi импортирован
			log.Printf("[ERROR] Gemini API Error Details: Code=%d, Message=%s", genErr.Code, genErr.Message)
			// Проверка на Blocked prompt
			if strings.Contains(genErr.Message, "blocked") || strings.Contains(genErr.Message, "SAFETY") {
				log.Printf("[WARN] Gemini Запрос заблокирован (Safety/Policy): %s", genErr.Message)
				return "[Заблокировано]", fmt.Errorf("запрос заблокирован политикой безопасности: %w", err)
			}
			// Проверка на Rate limit (429)
			if genErr.Code == 429 {
				log.Printf("[WARN] Gemini Достигнут лимит запросов (429): %s", genErr.Message)
				return "[Лимит]", fmt.Errorf("достигнут лимит запросов Gemini: %w", err)
			}
		}
		return "", fmt.Errorf("ошибка генерации ответа Gemini (Text Context): %w", err)
	}

	if len(resp.Candidates) == 0 || resp.Candidates[0].Content == nil || len(resp.Candidates[0].Content.Parts) == 0 {
		log.Println("[WARN] Gemini Ответ (Text Context): Получен пустой ответ или нет валидных частей.")
		return "", fmt.Errorf("Gemini вернул пустой ответ (Text Context)")
	}

	// Собираем текст из всех частей ответа
	var responseText strings.Builder
	for _, part := range resp.Candidates[0].Content.Parts {
		if text, ok := part.(genai.Text); ok {
			responseText.WriteString(string(text))
		}
	}

	finalResponse := responseText.String()

	if c.debug {
		finishReason := resp.Candidates[0].FinishReason
		tokenCount := resp.Candidates[0].TokenCount
		log.Printf("[DEBUG] Gemini Ответ (Text Context): Причина завершения: %s, Токенов: %d", finishReason, tokenCount)
		log.Printf("[DEBUG] Gemini Ответ (Text Context): %s...", truncateString(finalResponse, 100))
	}

	if resp.Candidates[0].FinishReason == genai.FinishReasonSafety || resp.Candidates[0].FinishReason == genai.FinishReasonRecitation {
		log.Printf("[WARN] Gemini Ответ заблокирован: Причина=%s", resp.Candidates[0].FinishReason)
		return "[Заблокировано]", fmt.Errorf("ответ заблокирован Gemini: %s", resp.Candidates[0].FinishReason)
	}

	return finalResponse, nil
}

// truncateString обрезает строку до указанной длины, добавляя многоточие
func truncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen]
}

// TranscribeAudio транскрибирует аудиоданные с помощью Gemini API
func (c *Client) TranscribeAudio(audioData []byte, mimeType string) (string, error) {
	// Проверяем, поддерживает ли модель аудио (хотя бы по названию)
	if !strings.Contains(c.modelName, "1.5") {
		log.Printf("[WARN][TranscribeAudio] Текущая модель '%s' может не поддерживать аудио. Для транскрибации рекомендуется Gemini 1.5 Flash/Pro.", c.modelName)
		// Можно либо вернуть ошибку, либо попытаться использовать 1.5 Flash по умолчанию
		// return "", fmt.Errorf("модель %s не поддерживает транскрибацию аудио", c.modelName)
	}

	ctx := context.Background()
	// Используем модель, указанную при инициализации клиента, или 1.5 Flash как fallback
	// model := c.genaiClient.GenerativeModel(c.modelName)
	// TODO: Решить, как обрабатывать модели без поддержки аудио. Пока оставляем текущую.
	model := c.genaiClient.GenerativeModel(c.modelName)

	if c.debug {
		log.Printf("[DEBUG][TranscribeAudio] Используется модель: %s, MIME-тип: %s, Размер данных: %d байт", c.modelName, mimeType, len(audioData))
	}

	// Формируем запрос с аудиоданными и простым промптом для транскрибации
	prompt := genai.Text("Transcribe this audio:")
	audioPart := genai.Blob{MIMEType: mimeType, Data: audioData}

	resp, err := model.GenerateContent(ctx, prompt, audioPart)
	if err != nil {
		if c.debug {
			log.Printf("[DEBUG][TranscribeAudio] Ошибка API Gemini: %v", err)
		}
		return "", fmt.Errorf("ошибка транскрибации аудио в Gemini: %w", err)
	}

	// Извлекаем текст из ответа
	var transcript strings.Builder
	if resp != nil && len(resp.Candidates) > 0 && resp.Candidates[0].Content != nil {
		for _, part := range resp.Candidates[0].Content.Parts {
			if textPart, ok := part.(genai.Text); ok {
				transcript.WriteString(string(textPart))
			}
		}
	} else {
		if c.debug {
			log.Println("[DEBUG][TranscribeAudio] Gemini не вернул валидный ответ или текст.")
		}
		return "", fmt.Errorf("Gemini не вернул текст транскрибации")
	}

	finalTranscript := transcript.String()
	if c.debug {
		log.Printf("[DEBUG][TranscribeAudio] Успешная транскрибация: %s...", truncateString(finalTranscript, 100))
	}

	return finalTranscript, nil
}

// EmbedContent генерирует векторное представление (эмбеддинг) для текста с использованием Gemini API.
func (c *Client) EmbedContent(text string) ([]float32, error) {
	ctx := context.Background()
	em := c.genaiClient.EmbeddingModel(c.embeddingModelName) // Используем модель из конфига
	if em == nil {
		return nil, fmt.Errorf("модель эмбеддингов '%s' не найдена или не инициализирована", c.embeddingModelName)
	}

	if c.debug {
		log.Printf("[DEBUG] Gemini Embed Запрос: Модель %s, Текст: %s...", c.embeddingModelName, truncateString(text, 50))
	}

	res, err := em.EmbedContent(ctx, genai.Text(text))
	if err != nil {
		if c.debug {
			log.Printf("[DEBUG] Gemini Embed Ошибка API: %v", err)
		}
		// Попытка извлечь более детальную ошибку из googleapi.Error
		var gerr *googleapi.Error
		if errors.As(err, &gerr) {
			// Если ошибка связана с квотой (429 Too Many Requests)
			if gerr.Code == 429 {
				log.Printf("[WARN] Gemini Embed API: Достигнут лимит запросов (429 Too Many Requests) для модели %s.", c.embeddingModelName)
				return nil, fmt.Errorf("ошибка API Gemini (лимит запросов): %w", err)
			}
		}
		return nil, fmt.Errorf("ошибка API Gemini при генерации эмбеддинга: %w", err)
	}

	if res.Embedding == nil || len(res.Embedding.Values) == 0 {
		if c.debug {
			log.Printf("[DEBUG] Gemini Embed Ответ: Получен пустой эмбеддинг.")
		}
		return nil, fmt.Errorf("API Gemini вернул пустой эмбеддинг")
	}

	if c.debug {
		log.Printf("[DEBUG] Gemini Embed Ответ: Получен эмбеддинг размерности %d", len(res.Embedding.Values))
	}

	return res.Embedding.Values, nil
}
