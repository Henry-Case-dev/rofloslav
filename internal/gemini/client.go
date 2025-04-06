package gemini

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/Henry-Case-dev/rofloslav/internal/config" // Импорт для config.GenerationSettings и др.
	genai "github.com/google/generative-ai-go/genai"
	"google.golang.org/api/option"
	// Убираем неиспользуемый импорт types, если он есть
)

// Client представляет собой клиент для взаимодействия с Gemini API.
type Client struct {
	generativeClient   *genai.Client
	modelName          string
	embeddingModelName string // Добавлено поле для имени модели эмбеддингов
}

// NewClient создает и инициализирует нового клиента Gemini.
// Используем modelName для генерации контента и embeddingModelName для эмбеддингов.
func NewClient(ctx context.Context, apiKey, modelName, embeddingModelName string) (*Client, error) {
	log.Printf("Инициализация клиента Gemini для модели генерации: %s и модели эмбеддингов: %s", modelName, embeddingModelName)
	generativeClient, err := genai.NewClient(ctx, option.WithAPIKey(apiKey))
	if err != nil {
		log.Printf("Ошибка создания клиента Gemini: %v", err)
		return nil, fmt.Errorf("ошибка создания клиента Gemini: %w", err)
	}
	log.Printf("Клиент Gemini успешно создан.")
	return &Client{
		generativeClient:   generativeClient,
		modelName:          modelName,
		embeddingModelName: embeddingModelName, // Сохраняем имя модели эмбеддингов
	}, nil
}

// GetEmbedding получает векторное представление (эмбеддинг) для одного текста.
func (c *Client) GetEmbedding(ctx context.Context, text string) ([]float32, error) {
	if text == "" {
		log.Println("[Gemini WARN] GetEmbedding: Получена пустая строка для эмбеддинга.")
		// Возвращаем ошибку, чтобы вызывающий код знал о проблеме
		return nil, fmt.Errorf("нельзя получить эмбеддинг для пустой строки")
	}
	log.Printf("[Gemini DEBUG] GetEmbedding: Запрос на получение эмбеддинга для текста: \"%s...\"", truncateString(text, 100))

	// Используем GetEmbeddingsBatch для одного элемента
	embeddings, err := c.GetEmbeddingsBatch(ctx, []string{text})
	if err != nil {
		log.Printf("[Gemini ERROR] GetEmbedding: Ошибка при получении эмбеддинга через батч: %v", err)
		return nil, fmt.Errorf("ошибка получения эмбеддинга: %w", err) // Передаем ошибку дальше
	}

	// Проверяем, что вернулся ровно один эмбеддинг
	if len(embeddings) != 1 {
		log.Printf("[Gemini ERROR] GetEmbedding: Ожидался 1 эмбеддинг, получено %d.", len(embeddings))
		return nil, fmt.Errorf("получено неожиданное количество эмбеддингов (%d) для одного текста", len(embeddings))
	}
	if len(embeddings[0]) == 0 {
		log.Printf("[Gemini ERROR] GetEmbedding: Получен пустой эмбеддинг (вектор нулевой длины).")
		return nil, fmt.Errorf("получен пустой эмбеддинг")
	}

	log.Printf("[Gemini DEBUG] GetEmbedding: Успешно получен эмбеддинг размером %d.", len(embeddings[0]))
	return embeddings[0], nil
}

