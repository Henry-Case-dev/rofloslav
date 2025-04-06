package storage

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"log"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/Henry-Case-dev/rofloslav/internal/config"
	"github.com/Henry-Case-dev/rofloslav/internal/gemini"
	"github.com/Henry-Case-dev/rofloslav/internal/types"
	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/google/uuid"
	"github.com/qdrant/go-client/qdrant"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
)

// QdrantStorage реализует HistoryStorage с использованием Qdrant.
type QdrantStorage struct {
	client         qdrant.PointsClient // Клиент для операций с точками
	collectionName string
	timeout        time.Duration
	geminiClient   *gemini.Client // Клиент Gemini для получения эмбеддингов
	debug          bool
	// НОВЫЙ ПОЛЕ: Размер чанка для импорта
	importChunkSize int
	// Мьютекс не нужен для операций с Qdrant, но может понадобиться для внутренних кешей, если они будут
	// mutex          sync.RWMutex
}

// Payload для хранения в Qdrant вместе с вектором
type MessagePayload struct {
	ChatID         int64  `json:"chat_id"`
	MessageID      int    `json:"message_id"` // Используем как часть первичного ключа для поиска/удаления
	UserID         int64  `json:"user_id,omitempty"`
	UserName       string `json:"user_name,omitempty"`
	FirstName      string `json:"first_name,omitempty"`
	IsBot          bool   `json:"is_bot,omitempty"`
	Text           string `json:"text"`
	Date           int    `json:"date"` // Unix timestamp
	ReplyToMsgID   int    `json:"reply_to_msg_id,omitempty"`
	Entities       []byte `json:"entities,omitempty"`         // Сериализуем как JSON []byte
	IsSrachTrigger bool   `json:"is_srach_trigger,omitempty"` // Можно добавить доп. метаданные
	ImportSource   string `json:"import_source"`              // Источник импорта ("live", "batch_old")
	UniqueID       string `json:"unique_id"`                  // Уникальный ID сообщения (chat_id + message_id)
	Role           string `json:"role,omitempty"`             // Роль отправителя ("user", "model")
}

// NewQdrantStorage создает новый экземпляр QdrantStorage.
func NewQdrantStorage(cfg *config.Config, geminiClient *gemini.Client) (*QdrantStorage, error) {
	log.Printf("[QdrantStorage] Инициализация клиента Qdrant для эндпоинта: %s, коллекция: %s", cfg.QdrantEndpoint, cfg.QdrantCollection)

	// --- Подключение к Qdrant ---
	parsedURL, err := url.Parse(cfg.QdrantEndpoint)
	if err != nil {
		log.Printf("[QdrantStorage ERROR] Не удалось разобрать Qdrant Endpoint URL '%s': %v", cfg.QdrantEndpoint, err)
		return nil, fmt.Errorf("неверный формат Qdrant Endpoint URL: %w", err)
	}

	host := parsedURL.Hostname()
	port := parsedURL.Port()
	scheme := parsedURL.Scheme

	// Определяем порт gRPC по умолчанию, если не указан
	if port == "" {
		if scheme == "https" {
			port = "6334" // Qdrant Cloud обычно использует 6334 для gRPC с TLS
		} else {
			port = "6334" // Стандартный gRPC порт Qdrant без TLS
		}
	}
	qdrantAddr := fmt.Sprintf("%s:%s", host, port)
	log.Printf("[QdrantStorage] Адрес для gRPC подключения: %s", qdrantAddr)

	// Определяем параметры подключения (TLS, API Key)
	var dialOpts []grpc.DialOption
	if scheme == "https" {
		log.Println("[QdrantStorage] Используется TLS для подключения к Qdrant.")
		dialOpts = append(dialOpts, grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{
			// Можно добавить InsecureSkipVerify: true, если сертификат самоподписанный (не рекомендуется для продакшена)
			// InsecureSkipVerify: true,
		})))
	} else {
		log.Println("[QdrantStorage] Используется небезопасное подключение (без TLS).")
		dialOpts = append(dialOpts, grpc.WithTransportCredentials(insecure.NewCredentials()))
	}

	// Добавляем API Key, если он задан
	if cfg.QdrantAPIKey != "" {
		log.Println("[QdrantStorage] Используется API Key для аутентификации.")
		dialOpts = append(dialOpts, grpc.WithUnaryInterceptor(func(ctx context.Context, method string, req, reply interface{}, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
			// Добавляем API ключ в метаданные каждого запроса
			md := metadata.New(map[string]string{"api-key": cfg.QdrantAPIKey})
			ctx = metadata.NewOutgoingContext(ctx, md)
			return invoker(ctx, method, req, reply, cc, opts...)
		}))
		// Примечание: Для потоковых RPC (если будут использоваться) потребуется StreamInterceptor
	}

	// Подключаемся с определенными опциями
	conn, err := grpc.Dial(qdrantAddr, dialOpts...)
	if err != nil {
		log.Printf("[QdrantStorage ERROR] Не удалось подключиться к Qdrant (%s): %v", qdrantAddr, err)
		return nil, fmt.Errorf("ошибка подключения к Qdrant gRPC (%s): %w", qdrantAddr, err)
	}
	// defer conn.Close() // Закрытие соединения будет при остановке бота

	pointsClient := qdrant.NewPointsClient(conn)
	collectionsClient := qdrant.NewCollectionsClient(conn) // Нужен для проверки/создания коллекции

	timeout := time.Duration(cfg.QdrantTimeoutSec) * time.Second

	// --- Проверка/Создание Коллекции ---
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	// Добавляем API ключ в контекст для запроса List, если он используется
	listCtx := ctx
	if cfg.QdrantAPIKey != "" {
		md := metadata.New(map[string]string{"api-key": cfg.QdrantAPIKey})
		listCtx = metadata.NewOutgoingContext(ctx, md)
	}

	collectionExists := false
	listCollectionsRequest := &qdrant.ListCollectionsRequest{}
	resp, err := collectionsClient.List(listCtx, listCollectionsRequest) // Используем listCtx
	if err != nil {
		log.Printf("[QdrantStorage ERROR] Ошибка получения списка коллекций: %v", err)
		conn.Close() // Закрываем соединение при ошибке инициализации
		return nil, fmt.Errorf("ошибка получения списка коллекций Qdrant: %w", err)
	}
	for _, collection := range resp.GetCollections() {
		if collection.GetName() == cfg.QdrantCollection {
			collectionExists = true
			log.Printf("[QdrantStorage] Коллекция '%s' найдена.", cfg.QdrantCollection)
			break
		}
	}

	if !collectionExists {
		log.Printf("[QdrantStorage] Коллекция '%s' не найдена. Попытка создания...", cfg.QdrantCollection)
		// Получим размерность, сгенерировав эмбеддинг для тестовой строки.
		// Важно: Убедитесь, что Gemini клиент уже инициализирован и работает.
		testEmbeddings, err := geminiClient.GetEmbeddingsBatch(ctx, []string{"test"})
		if err != nil {
			log.Printf("[QdrantStorage ERROR] Не удалось получить тестовый эмбеддинг для определения размерности: %v", err)
			return nil, fmt.Errorf("не удалось определить размерность вектора для коллекции Qdrant: %w", err)
		}
		if len(testEmbeddings) != 1 || len(testEmbeddings[0]) == 0 {
			log.Printf("[QdrantStorage ERROR] Получен некорректный результат эмбеддинга для теста (ожидался 1 непустой вектор): %d векторов", len(testEmbeddings))
			return nil, fmt.Errorf("не удалось определить размерность вектора: неожиданный результат от GetEmbeddingsBatch")
		}
		testEmbedding := testEmbeddings[0] // Берем первый (и единственный) эмбеддинг
		vectorSize := uint64(len(testEmbedding))
		log.Printf("[QdrantStorage] Определена размерность векторов: %d", vectorSize)

		// --- НОВЫЙ КОД: Добавляем параметры оптимизации из конфига ---
		vectorsConfig := &qdrant.VectorsConfig{
			Config: &qdrant.VectorsConfig_Params{
				Params: &qdrant.VectorParams{
					Size:     vectorSize,
					Distance: qdrant.Distance_Cosine,
					OnDisk:   &cfg.QdrantOnDisk, // Используем флаг из конфига
				},
			},
		}

		var quantizationConfig *qdrant.QuantizationConfig
		if cfg.QdrantQuantizationOn {
			log.Printf("[QdrantStorage] Включаем скалярное квантование (int8). Quantized vectors in RAM: %t", cfg.QdrantQuantizationRam)
			quantizationConfig = &qdrant.QuantizationConfig{
				Quantization: &qdrant.QuantizationConfig_Scalar{
					Scalar: &qdrant.ScalarQuantization{
						Type:     qdrant.QuantizationType_Int8,
						Quantile: nil, // Используем nil для автоматического определения (0.99 по умолчанию в Qdrant)
						// Quantile: &defaultQuantile, // Можно задать, если нужно (например, 0.99)
						AlwaysRam: &cfg.QdrantQuantizationRam, // Используем флаг из конфига
					},
				},
			}
		}
		// --- КОНЕЦ НОВОГО КОДА ---

		// Добавляем API ключ в контекст для запроса Create, если он используется
		createCtx, createCancel := context.WithTimeout(context.Background(), timeout)
		defer createCancel()
		createReqCtx := createCtx
		if cfg.QdrantAPIKey != "" {
			md := metadata.New(map[string]string{"api-key": cfg.QdrantAPIKey})
			createReqCtx = metadata.NewOutgoingContext(createCtx, md)
		}

		_, err = collectionsClient.Create(createReqCtx, &qdrant.CreateCollection{ // Используем createReqCtx
			CollectionName: cfg.QdrantCollection,
			VectorsConfig:  vectorsConfig, // Используем созданный vectorsConfig
			// HnswConfig:       nil, // Можно настроить HNSW отдельно
			// OptimizersConfig: nil, // Можно настроить оптимизаторы
			QuantizationConfig: quantizationConfig, // Используем созданный quantizationConfig
		})
		if err != nil {
			log.Printf("[QdrantStorage ERROR] Не удалось создать коллекцию '%s': %v", cfg.QdrantCollection, err)
			conn.Close() // Закрываем соединение при ошибке
			return nil, fmt.Errorf("ошибка создания коллекции Qdrant '%s': %w", cfg.QdrantCollection, err)
		}
		log.Printf("[QdrantStorage] Коллекция '%s' успешно создана.", cfg.QdrantCollection)
	}

	log.Println("[QdrantStorage] Клиент Qdrant успешно инициализирован.")
	return &QdrantStorage{
		client:         pointsClient,
		collectionName: cfg.QdrantCollection,
		timeout:        timeout,
		geminiClient:   geminiClient,
		debug:          cfg.Debug,
		// НОВОЕ ПОЛЕ:
		importChunkSize: cfg.ImportChunkSize, // Сохраняем размер чанка
	}, nil
}

