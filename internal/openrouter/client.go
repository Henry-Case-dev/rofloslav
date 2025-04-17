package openrouter

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/Henry-Case-dev/rofloslav/internal/config" // Нужен для Debug флага
	"github.com/Henry-Case-dev/rofloslav/internal/llm"    // Импортируем наш интерфейс

	// НЕ ИСПОЛЬЗУЕМ tgbotapi здесь, интерфейс работает с текстом
	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5" // Добавляем импорт
)

// Убедимся, что Client реализует интерфейс llm.LLMClient
var _ llm.LLMClient = (*Client)(nil)

const defaultBaseURL = "https://openrouter.ai/api/v1"

// Client для взаимодействия с OpenRouter API
type Client struct {
	httpClient *http.Client
	apiKey     string
	modelName  string
	baseURL    string
	siteURL    string // Optional HTTP-Referer
	siteTitle  string // Optional X-Title
	debug      bool
}

// New создает нового клиента OpenRouter
func New(apiKey, modelName, siteURL, siteTitle string, cfg *config.Config) (*Client, error) {
	if apiKey == "" {
		return nil, fmt.Errorf("OpenRouter API ключ не предоставлен")
	}
	if modelName == "" {
		return nil, fmt.Errorf("имя модели OpenRouter не предоставлено")
	}

	log.Printf("Клиент OpenRouter инициализирован для модели: %s", modelName)

	return &Client{
		httpClient: &http.Client{Timeout: 120 * time.Second}, // Таймаут 2 минуты
		apiKey:     apiKey,
		modelName:  modelName,
		baseURL:    defaultBaseURL,
		siteURL:    siteURL,
		siteTitle:  siteTitle,
		debug:      cfg.Debug, // Берем флаг из конфига
	}, nil
}

// Close для OpenRouter клиента (в данном случае ничего не делает)
func (c *Client) Close() error {
	return nil
}

// --- Структуры для запроса и ответа OpenRouter (Chat Completions) ---

type ChatCompletionMessage struct {
	Role    string `json:"role"`              // "system", "user", "assistant"
	Content string `json:"content,omitempty"` // Текстовый контент
	// Name    string        `json:"name,omitempty"`    // Опционально
	// ToolCalls []*ToolCall `json:"tool_calls,omitempty"` // Пока не используем
	// ToolCallID string      `json:"tool_call_id,omitempty"` // Пока не используем
}

type ChatCompletionRequest struct {
	Model       string                  `json:"model"`
	Messages    []ChatCompletionMessage `json:"messages"`
	Temperature *float64                `json:"temperature,omitempty"`
	MaxTokens   *int                    `json:"max_tokens,omitempty"`
	TopP        *float64                `json:"top_p,omitempty"`
	// Stream      bool                    `json:"stream,omitempty"` // Пока не используем стриминг
	// Stop        []string                `json:"stop,omitempty"`
	// Seed        *int                    `json:"seed,omitempty"`
	// Tools       []*Tool                 `json:"tools,omitempty"`
	// ToolChoice  interface{}             `json:"tool_choice,omitempty"` // string or ToolChoice
}

type ResponseMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
	// ToolCalls []*ToolCall `json:"tool_calls,omitempty"`
}

type Choice struct {
	Index        int             `json:"index"`
	Message      ResponseMessage `json:"message"`
	FinishReason string          `json:"finish_reason"` // e.g., "stop", "length", "tool_calls"
}

type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

type ChatCompletionResponse struct {
	ID                string   `json:"id"`
	Object            string   `json:"object"`  // "chat.completion"
	Created           int64    `json:"created"` // Unix timestamp
	Model             string   `json:"model"`
	Choices           []Choice `json:"choices"`
	Usage             Usage    `json:"usage"`
	SystemFingerprint *string  `json:"system_fingerprint,omitempty"`
}

// Error Detail structure for OpenRouter errors
type ErrorDetail struct {
	Code    *string `json:"code"` // Can be null
	Message string  `json:"message"`
	Param   *string `json:"param"` // Can be null
	Type    string  `json:"type"`
}

// Error Response structure
type ErrorResponse struct {
	Error ErrorDetail `json:"error"`
}

// --- Реализация методов интерфейса llm.LLMClient ---