// GetEmbeddingsBatch получает векторные представления (эмбеддинги) для батча текстов.
// Переименовано из GetEmbeddings.
func (c *Client) GetEmbeddingsBatch(ctx context.Context, texts []string) ([][]float32, error) {
	log.Printf("[Gemini DEBUG] GetEmbeddingsBatch: Запрос на получение эмбеддингов для %d текстов.", len(texts))
	if len(texts) == 0 {
		log.Println("[Gemini WARN] GetEmbeddingsBatch: Получен пустой батч текстов.")
		return [][]float32{}, nil
	}

	// Проверка на пустые строки внутри батча
	for i, text := range texts {
		if text == "" {
			log.Printf("[Gemini WARN] GetEmbeddingsBatch: Обнаружена пустая строка в батче по индексу %d.", i)
			// Можно вернуть ошибку или пропустить, но лучше вернуть ошибку, т.к. результат будет неполным/неожиданным.
			return nil, fmt.Errorf("батч содержит пустую строку по индексу %d", i)
		}
	}

	// Используем EmbeddingModel из клиента genai
	em := c.generativeClient.EmbeddingModel(c.embeddingModelName) // Используем правильное имя модели
	batch := em.NewBatch()
	for _, text := range texts {
		batch.AddContent(genai.Text(text))
	}

	// Логируем первый текст для примера (если батч не пустой)
	log.Printf("[Gemini DEBUG] GetEmbeddingsBatch: Пример текста для эмбеддинга: \"%s...\"", truncateString(texts[0], 100))

	res, err := em.BatchEmbedContents(ctx, batch)
	if err != nil {
		// Проверяем на специфичную ошибку квоты
		if strings.Contains(err.Error(), "429") {
			log.Printf("[Gemini ERROR QUOTA] GetEmbeddingsBatch: Достигнута квота API Gemini при получении эмбеддингов: %v", err)
		} else {
			log.Printf("[Gemini ERROR] GetEmbeddingsBatch: Ошибка при получении эмбеддингов: %v", err)
		}
		return nil, fmt.Errorf("ошибка получения эмбеддингов от Gemini: %w", err)
	}

	if res == nil || len(res.Embeddings) != len(texts) {
		log.Printf("[Gemini ERROR] GetEmbeddingsBatch: Получено неожиданное количество эмбеддингов. Ожидалось %d, получено %d.", len(texts), len(res.Embeddings))
		return nil, fmt.Errorf("получено неожиданное количество эмбеддингов (%d) для %d текстов", len(res.Embeddings), len(texts))
	}

	embeddings := make([][]float32, len(texts))
	for i, emb := range res.Embeddings {
		if emb == nil {
			log.Printf("[Gemini ERROR] GetEmbeddingsBatch: Получен nil эмбеддинг для текста %d.", i)
			return nil, fmt.Errorf("получен nil эмбеддинг для текста %d", i)
		}
		if len(emb.Values) == 0 {
			log.Printf("[Gemini ERROR] GetEmbeddingsBatch: Получен пустой эмбеддинг (вектор нулевой длины) для текста %d.", i)
			return nil, fmt.Errorf("получен пустой эмбеддинг для текста %d", i)
		}
		embeddings[i] = emb.Values
	}

	log.Printf("[Gemini DEBUG] GetEmbeddingsBatch: Успешно получено %d эмбеддингов.", len(embeddings))
	return embeddings, nil
}

// GenerateContent генерирует текст на основе промпта и истории сообщений.
// Используем *config.GenerationSettings
func (c *Client) GenerateContent(ctx context.Context, systemPrompt string, history []*genai.Content, lastMessage string, settings *config.GenerationSettings) (string, error) {
	log.Printf("[Gemini DEBUG] GenerateContent: Запрос на генерацию контента. SystemPrompt: \"%s...\", History len: %d, LastMessage: \"%s...\"", truncateString(systemPrompt, 50), len(history), truncateString(lastMessage, 50))

	genaiModel := c.generativeClient.GenerativeModel(c.modelName)

	// Настройки через GenerationConfig
	genaiModel.GenerationConfig = genai.GenerationConfig{}
	if settings != nil {
		if settings.Temperature != nil {
			genaiModel.GenerationConfig.SetTemperature(*settings.Temperature)
		}
		if settings.TopP != nil {
			genaiModel.GenerationConfig.SetTopP(*settings.TopP)
		}
		if settings.TopK != nil {
			genaiModel.GenerationConfig.SetTopK(int32(*settings.TopK))
		}
		if settings.MaxOutputTokens != nil {
			genaiModel.GenerationConfig.SetMaxOutputTokens(int32(*settings.MaxOutputTokens))
		}
		if len(settings.StopSequences) > 0 {
			genaiModel.GenerationConfig.StopSequences = settings.StopSequences
		}
	}

	// Формируем историю для запроса
	var contents []*genai.Content // Используем слайс *genai.Content
	// Если systemPrompt используется, его нужно задать отдельно:
	if systemPrompt != "" {
		genaiModel.SystemInstruction = &genai.Content{Parts: []genai.Part{genai.Text(systemPrompt)}}
	}

	// Добавляем history (предполагаем, что она уже содержит чередование user/model)
	contents = append(contents, history...)

	// Добавляем последнее сообщение пользователя как отдельный Content
	if lastMessage != "" {
		contents = append(contents, &genai.Content{
			Parts: []genai.Part{genai.Text(lastMessage)},
			Role:  "user", // Явно указываем роль
		})
	}

	// Начинаем сессию чата с переданной историей
	cs := genaiModel.StartChat()
	cs.History = contents // Устанавливаем историю сессии

	// Отправляем пустой запрос, чтобы получить ответ модели на основе истории
	resp, err := cs.SendMessage(ctx /* Пустая часть */)

	if err != nil {
		if strings.Contains(err.Error(), "429") {
			log.Printf("[Gemini ERROR QUOTA] GenerateContent: Достигнута квота API Gemini: %v", err)
		} else {
			log.Printf("[Gemini ERROR] GenerateContent: Ошибка генерации контента: %v", err)
		}
		return "", fmt.Errorf("ошибка генерации контента в Gemini: %w", err)
	}

	generatedText := extractTextFromResponse(resp)
	log.Printf("[Gemini DEBUG] GenerateContent: Успешно сгенерирован ответ: \"%s...\"", truncateString(generatedText, 100))

	return generatedText, nil
}