// --- Реализация интерфейса HistoryStorage (частичная/адаптированная) ---

// AddMessage добавляет одно сообщение в хранилище Qdrant.
func (qs *QdrantStorage) AddMessage(chatID int64, message *tgbotapi.Message) {
	log.Printf("[Qdrant DEBUG] Попытка добавить сообщение ID %d в чат %d", message.MessageID, chatID)

	// Игнорируем сообщения без текста
	if message.Text == "" && message.Caption == "" {
		log.Printf("[Qdrant DEBUG] Сообщение ID %d в чате %d не содержит текста, пропускаем", message.MessageID, chatID)
		return
	}

	// Получаем текст сообщения (текст или подпись, если текст пуст)
	messageText := message.Text
	if messageText == "" {
		messageText = message.Caption
		log.Printf("[Qdrant DEBUG] Используем Caption вместо Text для сообщения ID %d", message.MessageID)
	}

	// 1. Получаем эмбеддинг текста
	log.Printf("[Qdrant DEBUG] Запрос эмбеддинга для сообщения ID %d, текст: %s...", message.MessageID, truncateString(messageText, 20))
	ctxEmb, cancelEmb := context.WithTimeout(context.Background(), qs.timeout)
	defer cancelEmb()
	embeddings, err := qs.geminiClient.GetEmbeddingsBatch(ctxEmb, []string{messageText})
	if err != nil {
		log.Printf("[Qdrant ERROR] Ошибка получения эмбеддинга для сообщения ID %d: %v", message.MessageID, err)
		return // Прерываем, если не удалось получить эмбеддинг
	}
	if len(embeddings) != 1 || len(embeddings[0]) == 0 {
		log.Printf("[Qdrant ERROR] Получен некорректный результат эмбеддинга для сообщения ID %d (ожидался 1 непустой вектор): %d векторов", message.MessageID, len(embeddings))
		return
	}
	embedding := embeddings[0] // Берем первый (и единственный) эмбеддинг
	log.Printf("[Qdrant DEBUG] Получен эмбеддинг размером %d для сообщения ID %d", len(embedding), message.MessageID)

	// 2. Создаем payload и ID для сообщения
	payloadMap, pointIDStr := qs.createPayload(chatID, message, "live")
	if payloadMap == nil {
		log.Printf("[Qdrant ERROR] Не удалось создать payload для сообщения ID %d", message.MessageID)
		return
	}
	log.Printf("[Qdrant DEBUG] Создан payload и ID (%s) для сообщения ID %d", pointIDStr, message.MessageID)

	// 3. Создаем точку Qdrant
	pointID := &qdrant.PointId{
		PointIdOptions: &qdrant.PointId_Uuid{
			Uuid: pointIDStr,
		},
	}

	point := &qdrant.PointStruct{
		Id: pointID,
		Vectors: &qdrant.Vectors{
			VectorsOptions: &qdrant.Vectors_Vector{
				Vector: &qdrant.Vector{
					Data: embedding,
				},
			},
		},
		Payload: payloadMap,
	}

	// 4. Добавляем точку в Qdrant
	ctx, cancel := context.WithTimeout(context.Background(), qs.timeout)
	defer cancel()

	upsertCtx := ctx
	if apiKey := qs.getApiKeyFromConfig(); apiKey != "" {
		md := metadata.New(map[string]string{"api-key": apiKey})
		upsertCtx = metadata.NewOutgoingContext(ctx, md)
		log.Printf("[Qdrant DEBUG] Используем API ключ для аутентификации запроса")
	}

	log.Printf("[Qdrant DEBUG] Отправка запроса Upsert для сообщения ID %d", message.MessageID)
	waitUpsert := true // Синхронный Upsert для индивидуальных сообщений
	resp, err := qs.client.Upsert(upsertCtx, &qdrant.UpsertPoints{
		CollectionName: qs.collectionName,
		Points:         []*qdrant.PointStruct{point},
		Wait:           &waitUpsert,
	})

	if err != nil {
		log.Printf("[Qdrant ERROR] Ошибка при Upsert сообщения ID %d: %v", message.MessageID, err)
		return
	}

	if resp == nil {
		log.Printf("[Qdrant ERROR] Странный ответ: resp == nil без ошибки для сообщения ID %d", message.MessageID)
		return
	}

	log.Printf("[Qdrant OK] Сообщение ID %d успешно добавлено в коллекцию %s", message.MessageID, qs.collectionName)
}

