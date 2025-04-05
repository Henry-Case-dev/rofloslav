package gemini

import (
	"context"
	"fmt"
	"log"
	"strings"

	// "github.com/Henry-Case-dev/rofloslav/internal/storage" // Удаляем импорт storage
	"github.com/Henry-Case-dev/rofloslav/internal/types" // Добавляем импорт types
	"github.com/google/generative-ai-go/genai"
	"google.golang.org/api/option"
)

// Client для взаимодействия с Gemini API
type Client struct {
	genaiClient *genai.Client
	modelName   string
	debug       bool
}

// New создает нового клиента Gemini
func New(apiKey, modelName string, debug bool) (*Client, error) {
	ctx := context.Background()
	genaiClient, err := genai.NewClient(ctx, option.WithAPIKey(apiKey))
	if err != nil {
		return nil, fmt.Errorf("ошибка создания клиента genai: %w", err)
	}

	log.Printf("Клиент Gemini инициализирован для модели: %s", modelName)

	return &Client{
		genaiClient: genaiClient,
		modelName:   modelName,
		debug:       debug,
	}, nil
}

// Close закрывает клиент Gemini
func (c *Client) Close() error {
	if c.genaiClient != nil {
		return c.genaiClient.Close()
	}
	return nil
}

// GenerateResponse генерирует ответ с использованием Gemini API, используя types.Message
func (c *Client) GenerateResponse(systemPrompt string, messages []types.Message) (string, error) {
	ctx := context.Background()
	model := c.genaiClient.GenerativeModel(c.modelName)

	// Настройки модели (можно вынести в конфиг при необходимости)
	model.SetTemperature(1)
	model.SetTopP(0.95)
	model.SetMaxOutputTokens(8192)
	// model.ResponseMIMEType = "text/plain" // Можно установить, если нужен только текст

	// Начинаем чат сессию
	session := model.StartChat()

	// Формируем историю для API из types.Message
	history, lastMessageText := c.prepareChatHistory(messages)
	session.History = history

	// Формируем текст запроса: системный промпт + последнее сообщение
	fullPrompt := systemPrompt
	if lastMessageText != "" {
		fullPrompt += "\n\n" + lastMessageText
	}

	if c.debug {
		log.Printf("[DEBUG] Gemini Запрос: Полный промпт (system + last message) = %s...", truncateString(fullPrompt, 100))
		log.Printf("[DEBUG] Gemini Запрос: История содержит %d сообщений.", len(session.History))
		log.Printf("[DEBUG] Gemini Запрос: Модель %s, Temp: %v, TopP: %v, MaxTokens: %v",
			c.modelName, model.Temperature, model.TopP, model.MaxOutputTokens)
	}

	// Отправляем сообщение
	resp, err := session.SendMessage(ctx, genai.Text(fullPrompt))
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
			responseText.WriteString(fmt.Sprintf("%s", part)) // Преобразуем part в строку
		}
	} else {
		if c.debug {
			log.Printf("[DEBUG] Gemini Ответ: Получен пустой ответ или нет кандидатов.")
		}
		// Можно вернуть пустую строку или ошибку в зависимости от логики
		return "", fmt.Errorf("Gemini не вернул валидный ответ")
	}

	finalResponse := responseText.String()
	if c.debug {
		log.Printf("[DEBUG] Gemini Ответ: %s...", truncateString(finalResponse, 100))
	}

	return finalResponse, nil
}

// prepareChatHistory преобразует []types.Message в историю для Gemini API
func (c *Client) prepareChatHistory(messages []types.Message) ([]*genai.Content, string) {
	history := []*genai.Content{}
	var lastMessageText string

	if len(messages) == 0 {
		return history, ""
	}

	// Обрабатываем все сообщения, кроме последнего
	processUpToIndex := len(messages) - 1
	for i := 0; i < processUpToIndex; i++ {
		msg := messages[i]
		content := c.convertStorageMessageToGenaiContent(msg)
		if content != nil {
			history = append(history, content)
		}
	}

	// Сохраняем текст последнего сообщения отдельно
	lastMsg := messages[len(messages)-1]
	if lastMsg.Text != "" {
		role := lastMsg.Role
		if role == "" {
			role = "user"
		}

		if role == "model" {
			content := c.convertStorageMessageToGenaiContent(lastMsg)
			if content != nil {
				history = append(history, content)
			}
		} else {
			lastMessageText = lastMsg.Text
		}

	}

	// Очистка истории от последовательных сообщений с одинаковой ролью (рекомендация Gemini)
	if len(history) > 1 {
		cleanedHistory := []*genai.Content{history[0]}
		for i := 1; i < len(history); i++ {
			if history[i].Role != cleanedHistory[len(cleanedHistory)-1].Role {
				cleanedHistory = append(cleanedHistory, history[i])
			} else {
				// Объединяем текст, если роли совпадают
				lastContent := cleanedHistory[len(cleanedHistory)-1]
				var combinedText strings.Builder
				for _, p := range lastContent.Parts {
					combinedText.WriteString(fmt.Sprintf("%s", p))
				}
				combinedText.WriteString("\n") // Добавляем разделитель
				for _, p := range history[i].Parts {
					combinedText.WriteString(fmt.Sprintf("%s", p))
				}
				lastContent.Parts = []genai.Part{genai.Text(combinedText.String())}
			}
		}
		history = cleanedHistory
	}

	return history, lastMessageText
}

