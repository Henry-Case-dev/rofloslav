package deepseek

import (
	"context"
	"fmt"
	"log"

	"github.com/Henry-Case-dev/rofloslav/internal/llm" // Импортируем наш интерфейс
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
	ctx := context.Background()

	// Объединяем историю и последнее сообщение для DeepSeek
	allMessages := make([]*tgbotapi.Message, 0, len(history)+1)
	allMessages = append(allMessages, history...)
	if lastMessage != nil { // Добавляем последнее сообщение, если оно не nil
		allMessages = append(allMessages, lastMessage)
	}

	// Подготавливаем историю сообщений для OpenAI формата
	chatMessages := c.prepareChatHistory(systemPrompt, allMessages) // Передаем весь набор

	// Формируем запрос
	req := openai.ChatCompletionRequest{
		Model:    c.modelName,
		Messages: chatMessages,
		// TODO: Добавить возможность конфигурации параметров, если нужно
		Temperature: 1.0,  // Пример значения, можно взять из конфига
		MaxTokens:   8192, // Максимальное значение для deepseek-chat/reasoner
		// TopP: 0.95, // DeepSeek вроде бы не поддерживает TopP так же как OpenAI/Gemini
	}

	if c.debug {
		log.Printf("[DEBUG] DeepSeek Запрос: Модель %s, Сообщений: %d", c.modelName, len(chatMessages))
		// Логируем сами сообщения (осторожно, может быть много текста)
		// for i, msg := range chatMessages {
		// 	log.Printf("[DEBUG] DeepSeek Msg %d: Role=%s, Content=%s...", i, msg.Role, truncateString(msg.Content, 50))
		// }
	}

	resp, err := c.openaiClient.CreateChatCompletion(ctx, req)
	if err != nil {
		if c.debug {
			log.Printf("[DEBUG] DeepSeek Ошибка API: %v", err)
		}
		return "", fmt.Errorf("ошибка вызова DeepSeek API: %w", err)
	}

	if len(resp.Choices) == 0 || resp.Choices[0].Message.Content == "" {
		if c.debug {
			log.Printf("[DEBUG] DeepSeek Ответ: Получен пустой ответ или нет вариантов.")
		}
		return "", fmt.Errorf("DeepSeek не вернул валидный ответ")
	}

	finalResponse := resp.Choices[0].Message.Content
	if c.debug {
		log.Printf("[DEBUG] DeepSeek Ответ: %s...", truncateString(finalResponse, 100))
	}

	return finalResponse, nil
}

// GenerateArbitraryResponse генерирует ответ на основе промпта и контекста
func (c *Client) GenerateArbitraryResponse(systemPrompt string, contextText string) (string, error) {
	ctx := context.Background()

	// Формируем сообщения: системный промпт и контекст как сообщение пользователя
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

	if c.debug {
		log.Printf("[DEBUG] DeepSeek Запрос (Arbitrary): Модель %s", c.modelName)
	}

	resp, err := c.openaiClient.CreateChatCompletion(ctx, req)
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

	finalResponse := resp.Choices[0].Message.Content
	if c.debug {
		log.Printf("[DEBUG] DeepSeek Ответ (Arbitrary): %s...", truncateString(finalResponse, 100))
	}

	return finalResponse, nil
}

// prepareChatHistory преобразует сообщения Telegram в формат OpenAI
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

// GenerateResponseFromTextContext генерирует ответ на основе промпта и готового текстового контекста
func (c *Client) GenerateResponseFromTextContext(systemPrompt string, contextText string) (string, error) {
	ctx := context.Background()

	// Формируем сообщения: системный промпт и контекст как сообщение пользователя
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

	if c.debug {
		log.Printf("[DEBUG] DeepSeek Запрос (Text Context): Модель %s", c.modelName)
	}

	resp, err := c.openaiClient.CreateChatCompletion(ctx, req)
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

	finalResponse := resp.Choices[0].Message.Content
	if c.debug {
		log.Printf("[DEBUG] DeepSeek Ответ (Text Context): %s...", truncateString(finalResponse, 100))
	}

	return finalResponse, nil
}

// Вспомогательная функция для обрезки строки (можно вынести в общий util пакет)
func truncateString(s string, maxLen int) string {
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	if maxLen < 3 {
		return string(runes[:maxLen])
	}
	return string(runes[:maxLen-3]) + "..."
}

// TranscribeAudio для DeepSeek (возвращает ошибку, т.к. не поддерживается)
func (c *Client) TranscribeAudio(audioData []byte, mimeType string) (string, error) {
	return "", fmt.Errorf("транскрибация аудио не поддерживается DeepSeek API")
}