// createPayload конвертирует сообщение и метаданные в map[string]*qdrant.Value для Qdrant.
func (qs *QdrantStorage) createPayload(chatID int64, message *tgbotapi.Message, importSource string) (map[string]*qdrant.Value, string) {

	// Создаем уникальный ID для сообщения
	uniqueID := fmt.Sprintf("%d_%d", chatID, message.MessageID)

	payload := &MessagePayload{
		ChatID:       chatID,
		MessageID:    message.MessageID,
		Text:         message.Text,
		Date:         message.Date,
		ImportSource: importSource,
		UniqueID:     uniqueID, // Сохраняем уникальный ID и в пейлоаде
	}
	if message.From != nil {
		payload.UserID = message.From.ID
		payload.UserName = message.From.UserName
		payload.FirstName = message.From.FirstName
		payload.IsBot = message.From.IsBot
	}
	if message.ReplyToMessage != nil {
		payload.ReplyToMsgID = message.ReplyToMessage.MessageID
	}
	if len(message.Entities) > 0 {
		// Сериализуем entities в JSON для хранения
		entitiesBytes, err := json.Marshal(message.Entities)
		if err == nil {
			payload.Entities = entitiesBytes
		} else {
			log.Printf("[QdrantStorage WARN Payload Chat %d Msg %d] Не удалось сериализовать Entities: %v", chatID, message.MessageID, err)
		}
	}

	// Конвертируем структуру MessagePayload в map[string]*qdrant.Value
	qdrantPayload := map[string]*qdrant.Value{
		"chat_id":         {Kind: &qdrant.Value_IntegerValue{IntegerValue: chatID}},
		"message_id":      {Kind: &qdrant.Value_IntegerValue{IntegerValue: int64(payload.MessageID)}},
		"user_id":         {Kind: &qdrant.Value_IntegerValue{IntegerValue: payload.UserID}},
		"user_name":       {Kind: &qdrant.Value_StringValue{StringValue: payload.UserName}},
		"first_name":      {Kind: &qdrant.Value_StringValue{StringValue: payload.FirstName}},
		"is_bot":          {Kind: &qdrant.Value_BoolValue{BoolValue: payload.IsBot}},
		"text":            {Kind: &qdrant.Value_StringValue{StringValue: payload.Text}},
		"date":            {Kind: &qdrant.Value_IntegerValue{IntegerValue: int64(payload.Date)}},
		"reply_to_msg_id": {Kind: &qdrant.Value_IntegerValue{IntegerValue: int64(payload.ReplyToMsgID)}},
		"import_source":   {Kind: &qdrant.Value_StringValue{StringValue: payload.ImportSource}},
		"unique_id":       {Kind: &qdrant.Value_StringValue{StringValue: payload.UniqueID}},
	}
	if len(payload.Entities) > 0 {
		// Храним сериализованный JSON как строку
		qdrantPayload["entities_json"] = &qdrant.Value{Kind: &qdrant.Value_StringValue{StringValue: string(payload.Entities)}}
	}

	// Добавляем роль (если она не "user", или если хотим хранить всегда)
	if payload.Role != "user" {
		qdrantPayload["role"] = &qdrant.Value{Kind: &qdrant.Value_StringValue{StringValue: payload.Role}}
	}

	return qdrantPayload, uniqueID
}

// AddMessagesToContext добавляет несколько сообщений.
func (qs *QdrantStorage) AddMessagesToContext(chatID int64, messages []*tgbotapi.Message) {
	// Можно оптимизировать, получая эмбеддинги батчами и делая один Upsert
	for _, msg := range messages {
		qs.AddMessage(chatID, msg)
	}
}

// GetMessages - Возвращает пустой срез. Поиск идет через FindRelevantMessages.
func (qs *QdrantStorage) GetMessages(chatID int64) []*tgbotapi.Message {
	log.Printf("[QdrantStorage WARN] GetMessages вызван, но не реализован для Qdrant. Возвращен пустой срез. Используйте FindRelevantMessages.")
	return []*tgbotapi.Message{}
}

// GetMessagesSince - Возвращает пустой срез. Поиск идет через FindRelevantMessages.
func (qs *QdrantStorage) GetMessagesSince(chatID int64, since time.Time) []*tgbotapi.Message {
	log.Printf("[QdrantStorage WARN] GetMessagesSince вызван, но не реализован для Qdrant. Возвращен пустой срез. Используйте FindRelevantMessages.")
	return []*tgbotapi.Message{}
}