// GenerateResponse (DEPRECATED in interface logic) - вызывает GenerateResponseFromTextContext
func (c *Client) GenerateResponse(systemPrompt string, history []*tgbotapi.Message, lastMessage *tgbotapi.Message) (string, error) {
	// Форматируем историю и последнее сообщение в единый текст
	// Используем простой формат для передачи в GenerateResponseFromTextContext
	var contextBuilder strings.Builder
	for _, msg := range history {
		role := "User"
		if msg.From != nil && msg.From.IsBot { // Примитивное определение роли
			role = "Bot"
		}
		text := msg.Text
		if text == "" && msg.Caption != "" {
			text = msg.Caption
		}
		if text != "" {
			contextBuilder.WriteString(fmt.Sprintf("%s: %s\n", role, text))
		}
	}
	// Добавляем последнее сообщение
	if lastMessage != nil {
		role := "User"
		if lastMessage.From != nil && lastMessage.From.IsBot {
			role = "Bot"
		}
		text := lastMessage.Text
		if text == "" && lastMessage.Caption != "" {
			text = lastMessage.Caption
		}
		if text != "" {
			contextBuilder.WriteString(fmt.Sprintf("%s: %s\n", role, text)) // Добавляем последнее сообщение как user
		} else {
			contextBuilder.WriteString("User: [сообщение без текста]\n")
		}
	}

	// Вызываем основной метод
	return c.GenerateResponseFromTextContext(systemPrompt, contextBuilder.String())
}

// GenerateResponseFromTextContext генерирует ответ на основе промпта и готового текстового контекста
func (c *Client) GenerateResponseFromTextContext(systemPrompt string, contextText string) (string, error) {
	// Формируем сообщения для OpenRouter
	messages := []ChatCompletionMessage{}
	if systemPrompt != "" {
		messages = append(messages, ChatCompletionMessage{Role: "system", Content: systemPrompt})
	}
	messages = append(messages, ChatCompletionMessage{Role: "user", Content: contextText})

	// Настройки (можно вынести в конфиг или параметры)
	temp := 1.0
	// maxTokens := 8192 // OpenRouter не всегда корректно обрабатывает max_tokens, лучше не ставить или ставить с запасом
	// topP := 0.95 // Можно добавить

	requestPayload := ChatCompletionRequest{
		Model:       c.modelName,
		Messages:    messages,
		Temperature: &temp,
		// MaxTokens: &maxTokens, // Осторожно использовать
		// TopP:        &topP,
	}

	return c.sendRequest(requestPayload)
}

// GenerateArbitraryResponse генерирует ответ для задач без истории (саммари, анализ срача)
func (c *Client) GenerateArbitraryResponse(systemPrompt string, contextText string) (string, error) {
	// Используем ту же логику, что и GenerateResponseFromTextContext
	return c.GenerateResponseFromTextContext(systemPrompt, contextText)
}

