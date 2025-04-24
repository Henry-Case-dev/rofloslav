package deepseek

import (
	"context"
	"errors"
	"fmt"
	"log"

	"github.com/Henry-Case-dev/rofloslav/internal/llm"
	"github.com/Henry-Case-dev/rofloslav/internal/utils" // <--- Добавляем импорт
	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	openai "github.com/sashabaranov/go-openai"
)

// Убедимся, что Client реализует интерфейс llm.LLMClient
var _ llm.LLMClient = (*Client)(nil)

// Client для взаимодействия с DeepSeek API (через OpenAI совместимый интерфейс)
type Client struct {
	openaiClient *openai.Client
	modelName    string
	debug        bool
}

// New создает нового клиента DeepSeek
func New(apiKey, modelName, baseURL string, debug bool) (*Client, error) {
	if apiKey == "" {
		return nil, fmt.Errorf("DeepSeek API ключ не предоставлен")
	}
	if modelName == "" {
		return nil, fmt.Errorf("имя модели DeepSeek не предоставлено")
	}

	config := openai.DefaultConfig(apiKey)
	if baseURL != "" {
		config.BaseURL = baseURL // Используем кастомный URL, если он задан
	} else {
		config.BaseURL = "https://api.deepseek.com/v1" // URL по умолчанию
	}

	openaiClient := openai.NewClientWithConfig(config)

	log.Printf("Клиент DeepSeek инициализирован для модели: %s (BaseURL: %s)", modelName, config.BaseURL)

	return &Client{
		openaiClient: openaiClient,
		modelName:    modelName,
		debug:        debug,
	}, nil
}

// Close для DeepSeek клиента (в данном случае ничего не делает)
func (c *Client) Close() error {
	// Клиент go-openai не требует явного закрытия
	return nil
}

// GenerateResponse генерирует ответ с использованием DeepSeek API
func (c *Client) GenerateResponse(systemPrompt string, history []*tgbotapi.Message, lastMessage *tgbotapi.Message) (string, error) {
	if c.debug {
		log.Printf("[DEBUG] DeepSeek Запрос: SystemPrompt: %s...", utils.TruncateString(systemPrompt, 100))
		log.Printf("[DEBUG] DeepSeek Запрос: LastMessage: %s...", utils.TruncateString(lastMessage.Text, 50))
		log.Printf("[DEBUG] DeepSeek Запрос: Модель %s", c.modelName)
	}

	// Преобразование истории в формат OpenAI
	chatMessages := c.prepareChatHistory(systemPrompt, history)

	// Добавляем последнее сообщение пользователя
	chatMessages = append(chatMessages, openai.ChatCompletionMessage{
		Role:    openai.ChatMessageRoleUser,
		Content: lastMessage.Text,
	})

	// Вызов API
	resp, err := c.openaiClient.CreateChatCompletion(
		context.Background(),
		openai.ChatCompletionRequest{
			Model:    c.modelName,
			Messages: chatMessages,
		},
	)

	if err != nil {
		log.Printf("[ERROR] DeepSeek Ошибка API: %v", err)
		return "", fmt.Errorf("ошибка вызова DeepSeek API: %w", err)
	}

	if len(resp.Choices) > 0 {
		response := resp.Choices[0].Message.Content
		if c.debug {
			log.Printf("[DEBUG] DeepSeek Ответ: %s...", utils.TruncateString(response, 100))
		}
		return response, nil
	}

	log.Printf("[ERROR] DeepSeek: Пустой ответ от API.")
	return "", errors.New("deepseek: пустой ответ от API")
}

// GenerateArbitraryResponse генерирует ответ на основе системного промпта и произвольного текстового контекста.
func (c *Client) GenerateArbitraryResponse(systemPrompt string, contextText string) (string, error) {
	if c.debug {
		log.Printf("[DEBUG] DeepSeek Запрос (Arbitrary): SystemPrompt: %s...", utils.TruncateString(systemPrompt, 100))
		log.Printf("[DEBUG] DeepSeek Запрос (Arbitrary): ContextText: %s...", utils.TruncateString(contextText, 150))
		log.Printf("[DEBUG] DeepSeek Запрос (Arbitrary): Модель %s", c.modelName)
	}

	// Формируем сообщение для API
	chatMessages := []openai.ChatCompletionMessage{
		{
			Role:    openai.ChatMessageRoleSystem,
			Content: systemPrompt,
		},
		{
			Role:    openai.ChatMessageRoleUser,
			Content: contextText,
		},
	}

	req := openai.ChatCompletionRequest{
		Model:       c.modelName,
		Messages:    chatMessages,
		Temperature: 1.0, // Можно сделать настраиваемым
		MaxTokens:   8192,
	}

	resp, err := c.openaiClient.CreateChatCompletion(context.Background(), req)
	if err != nil {
		if c.debug {
			log.Printf("[DEBUG] DeepSeek Ошибка API (Arbitrary): %v", err)
		}
		return "", fmt.Errorf("ошибка вызова DeepSeek API (Arbitrary): %w", err)
	}

	if len(resp.Choices) == 0 || resp.Choices[0].Message.Content == "" {
		if c.debug {
			log.Printf("[DEBUG] DeepSeek Ответ (Arbitrary): Получен пустой ответ или нет вариантов.")
		}
		return "", fmt.Errorf("DeepSeek не вернул валидный ответ (Arbitrary)")
	}

	response := resp.Choices[0].Message.Content
	if c.debug {
		log.Printf("[DEBUG] DeepSeek Ответ (Arbitrary): %s...", utils.TruncateString(response, 100))
	}

	return response, nil
}