// LoadChatHistory - Нерелевантно для Qdrant, возвращает nil.
func (qs *QdrantStorage) LoadChatHistory(chatID int64) ([]*tgbotapi.Message, error) {
	log.Printf("[QdrantStorage] LoadChatHistory вызван, но не требуется для Qdrant.")
	return nil, nil
}

// SaveChatHistory - Нерелевантно для Qdrant, возвращает nil.
func (qs *QdrantStorage) SaveChatHistory(chatID int64) error {
	// log.Printf("[QdrantStorage] SaveChatHistory вызван, но не требуется для Qdrant (сохранение идет при AddMessage).")
	return nil
}

// ClearChatHistory удаляет все точки для данного chatID из Qdrant.
func (qs *QdrantStorage) ClearChatHistory(chatID int64) {
	log.Printf("[QdrantStorage] Очистка истории для чата %d...", chatID)
	ctx, cancel := context.WithTimeout(context.Background(), qs.timeout)
	defer cancel()
	deleteCtx := ctx // Контекст для запроса Delete
	if apiKey := qs.getApiKeyFromConfig(); apiKey != "" {
		md := metadata.New(map[string]string{"api-key": apiKey})
		deleteCtx = metadata.NewOutgoingContext(ctx, md)
	}

	// Используем фильтр для удаления точек по chat_id
	waitDelete := true
	_, err := qs.client.Delete(deleteCtx, &qdrant.DeletePoints{ // Используем deleteCtx
		CollectionName: qs.collectionName,
		Points: &qdrant.PointsSelector{
			PointsSelectorOneOf: &qdrant.PointsSelector_Filter{
				Filter: &qdrant.Filter{
					Must: []*qdrant.Condition{
						{
							ConditionOneOf: &qdrant.Condition_Field{
								Field: &qdrant.FieldCondition{
									Key: "chat_id",
									Match: &qdrant.Match{
										MatchValue: &qdrant.Match_Integer{Integer: chatID},
									},
								},
							},
						},
					},
				},
			},
		},
		Wait: &waitDelete,
	})

	if err != nil {
		log.Printf("[QdrantStorage ERROR ClearChat Chat %d] Ошибка удаления точек: %v", chatID, err)
	} else {
		log.Printf("[QdrantStorage] История для чата %d успешно удалена из Qdrant.", chatID)
	}
}

// SaveAllChatHistories - Нерелевантно для Qdrant, возвращает nil.
func (qs *QdrantStorage) SaveAllChatHistories() error {
	// log.Printf("[QdrantStorage] SaveAllChatHistories вызван, но не требуется для Qdrant.")
	return nil
}

// --- Функции для Семантического Поиска (не часть интерфейса HistoryStorage) ---

// FindRelevantMessages ищет сообщения в Qdrant, семантически близкие к queryText.
// Соответствует интерфейсу HistoryStorage.
func (qs *QdrantStorage) FindRelevantMessages(chatID int64, queryText string, limit int) ([]types.Message, error) {
	if queryText == "" {
		log.Printf("[QdrantStorage WARN FindRelevant Chat %d] Пустой запрос для поиска.", chatID)
		return []types.Message{}, nil // Возвращаем пустой срез Message
	}
	if limit <= 0 {
		log.Printf("[QdrantStorage WARN FindRelevant Chat %d] Некорректный лимит %d, использую 1.", chatID, limit)
		limit = 1
	}

	// 1. Получаем эмбеддинг для текста запроса
	ctxEmb, cancelEmb := context.WithTimeout(context.Background(), qs.timeout)
	defer cancelEmb()
	queryEmbeddings, err := qs.geminiClient.GetEmbeddingsBatch(ctxEmb, []string{queryText})
	if err != nil {
		log.Printf("[QdrantStorage ERROR FindRelevant Chat %d] Ошибка получения эмбеддинга для запроса '%s': %v", chatID, truncateString(queryText, 50), err)
		return nil, fmt.Errorf("ошибка получения эмбеддинга для поиска: %w", err)
	}
	if len(queryEmbeddings) != 1 || len(queryEmbeddings[0]) == 0 {
		log.Printf("[QdrantStorage WARN FindRelevant Chat %d] Получен некорректный результат эмбеддинга для запроса '%s' (ожидался 1 непустой вектор): %d векторов", chatID, truncateString(queryText, 50), len(queryEmbeddings))
		// Не возвращаем ошибку, но и результатов не будет
		return []types.Message{}, nil
	}
	queryEmbedding := queryEmbeddings[0] // Берем первый (и единственный) эмбеддинг

	// 2. Формируем запрос на поиск в Qdrant
	ctx, cancel := context.WithTimeout(context.Background(), qs.timeout)
	defer cancel()
	searchCtx := ctx // Контекст для запроса Search
	if apiKey := qs.getApiKeyFromConfig(); apiKey != "" {
		md := metadata.New(map[string]string{"api-key": apiKey})
		searchCtx = metadata.NewOutgoingContext(ctx, md)
	}

	searchRequest := &qdrant.SearchPoints{
		CollectionName: qs.collectionName,
		Vector:         queryEmbedding,
		Limit:          uint64(limit), // Конвертируем limit в uint64 для Qdrant API
		WithPayload:    &qdrant.WithPayloadSelector{SelectorOptions: &qdrant.WithPayloadSelector_Enable{Enable: true}},
		Filter: &qdrant.Filter{
			Must: []*qdrant.Condition{
				{
					ConditionOneOf: &qdrant.Condition_Field{
						Field: &qdrant.FieldCondition{
							Key:   "chat_id",
							Match: &qdrant.Match{MatchValue: &qdrant.Match_Integer{Integer: chatID}},
						},
					},
				},
			},
		},
		// Можно добавить ScoreThreshold, если нужно отсекать совсем нерелевантные результаты
		// ScoreThreshold: &threshold,
	}

	// 3. Выполняем поиск
	searchResult, err := qs.client.Search(searchCtx, searchRequest)
	if err != nil {
		log.Printf("[QdrantStorage ERROR FindRelevant Chat %d] Ошибка поиска в Qdrant: %v", chatID, err)
		return nil, fmt.Errorf("ошибка поиска в Qdrant: %w", err)
	}

	// 4. Преобразуем результат в []Message
	foundMessages := make([]types.Message, 0, len(searchResult.Result))
	for _, scoredPoint := range searchResult.Result {
		msg, err := qs.payloadToMessage(scoredPoint.Payload) // Используем новую хелпер-функцию
		if err != nil {
			log.Printf("[QdrantStorage WARN FindRelevant Chat %d PointID %s] Ошибка преобразования payload в Message: %v. Пропускаем точку.",
				chatID, pointIDToString(scoredPoint.Id), err)
			continue
		}
		// Опционально: можно добавить score в лог или даже в структуру Message, если нужно
		if qs.debug {
			log.Printf("[QdrantStorage DEBUG FindRelevant Chat %d] Найдена точка %s, Score: %.4f, Текст: %s",
				chatID, pointIDToString(scoredPoint.Id), scoredPoint.Score, truncateString(msg.Text, 50))
		}
		foundMessages = append(foundMessages, msg)
	}

	log.Printf("[QdrantStorage] Найдено %d релевантных сообщений для чата %d по запросу '%s'", len(foundMessages), chatID, truncateString(queryText, 50))
	return foundMessages, nil // Возвращаем []Message
}