// sendRequest - внутренняя функция для отправки запроса к OpenRouter API
func (c *Client) sendRequest(payload ChatCompletionRequest) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Second) // Таймаут 2.5 минуты
	defer cancel()

	jsonData, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("ошибка маршалинга JSON для OpenRouter: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/chat/completions", bytes.NewBuffer(jsonData))
	if err != nil {
		return "", fmt.Errorf("ошибка создания HTTP запроса для OpenRouter: %w", err)
	}

	// Установка заголовков
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")
	if c.siteURL != "" {
		req.Header.Set("HTTP-Referer", c.siteURL)
	}
	if c.siteTitle != "" {
		req.Header.Set("X-Title", c.siteTitle)
	}

	if c.debug {
		log.Printf("[DEBUG] OpenRouter Запрос: URL=%s, Модель=%s", req.URL.String(), payload.Model)
		// Логирование тела запроса может быть объемным, делаем это осторожно
		// log.Printf("[DEBUG] OpenRouter Запрос Тело: %s", string(jsonData))
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("ошибка выполнения HTTP запроса к OpenRouter: %w", err)
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("ошибка чтения тела ответа OpenRouter: %w", err)
	}

	if c.debug {
		log.Printf("[DEBUG] OpenRouter Ответ: Статус=%s", resp.Status)
		// Логирование тела ответа может быть объемным
		// log.Printf("[DEBUG] OpenRouter Ответ Тело: %s", string(bodyBytes))
	}

	// Проверка статуса ответа
	if resp.StatusCode != http.StatusOK {
		// Попытка распарсить тело ошибки
		var errorResp ErrorResponse
		if json.Unmarshal(bodyBytes, &errorResp) == nil && errorResp.Error.Message != "" {
			log.Printf("[ERROR] OpenRouter API Error: Type=%s, Code=%v, Message=%s",
				errorResp.Error.Type, errorResp.Error.Code, errorResp.Error.Message)
			// Особая обработка 429 Rate Limit
			if resp.StatusCode == http.StatusTooManyRequests {
				return "[Лимит]", fmt.Errorf("OpenRouter API Rate Limit (429): %s", errorResp.Error.Message)
			}
			return "", fmt.Errorf("ошибка OpenRouter API (%d): %s", resp.StatusCode, errorResp.Error.Message)
		}
		// Если не удалось распарсить ошибку, возвращаем общую ошибку
		return "", fmt.Errorf("ошибка OpenRouter API: статус %s, тело: %s", resp.Status, string(bodyBytes))
	}

	// Парсинг успешного ответа
	var successResp ChatCompletionResponse
	if err := json.Unmarshal(bodyBytes, &successResp); err != nil {
		return "", fmt.Errorf("ошибка парсинга успешного ответа OpenRouter: %w. Тело: %s", err, string(bodyBytes))
	}

	// Проверка наличия ответа
	if len(successResp.Choices) == 0 || successResp.Choices[0].Message.Content == "" {
		if c.debug {
			log.Printf("[DEBUG] OpenRouter Ответ: Получен пустой ответ или нет вариантов.")
		}
		// Проверяем FinishReason
		finishReason := "unknown"
		if len(successResp.Choices) > 0 {
			finishReason = successResp.Choices[0].FinishReason
		}
		log.Printf("[WARN] OpenRouter вернул пустой ответ. FinishReason: %s", finishReason)

		// Если заблокировано по safety/content filter
		if strings.Contains(strings.ToLower(finishReason), "content_filter") {
			return "[Заблокировано]", fmt.Errorf("ответ заблокирован OpenRouter (content_filter)")
		}

		return "", fmt.Errorf("OpenRouter не вернул валидный ответ (choices пуст или content пуст)")
	}

	finalResponse := successResp.Choices[0].Message.Content
	if c.debug {
		usage := successResp.Usage
		log.Printf("[DEBUG] OpenRouter Ответ: Успешно. Токены: Prompt=%d, Completion=%d, Total=%d. FinishReason: %s",
			usage.PromptTokens, usage.CompletionTokens, usage.TotalTokens, successResp.Choices[0].FinishReason)
		// log.Printf("[DEBUG] OpenRouter Ответ Текст: %s...", truncateString(finalResponse, 100))
	}

	// Проверка на блокировку по safety/content filter в finish_reason
	if strings.Contains(strings.ToLower(successResp.Choices[0].FinishReason), "content_filter") {
		log.Printf("[WARN] OpenRouter Ответ заблокирован: Причина=%s", successResp.Choices[0].FinishReason)
		return "[Заблокировано]", fmt.Errorf("ответ заблокирован OpenRouter: %s", successResp.Choices[0].FinishReason)
	}

	return finalResponse, nil
}

// TranscribeAudio для OpenRouter (возвращает ошибку, не стандартная функция)
func (c *Client) TranscribeAudio(audioData []byte, mimeType string) (string, error) {
	// OpenRouter может поддерживать транскрипцию через Whisper или другие модели,
	// но это требует другого эндпоинта (/audio/transcriptions) и структуры запроса.
	// В рамках Chat Completions это не сделать.
	return "", fmt.Errorf("транскрибация аудио через OpenRouter /chat/completions не поддерживается")
}

// EmbedContent для OpenRouter (возвращает ошибку, не стандартная функция)
func (c *Client) EmbedContent(text string) ([]float32, error) {
	// OpenRouter может предоставлять доступ к моделям эмбеддингов,
	// но это требует другого эндпоинта (/embeddings) и структуры запроса.
	return nil, fmt.Errorf("генерация эмбеддингов через OpenRouter /chat/completions не поддерживается")
}

// Вспомогательная функция для обрезки строки
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