// convertStorageMessageToGenaiContent преобразует одно types.Message
func (c *Client) convertStorageMessageToGenaiContent(msg types.Message) *genai.Content {
	if msg.Text == "" {
		return nil
	}

	role := msg.Role
	if role == "" {
		role = "user"
	}
	if role != "user" && role != "model" {
		log.Printf("[WARN] Недопустимая роль '%s' в types.Message ID %d. Использую 'user'.", role, msg.ID)
		role = "user"
	}

	return &genai.Content{
		Parts: []genai.Part{genai.Text(msg.Text)},
		Role:  role,
	}
}

// GenerateArbitraryResponse генерирует ответ на основе системного промпта и произвольного текстового контекста
func (c *Client) GenerateArbitraryResponse(systemPrompt string, contextText string) (string, error) {
	ctx := context.Background()
	model := c.genaiClient.GenerativeModel(c.modelName)

	// Используем те же настройки модели, что и в GenerateResponse
	model.SetTemperature(1)
	model.SetTopP(0.95)
	model.SetMaxOutputTokens(8192)

	// Формируем полный промпт
	// В этом случае мы не используем историю чата, а передаем всё как один большой промпт.
	// Системный промпт идет первым, затем контекст.
	fullPrompt := systemPrompt + "\n\nКонтекст для анализа:\n" + contextText

	if c.debug {
		log.Printf("[DEBUG] Gemini Запрос (Arbitrary): Полный промпт = %s...", truncateString(fullPrompt, 150))
		log.Printf("[DEBUG] Gemini Запрос (Arbitrary): Модель %s, Temp: %v, TopP: %v, MaxTokens: %v",
			c.modelName, model.Temperature, model.TopP, model.MaxOutputTokens)
	}

	// Отправляем сообщение (без истории чата)
	resp, err := model.GenerateContent(ctx, genai.Text(fullPrompt))
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

// --- НОВАЯ ФУНКЦИЯ для Эмбеддингов ---

// GetEmbedding получает векторное представление (эмбеддинг) для заданного текста.
func (c *Client) GetEmbedding(text string) ([]float32, error) {
	ctx := context.Background()
	// Используем модель, специально предназначенную для эмбеддингов.
	// "embedding-001" - стандартная модель Google для этого.
	// Можно сделать имя модели для эмбеддингов настраиваемым в config, если потребуется.
	embeddingModelName := "embedding-001"
	em := c.genaiClient.EmbeddingModel(embeddingModelName)

	if c.debug {
		log.Printf("[DEBUG] Gemini GetEmbedding: Используется модель '%s' для текста: %s...", embeddingModelName, truncateString(text, 50))
	}

	// Используем EmbeddingModel
	res, err := em.EmbedContent(ctx, genai.Text(text))
	if err != nil {
		if c.debug {
			log.Printf("[DEBUG] Gemini Ошибка получения эмбеддинга: %v, Текст: %s...", err, truncateString(text, 50))
		}
		return nil, fmt.Errorf("ошибка получения эмбеддинга от Gemini (%s): %w", embeddingModelName, err)
	}

	if res == nil || res.Embedding == nil || len(res.Embedding.Values) == 0 {
		if c.debug {
			log.Printf("[DEBUG] Gemini Пустой эмбеддинг для текста: %s...", truncateString(text, 50))
		}
		// Возвращаем ошибку, чтобы не передавать пустой вектор в Qdrant
		return nil, fmt.Errorf("получен пустой эмбеддинг от Gemini (%s)", embeddingModelName)
	}

	if c.debug {
		log.Printf("[DEBUG] Gemini Эмбеддинг получен. Размерность: %d, Текст: %s...", len(res.Embedding.Values), truncateString(text, 50))
	}

	return res.Embedding.Values, nil
}

// --- Конец новой функции ---

// Вспомогательная функция для обрезки строки
func truncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen]
}