// payloadToMessage конвертирует map[string]*qdrant.Value обратно в storage.Message
func (qs *QdrantStorage) payloadToMessage(payload map[string]*qdrant.Value) (types.Message, error) {
	var msg types.Message
	var msgIDInt, dateInt, userIDInt int64
	// var ok bool // 'ok' не используется, можно убрать

	if val, ok := payload["message_id"]; ok {
		if intVal, isInt := val.GetKind().(*qdrant.Value_IntegerValue); isInt {
			msgIDInt = intVal.IntegerValue
			msg.ID = msgIDInt // ID сообщения из Telegram
		} else {
			return types.Message{}, fmt.Errorf("неверный тип для message_id: %T", val.GetKind())
		}
	} else {
		return types.Message{}, fmt.Errorf("отсутствует поле message_id")
	}

	if val, ok := payload["date"]; ok {
		if intVal, isInt := val.GetKind().(*qdrant.Value_IntegerValue); isInt {
			dateInt = intVal.IntegerValue
			msg.Timestamp = int(dateInt) // Исправление: присваиваем int
		} else {
			return types.Message{}, fmt.Errorf("неверный тип для date: %T", val.GetKind())
		}
	} else {
		return types.Message{}, fmt.Errorf("отсутствует поле date")
	}

	// Role не хранится напрямую в payload Qdrant для обычных сообщений,
	// т.к. обычно они от 'user'. При импорте это может быть иначе.
	// Если нужно восстановить роль, её нужно добавить в payload при сохранении.
	// Пока что ставим "user" по умолчанию, если не нашли иного.
	msg.Role = "user" // TODO: Рассмотреть сохранение/восстановление роли
	// Можно проверить наличие поля "role", если оно будет добавляться
	// if val, ok := payload["role"]; ok {
	//     if strVal, isStr := val.GetKind().(*qdrant.Value_StringValue); isStr {
	//         msg.Role = strVal.StringValue
	//     }
	// }

	if val, ok := payload["text"]; ok {
		if strVal, isStr := val.GetKind().(*qdrant.Value_StringValue); isStr {
			msg.Text = strVal.StringValue
		} else {
			return types.Message{}, fmt.Errorf("неверный тип для text: %T", val.GetKind())
		}
	} else {
		return types.Message{}, fmt.Errorf("отсутствует поле text")
	}

	// Восстанавливаем необязательные поля
	if val, ok := payload["user_id"]; ok {
		if intVal, isInt := val.GetKind().(*qdrant.Value_IntegerValue); isInt {
			userIDInt = intVal.IntegerValue
			_ = userIDInt // Подавляем ошибку 'unused variable', если ID не используется далее
			// Можно использовать userIDInt для восстановления User в tgbotapi.Message, если нужно
		}
	}
	// ... можно добавить восстановление user_name, first_name и т.д., если необходимо ...

	// Embedding не восстанавливаем, т.к. он не нужен для возврата в виде Message
	// msg.Embedding = ...

	// Проверяем, что основные поля были найдены
	if msg.ID == 0 || msg.Text == "" || msg.Timestamp == 0 {
		return types.Message{}, fmt.Errorf("не удалось восстановить основные поля сообщения из payload: %+v", payload)
	}

	return msg, nil
}

// pointIDToString конвертирует qdrant.PointId в строку для логов.
func pointIDToString(id *qdrant.PointId) string {
	if id == nil {
		return "<nil>"
	}
	switch opt := id.PointIdOptions.(type) {
	case *qdrant.PointId_Num:
		return fmt.Sprintf("Num(%d)", opt.Num)
	case *qdrant.PointId_Uuid:
		return fmt.Sprintf("Uuid(%s)", opt.Uuid)
	default:
		return "<unknown_type>"
	}
}

// --- Импорт старых данных ---