// prepareChatHistory преобразует историю сообщений Telegram в формат OpenAI
func (c *Client) prepareChatHistory(systemPrompt string, messages []*tgbotapi.Message) []openai.ChatCompletionMessage {
	openAiMessages := make([]openai.ChatCompletionMessage, 0, len(messages)+1)

	// Добавляем системный промпт первым
	if systemPrompt != "" {
		openAiMessages = append(openAiMessages, openai.ChatCompletionMessage{
			Role:    openai.ChatMessageRoleSystem,
			Content: systemPrompt,
		})
	}

	for _, msg := range messages {
		if msg == nil || msg.Text == "" {
			continue
		}

		role := openai.ChatMessageRoleUser
		// Считаем сообщения от нашего бота как 'assistant'
		// Важно: Используем ID бота из API, а не просто IsBot, если возможно
		// Пока предполагаем, что IsBot достаточно для определения сообщений бота
		if msg.From != nil && msg.From.IsBot { // <-- Уточнить эту логику при необходимости
			role = openai.ChatMessageRoleAssistant
		}

		openAiMessages = append(openAiMessages, openai.ChatCompletionMessage{
			Role:    role,
			Content: msg.Text,
		})
	}

	// TODO: Добавить логику слияния сообщений с одинаковой ролью подряд, если DeepSeek этого требует.
	// Пока оставляем как есть.

	return openAiMessages
}

// GenerateResponseFromTextContext генерирует ответ на основе системного промпта и предварительно отформатированного текстового контекста.
func (c *Client) GenerateResponseFromTextContext(systemPrompt string, contextText string) (string, error) {
	if c.debug {
		log.Printf("[DEBUG] DeepSeek Запрос (Text Context): SystemPrompt: %s...", utils.TruncateString(systemPrompt, 100))
		log.Printf("[DEBUG] DeepSeek Запрос (Text Context): ContextText: %s...", utils.TruncateString(contextText, 150))
		log.Printf("[DEBUG] DeepSeek Запрос (Text Context): Модель %s", c.modelName)
	}

	// Формируем сообщение для API
	chatMessages := []openai.ChatCompletionMessage{
		{
			Role:    openai.ChatMessageRoleSystem,
			Content: systemPrompt,
		},
		{
			Role:    openai.ChatMessageRoleUser,
			Content: contextText,
		},
	}

	req := openai.ChatCompletionRequest{
		Model:       c.modelName,
		Messages:    chatMessages,
		Temperature: 1.0, // Можно сделать настраиваемым
		MaxTokens:   8192,
	}

	resp, err := c.openaiClient.CreateChatCompletion(context.Background(), req)
	if err != nil {
		if c.debug {
			log.Printf("[DEBUG] DeepSeek Ошибка API (Text Context): %v", err)
		}
		return "", fmt.Errorf("ошибка вызова DeepSeek API (Text Context): %w", err)
	}

	if len(resp.Choices) == 0 || resp.Choices[0].Message.Content == "" {
		if c.debug {
			log.Printf("[DEBUG] DeepSeek Ответ (Text Context): Получен пустой ответ или нет вариантов.")
		}
		return "", fmt.Errorf("DeepSeek не вернул валидный ответ (Text Context)")
	}

	response := resp.Choices[0].Message.Content
	if c.debug {
		log.Printf("[DEBUG] DeepSeek Ответ (Text Context): %s...", utils.TruncateString(response, 100))
	}

	return response, nil
}

// TranscribeAudio - Заглушка, DeepSeek через OpenAI SDK не поддерживает напрямую транскрипцию
func (c *Client) TranscribeAudio(audioData []byte, mimeType string) (string, error) {
	return "", fmt.Errorf("транскрибация аудио не поддерживается DeepSeek API")
}

// EmbedContent реализация для интерфейса, но DeepSeek не поддерживает эмбеддинги
func (c *Client) EmbedContent(text string) ([]float32, error) {
	if c.debug {
		log.Printf("[DEBUG] DeepSeek не поддерживает эмбеддинги.")
	}
	return nil, errors.New("DeepSeek не поддерживает эмбеддинги")
}

// GenerateContentWithImage реализация для интерфейса, но DeepSeek не поддерживает обработку изображений
func (c *Client) GenerateContentWithImage(ctx context.Context, systemPrompt string, imageData []byte, caption string) (string, error) {
	if c.debug {
		log.Printf("[DEBUG] DeepSeek не поддерживает обработку изображений.")
	}
	return "", errors.New("DeepSeek не поддерживает обработку изображений")
}