// GenerateArbitraryContent генерирует текст на основе произвольного промпта (без истории).
// Используем *config.ArbitraryGenerationSettings
func (c *Client) GenerateArbitraryContent(ctx context.Context, prompt string, settings *config.ArbitraryGenerationSettings) (string, error) {
	log.Printf("[Gemini DEBUG] GenerateArbitraryContent: Запрос на генерацию. Prompt: \"%s...\"", truncateString(prompt, 100))
	genaiModel := c.generativeClient.GenerativeModel(c.modelName)

	// Настройки через GenerationConfig
	genaiModel.GenerationConfig = genai.GenerationConfig{}
	if settings != nil {
		if settings.Temperature != nil {
			genaiModel.GenerationConfig.SetTemperature(*settings.Temperature)
		}
		if settings.TopP != nil {
			genaiModel.GenerationConfig.SetTopP(*settings.TopP)
		}
		if settings.TopK != nil {
			genaiModel.GenerationConfig.SetTopK(int32(*settings.TopK))
		}
		if settings.MaxOutputTokens != nil {
			genaiModel.GenerationConfig.SetMaxOutputTokens(int32(*settings.MaxOutputTokens))
		}
		if len(settings.StopSequences) > 0 {
			genaiModel.GenerationConfig.StopSequences = settings.StopSequences
		}
	}

	resp, err := genaiModel.GenerateContent(ctx, genai.Text(prompt))
	if err != nil {
		if strings.Contains(err.Error(), "429") {
			log.Printf("[Gemini ERROR QUOTA] GenerateArbitraryContent: Достигнута квота API Gemini: %v", err)
		} else {
			log.Printf("[Gemini ERROR] GenerateArbitraryContent: Ошибка генерации: %v", err)
		}
		return "", fmt.Errorf("ошибка генерации произвольного контента в Gemini: %w", err)
	}

	generatedText := extractTextFromResponse(resp)
	log.Printf("[Gemini DEBUG] GenerateArbitraryContent: Успешно сгенерирован ответ: \"%s...\"", truncateString(generatedText, 100))

	return generatedText, nil
}

// --- Вспомогательные функции ---

// truncateString обрезает строку до maxLen, стараясь не рвать слова.
func truncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	lastSpace := -1
	// Ищем последний пробел, начиная с конца maxLen
	limit := maxLen
	if limit >= len(runes) {
		limit = len(runes) - 1
	}
	for i := limit; i >= 0; i-- { // Итерация до 0
		if runes[i] == ' ' {
			lastSpace = i
			break
		}
	}
	// Обрезаем по последнему пробелу, если он найден и не в самом начале
	if lastSpace > 0 {
		return string(runes[:lastSpace]) + "..."
	}
	// Иначе обрезаем жестко по maxLen
	return string(runes[:maxLen]) + "..."
}

// extractTextFromResponse извлекает текстовый контент из ответа Gemini API.
func extractTextFromResponse(resp *genai.GenerateContentResponse) string {
	var generatedText strings.Builder
	if resp != nil && len(resp.Candidates) > 0 {
		candidate := resp.Candidates[0] // Берем первого кандидата
		if candidate.Content != nil && len(candidate.Content.Parts) > 0 {
			for _, part := range candidate.Content.Parts {
				if textPart, ok := part.(genai.Text); ok {
					generatedText.WriteString(string(textPart))
				} else {
					log.Printf("[Gemini WARN] extractTextFromResponse: Обнаружена нетекстовая часть в ответе: %T", part)
				}
			}
		} else {
			// Логируем, если кандидат есть, но контент пуст
			log.Printf("[Gemini WARN] extractTextFromResponse: Ответ кандидата не содержит контента или частей. FinishReason: %s", candidate.FinishReason)
		}
	} else {
		log.Println("[Gemini WARN] extractTextFromResponse: Ответ пуст или не содержит кандидатов.")
	}
	return generatedText.String()
}

// Close закрывает клиент Gemini.
func (c *Client) Close() error {
	log.Println("[Gemini] Закрытие клиента...")
	err := c.generativeClient.Close()
	if err != nil {
		log.Printf("[Gemini ERROR] Ошибка при закрытии клиента: %v", err)
		return fmt.Errorf("ошибка закрытия клиента Gemini: %w", err)
	}
	log.Println("[Gemini] Клиент успешно закрыт.")
	return nil
}