// ImportMessagesFromJSONFile импортирует сообщения из JSON файла в Qdrant.
// Использует types.Message для работы с сообщениями.
func (qs *QdrantStorage) ImportMessagesFromJSONFile(chatID int64, filePath string) (importedCount int, skippedCount int, err error) {
	log.Printf("[Qdrant Import] Начинаю импорт из файла: %s для чата %d", filePath, chatID)

	// 1. Открываем файл для потокового чтения
	file, err := os.Open(filePath)
	if err != nil {
		log.Printf("[Qdrant Import ERROR] Чат %d: Ошибка открытия файла %s: %v", chatID, filePath, err)
		return 0, 0, fmt.Errorf("ошибка открытия файла импорта '%s': %w", filePath, err)
	}
	defer file.Close()

	// 2. Создаем json.Decoder для потокового чтения
	decoder := json.NewDecoder(file)

	// Ожидаем массив JSON объектов '['
	t, err := decoder.Token()
	if err != nil || t != json.Delim('[') {
		log.Printf("[Qdrant Import ERROR] Чат %d: Ошибка чтения начала JSON массива в %s: %v (токен: %v)", chatID, filePath, err, t)
		return 0, 0, fmt.Errorf("ожидался JSON массив в файле '%s'", filePath)
	}

	// 3. Обрабатываем и добавляем сообщения чанками
	chunkSize := qs.importChunkSize // Используем размер чанка из QdrantStorage
	if chunkSize <= 0 {
		chunkSize = 256 // Устанавливаем значение по умолчанию, если не задано или некорректно
		log.Printf("[Qdrant Import WARN] Чат %d: Некорректный importChunkSize, использую %d", chatID, chunkSize)
	}
	messageChunk := make([]types.Message, 0, chunkSize)
	existingPoints := make(map[string]bool) // Кеш для проверки дубликатов в рамках одного файла

	// Используем WaitGroup для ожидания завершения всех горутин получения эмбеддингов
	var wg sync.WaitGroup
	// Канал для передачи готовых точек в основной поток
	// Буфер равен chunkSize, т.к. мы обрабатываем один чанк за раз
	pointsChan := make(chan *qdrant.PointStruct, chunkSize)
	// Канал для отслеживания ошибок получения эмбеддингов
	errorChan := make(chan error, chunkSize)
	// Мьютекс для безопасного доступа к счетчикам
	var countMutex sync.Mutex

	// Ограничение количества одновременных горутин для получения эмбеддингов (например, 10)
	embeddingSemaphore := make(chan struct{}, 10)

	totalProcessed := 0 // Общий счетчик обработанных сообщений из файла

	// Читаем сообщения из массива JSON
	for decoder.More() {
		var msg types.Message
		if err := decoder.Decode(&msg); err != nil {
			log.Printf("[Qdrant Import ERROR] Чат %d: Ошибка декодирования JSON сообщения в %s: %v", chatID, filePath, err)
			// Пропускаем ошибочное сообщение, но продолжаем импорт
			countMutex.Lock()
			skippedCount++
			countMutex.Unlock()
			continue // Пропускаем это сообщение
		}

		totalProcessed++
		messageChunk = append(messageChunk, msg)

		// Если чанк наполнен, обрабатываем его
		if len(messageChunk) >= chunkSize {
			log.Printf("[Qdrant Import] Чат %d: Обработка чанка %d сообщений (всего обработано: %d)...", chatID, len(messageChunk), totalProcessed)
			pointsBatch, skippedInBatch, embErr := qs.processMessageChunk(chatID, messageChunk, existingPoints, &wg, pointsChan, errorChan, embeddingSemaphore)
			countMutex.Lock()
			skippedCount += skippedInBatch
			countMutex.Unlock()
			if embErr != nil && err == nil { // Сохраняем первую ошибку эмбеддинга
				err = embErr
			}

			if len(pointsBatch) > 0 {
				batchImported, batchSkipped, batchErr := qs.upsertPointsBatch(pointsBatch)
				countMutex.Lock()
				importedCount += batchImported
				skippedCount += batchSkipped
				countMutex.Unlock()
				if batchErr != nil {
					log.Printf("[Qdrant Import ERROR Batch] Чат %d: Ошибка при отправке батча из чанка: %v", chatID, batchErr)
					if err == nil {
						err = batchErr // Сохраняем первую ошибку
					}
				}
			}
			messageChunk = messageChunk[:0] // Очищаем чанк сообщений
			// Восстанавливаем очистку батча
			pointsBatch = pointsBatch[:0]
		}
	}

	// Обрабатываем последний неполный чанк, если он остался
	if len(messageChunk) > 0 {
		log.Printf("[Qdrant Import] Чат %d: Обработка последнего чанка из %d сообщений (всего обработано: %d)...", chatID, len(messageChunk), totalProcessed)
		pointsBatch, skippedInBatch, embErr := qs.processMessageChunk(chatID, messageChunk, existingPoints, &wg, pointsChan, errorChan, embeddingSemaphore)
		countMutex.Lock()
		skippedCount += skippedInBatch
		countMutex.Unlock()
		if embErr != nil && err == nil {
			err = embErr
		}

		if len(pointsBatch) > 0 {
			batchImported, batchSkipped, batchErr := qs.upsertPointsBatch(pointsBatch)
			countMutex.Lock()
			importedCount += batchImported
			skippedCount += batchSkipped
			countMutex.Unlock()
			if batchErr != nil {
				log.Printf("[Qdrant Import ERROR Batch] Чат %d: Ошибка при отправке последнего батча: %v", chatID, batchErr)
				if err == nil {
					err = batchErr
				}
			}
		}
	}

	// Ожидаем окончания декодирования массива ']'
	t, errDecodeEnd := decoder.Token()
	if errDecodeEnd != nil || t != json.Delim(']') {
		log.Printf("[Qdrant Import WARN] Чат %d: Ошибка чтения конца JSON массива в %s: %v (токен: %v)", chatID, filePath, errDecodeEnd, t)
		// Не фатально, но стоит залогировать
	}

	log.Printf("[Qdrant Import OK] Чат %d: Импорт из файла %s завершен. Всего прочитано: %d, Импортировано/Обновлено: %d, Пропущено (дубликаты/ошибки): %d.",
		chatID, filePath, totalProcessed, importedCount, skippedCount)

	return importedCount, skippedCount, err // Возвращаем первую возникшую ошибку
}

// --- НОВЫЙ МЕТОД: processMessageChunk ---
// processMessageChunk обрабатывает чанк сообщений: получает эмбеддинги и формирует батч Qdrant точек.
func (qs *QdrantStorage) processMessageChunk(
	chatID int64,
	messages []types.Message,
	existingPoints map[string]bool, // Кеш для проверки дубликатов UUID
	wg *sync.WaitGroup,
	pointsChan chan<- *qdrant.PointStruct,
	errorChan chan<- error,
	semaphore chan struct{},
) (pointsBatch []*qdrant.PointStruct, skippedInChunk int, firstEmbError error) {

	pointsResultChan := make(chan *qdrant.PointStruct, len(messages)) // Канал для результатов этого чанка
	var chunkWg sync.WaitGroup
	var chunkMutex sync.Mutex // Мьютекс для skippedInChunk и firstEmbError

	for i, msg := range messages {
		// Пропускаем сообщения без текста или ID
		if msg.Text == "" || msg.ID == 0 {
			chunkMutex.Lock()
			skippedInChunk++
			chunkMutex.Unlock()
			if qs.debug {
				log.Printf("[Qdrant Import DEBUG Chunk] Чат %d: Пропуск сообщения %d/%d (пустой текст или ID=0).", chatID, i+1, len(messages))
			}
			continue
		}

		// Генерируем UUID v5
		uniqueIDStr := fmt.Sprintf("%d_%d", chatID, msg.ID)
		pointUUID := uuid.NewSHA1(uuid.NameSpaceDNS, []byte(uniqueIDStr))
		pointIDStrForUUID := pointUUID.String()

		// Проверка на дубликат в общем кеше
		if existingPoints[pointIDStrForUUID] {
			chunkMutex.Lock()
			skippedInChunk++
			chunkMutex.Unlock()
			if qs.debug {
				log.Printf("[Qdrant Import DEBUG Chunk] Чат %d: Пропуск дубликата сообщения %d/%d (UUID: %s) в файле.", chatID, i+1, len(messages), pointIDStrForUUID)
			}
			continue
		}
		existingPoints[pointIDStrForUUID] = true // Запоминаем UUID

		chunkWg.Add(1)
		go func(m types.Message, uID string, index int) {
			defer chunkWg.Done()
			semaphore <- struct{}{}        // Захватываем слот семафора
			defer func() { <-semaphore }() // Освобождаем слот

			// 1. Получаем эмбеддинг
			ctxEmb, cancelEmb := context.WithTimeout(context.Background(), qs.timeout)
			defer cancelEmb()
			embeddings, err := qs.geminiClient.GetEmbeddingsBatch(ctxEmb, []string{m.Text})
			if err != nil {
				log.Printf("[Qdrant Import ERROR Emb Chunk] Чат %d: Сообщение %d/%d (UUID: %s): Ошибка эмбеддинга: %v", chatID, index+1, len(messages), uID, err)
				errorChan <- err // Отправляем ошибку в общий канал ошибок
				chunkMutex.Lock()
				if firstEmbError == nil { // Запоминаем первую ошибку в чанке
					firstEmbError = err
				}
				chunkMutex.Unlock()
				pointsResultChan <- nil // Отправляем nil в канал результатов чанка
				return
			}
			if len(embeddings) != 1 || len(embeddings[0]) == 0 {
				log.Printf("[Qdrant Import WARN Emb Chunk] Чат %d: Сообщение %d/%d (UUID: %s): Пустой/некорректный эмбеддинг (кол-во: %d).", chatID, index+1, len(messages), uID, len(embeddings))
				pointsResultChan <- nil // Отправляем nil в канал результатов чанка
				return
			}
			embedding := embeddings[0] // Берем первый (и единственный) эмбеддинг

			// 2. Создаем Payload
			msgPayload := &MessagePayload{
				ChatID:       chatID,
				MessageID:    int(m.ID),
				Text:         m.Text,
				Date:         m.Timestamp,
				ImportSource: "batch_old",
				UniqueID:     uID,
				Role:         m.Role,
				// UserID, UserName, FirstName, IsBot, ReplyToMsgID берутся из m
				UserID:       m.UserID,
				UserName:     m.UserName,
				FirstName:    m.FirstName,
				IsBot:        m.IsBot,
				ReplyToMsgID: m.ReplyToMsgID,
			}
			if len(m.Entities) > 0 {
				entitiesBytes, err := json.Marshal(m.Entities)
				if err == nil {
					msgPayload.Entities = entitiesBytes
				} else {
					log.Printf("[Qdrant Import WARN Chunk] Чат %d, Msg %d: Не удалось сериализовать Entities: %v", chatID, m.ID, err)
				}
			}
			payloadMap := qs.messagePayloadToQdrantMap(msgPayload)

			// 3. Создаем Qdrant Point
			point := &qdrant.PointStruct{
				Id:      &qdrant.PointId{PointIdOptions: &qdrant.PointId_Uuid{Uuid: uID}},
				Vectors: &qdrant.Vectors{VectorsOptions: &qdrant.Vectors_Vector{Vector: &qdrant.Vector{Data: embedding}}},
				Payload: payloadMap,
			}
			pointsResultChan <- point // Отправляем готовую точку в канал результатов чанка

		}(msg, pointIDStrForUUID, i)
	}

	// Ждем завершения всех горутин для этого чанка
	chunkWg.Wait()
	close(pointsResultChan) // Закрываем канал результатов чанка

	// Собираем результаты из канала результатов чанка
	for point := range pointsResultChan {
		if point != nil {
			pointsBatch = append(pointsBatch, point)
		} else {
			// Сообщение было пропущено из-за ошибки/пустого эмбеддинга
			chunkMutex.Lock()
			skippedInChunk++
			chunkMutex.Unlock()
		}
	}

	// Закрывать pointsChan и errorChan не нужно здесь, они общие для всего импорта

	return pointsBatch, skippedInChunk, firstEmbError
}

// --- КОНЕЦ НОВОГО МЕТОДА ---

// upsertPointsBatch отправляет батч точек в Qdrant.
// Возвращает количество успешно добавленных/обновленных и пропущенных (уже существующих) точек.
func (qs *QdrantStorage) upsertPointsBatch(points []*qdrant.PointStruct) (int, int, error) {
	if len(points) == 0 {
		return 0, 0, nil
	}

	// 1. Проверяем, какие точки уже существуют (опционально, но уменьшает нагрузку на Upsert)
	// Qdrant Upsert сам по себе идемпотентен, но предварительная проверка может быть чуть быстрее,
	// если ожидается много дубликатов. Однако это требует дополнительного запроса Get.
	// Пока что будем полагаться на идемпотентность Upsert.

	// 2. Выполняем Upsert
	ctx, cancel := context.WithTimeout(context.Background(), qs.timeout*2) // Увеличим таймаут для батча
	defer cancel()
	upsertCtx := ctx // Контекст для запроса Upsert
	if apiKey := qs.getApiKeyFromConfig(); apiKey != "" {
		md := metadata.New(map[string]string{"api-key": apiKey})
		upsertCtx = metadata.NewOutgoingContext(ctx, md)
	}

	// --- ИЗМЕНЕНИЕ: Устанавливаем wait = false для импорта ---
	waitUpsert := false                                            // Не ждем подтверждения для ускорения импорта
	resp, err := qs.client.Upsert(upsertCtx, &qdrant.UpsertPoints{ // Используем upsertCtx
		CollectionName: qs.collectionName,
		Points:         points,
		Wait:           &waitUpsert,
	})
	// --- КОНЕЦ ИЗМЕНЕНИЯ ---

	if err != nil {
		log.Printf("[Qdrant UpsertBatch ERROR] Ошибка при Upsert батча (%d точек): %v", len(points), err)
		// Если произошла ошибка при Upsert, считаем, что все точки не были обработаны (пропущены)
		return 0, len(points), fmt.Errorf("ошибка Upsert батча: %w", err)
	}

	// Если ошибки не было (err == nil), Qdrant гарантирует, что операция завершена (т.к. Wait=true).
	// В этом случае считаем, что все точки были успешно добавлены/обновлены.
	// Qdrant не возвращает статус или количество обновленных/вставленных точек.
	importedCount := len(points)
	skippedCount := 0 // Так как Upsert идемпотентен, дубликаты просто обновляются, а не пропускаются с ошибкой.

	// Убираем проверку resp.GetStatus(), так как ее нет и она не нужна при err == nil
	/*
		if resp.GetStatus() != qdrant.UpdateStatus_Completed {
			log.Printf("[Qdrant UpsertBatch WARN] Статус Upsert батча: %s", resp.GetStatus())
			return 0, len(points), fmt.Errorf("статус Upsert батча: %s", resp.GetStatus())
		}
	*/

	// Добавляем проверку на nil resp на всякий случай (хотя при err==nil он не должен быть nil)
	if resp == nil && err == nil {
		log.Printf("[Qdrant UpsertBatch WARN] Upsert вернул nil response без ошибки для %d точек.", len(points))
		// Сложно сказать, что произошло. На всякий случай считаем пропущенными.
		return 0, len(points), fmt.Errorf("upsert вернул nil response без ошибки")
	}

	if qs.debug {
		log.Printf("[Qdrant UpsertBatch DEBUG] Успешно выполнен Upsert для %d точек.", importedCount)
	}

	return importedCount, skippedCount, nil
}

// messagePayloadToQdrantMap конвертирует *MessagePayload в map[string]*qdrant.Value для Qdrant Payload.
func (qs *QdrantStorage) messagePayloadToQdrantMap(p *MessagePayload) map[string]*qdrant.Value {
	if p == nil {
		return nil
	}
	payloadMap := make(map[string]*qdrant.Value)

	// Обязательные поля для фильтрации и идентификации
	payloadMap["chat_id"] = &qdrant.Value{Kind: &qdrant.Value_IntegerValue{IntegerValue: p.ChatID}}
	payloadMap["message_id"] = &qdrant.Value{Kind: &qdrant.Value_IntegerValue{IntegerValue: int64(p.MessageID)}} // Qdrant ожидает int64
	payloadMap["unique_id"] = &qdrant.Value{Kind: &qdrant.Value_StringValue{StringValue: p.UniqueID}}
	payloadMap["date"] = &qdrant.Value{Kind: &qdrant.Value_IntegerValue{IntegerValue: int64(p.Date)}} // Используем p.Date, Qdrant ожидает int64
	payloadMap["text"] = &qdrant.Value{Kind: &qdrant.Value_StringValue{StringValue: p.Text}}
	payloadMap["import_source"] = &qdrant.Value{Kind: &qdrant.Value_StringValue{StringValue: p.ImportSource}}

	// Опциональные поля
	if p.UserID != 0 {
		payloadMap["user_id"] = &qdrant.Value{Kind: &qdrant.Value_IntegerValue{IntegerValue: p.UserID}}
	}
	if p.UserName != "" {
		payloadMap["user_name"] = &qdrant.Value{Kind: &qdrant.Value_StringValue{StringValue: p.UserName}}
	}
	if p.FirstName != "" {
		payloadMap["first_name"] = &qdrant.Value{Kind: &qdrant.Value_StringValue{StringValue: p.FirstName}}
	}
	payloadMap["is_bot"] = &qdrant.Value{Kind: &qdrant.Value_BoolValue{BoolValue: p.IsBot}}
	if p.ReplyToMsgID != 0 {
		payloadMap["reply_to_msg_id"] = &qdrant.Value{Kind: &qdrant.Value_IntegerValue{IntegerValue: int64(p.ReplyToMsgID)}}
	}
	if p.Role != "" {
		payloadMap["role"] = &qdrant.Value{Kind: &qdrant.Value_StringValue{StringValue: p.Role}}
	} else {
		// Устанавливаем роль по умолчанию, если она не задана
		payloadMap["role"] = &qdrant.Value{Kind: &qdrant.Value_StringValue{StringValue: "user"}}
	}

	// Entities сохраняются как []byte в MessagePayload
	if len(p.Entities) > 0 {
		// Qdrant напрямую не поддерживает []byte, сохраняем как строку Base64 или JSON строку
		// Сохраняем как JSON строку для читаемости и возможности десериализации
		payloadMap["entities"] = &qdrant.Value{Kind: &qdrant.Value_StringValue{StringValue: string(p.Entities)}}
	}

	return payloadMap
}

// --- Вспомогательные функции ---

// truncateString обрезает строку до указанной длины, добавляя многоточие.
func truncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	// Ищем последний пробел перед maxLen для более красивого обрезания
	lastSpace := strings.LastIndex(s[:maxLen], " ")
	if lastSpace > 0 && lastSpace > maxLen/2 { // Обрезаем по пробелу, если он не слишком близко к началу
		return s[:lastSpace] + "..."
	}
	return s[:maxLen] + "..." // Обрезаем жестко, если нет подходящего пробела
}

// --- Добавляем недостающие функции конвертации ---

// Вспомогательная функция для конвертации Entity
func convertTgEntitiesToTypesEntities(tgEntities []tgbotapi.MessageEntity) []types.MessageEntity {
	if tgEntities == nil {
		return nil
	}
	typeEntities := make([]types.MessageEntity, len(tgEntities))
	for i, e := range tgEntities {
		typeEntities[i] = types.MessageEntity{
			Type:     e.Type,
			Offset:   e.Offset,
			Length:   e.Length,
			URL:      e.URL,
			User:     convertTgUserToTypesUser(e.User), // Нужна конвертация User
			Language: e.Language,
		}
	}
	return typeEntities
}

// Вспомогательная функция для конвертации User
func convertTgUserToTypesUser(tgUser *tgbotapi.User) *types.User {
	if tgUser == nil {
		return nil
	}
	return &types.User{
		ID:           tgUser.ID,
		IsBot:        tgUser.IsBot,
		FirstName:    tgUser.FirstName,
		LastName:     tgUser.LastName,
		UserName:     tgUser.UserName,
		LanguageCode: tgUser.LanguageCode,
		// Добавьте другие поля при необходимости
	}
}

// --- Конец добавленных функций ---

// --- Функции заглушки для методов, не используемых Qdrant ---

// getApiKeyFromConfig пытается получить API ключ из конфигурации Gemini клиента.
// Это временное решение, в идеале API ключ Qdrant должен храниться отдельно.
func (qs *QdrantStorage) getApiKeyFromConfig() string {
	// В текущей структуре конфига нет прямого поля Qdrant API Key в QdrantStorage.
	// Мы можем его получить через cfg в NewQdrantStorage, но он не сохраняется.
	// Пока что вернем пустую строку, подразумевая, что ключ передается через interceptor,
	// который был настроен в NewQdrantStorage.
	// Если интерцептор не используется, этот метод нужно будет доработать, чтобы
	// он имел доступ к конфигурации.
	return "" // Возвращаем пустую строку, т.к. интерцептор уже настроен
}
