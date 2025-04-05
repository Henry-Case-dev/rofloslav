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
		// Определяем размерность векторов. Зависит от модели Gemini!
		// Получим размерность, сгенерировав эмбеддинг для тестовой строки.
		// Важно: Убедитесь, что Gemini клиент уже инициализирован и работает.
		testEmbedding, err := geminiClient.GetEmbedding("test")
		if err != nil || len(testEmbedding) == 0 {
			log.Printf("[QdrantStorage ERROR] Не удалось получить тестовый эмбеддинг для определения размерности: %v", err)
			return nil, fmt.Errorf("не удалось определить размерность вектора для коллекции Qdrant: %w", err)
		}
		vectorSize := uint64(len(testEmbedding))
		log.Printf("[QdrantStorage] Определена размерность векторов: %d", vectorSize)

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
			VectorsConfig: &qdrant.VectorsConfig{
				Config: &qdrant.VectorsConfig_Params{
					Params: &qdrant.VectorParams{
						Size:     vectorSize,
						Distance: qdrant.Distance_Cosine, // Косинусное расстояние обычно хорошо подходит для текстовых эмбеддингов
					},
				},
			},
			// Можно добавить другие параметры: hnsw_config, optimizers_config и т.д.
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
	}, nil
}

// --- Реализация интерфейса HistoryStorage (частичная/адаптированная) ---

// AddMessage получает эмбеддинг сообщения и добавляет/обновляет его в Qdrant.
// Использует уникальный ID (chat_id + message_id) для идемпотентности.
func (qs *QdrantStorage) AddMessage(chatID int64, message *tgbotapi.Message) {
	if message == nil || (message.Text == "" && message.Caption == "") {
		return // Не сохраняем пустые сообщения или сообщения без текста/подписи
	}

	// Используем текст или подпись
	text := message.Text
	if text == "" {
		text = message.Caption
	}

	// Определяем роль
	role := "user"
	if message.From != nil && message.From.IsBot {
		// TODO: Проверить ID бота, если это важно
		// Пока считаем всех остальных ботов как "user"
	}

	// Генерируем уникальный строковый идентификатор для генерации UUID
	uniqueIDStr := fmt.Sprintf("%d_%d", chatID, message.MessageID)
	// Генерируем UUID v5 (детерминированный на основе строки)
	// Используем предопределенный Namespace UUID (например, DNS namespace)
	// или можно создать свой собственный.
	pointUUID := uuid.NewSHA1(uuid.NameSpaceDNS, []byte(uniqueIDStr))
	pointIDStrForUUID := pointUUID.String() // Строковое представление UUID

	// Создаем MessagePayload напрямую из tgbotapi.Message
	msgPayload := &MessagePayload{
		ChatID:       chatID,
		MessageID:    message.MessageID,
		Text:         text,
		Date:         message.Date,
		ImportSource: "live",
		UniqueID:     pointIDStrForUUID, // Сохраняем настоящий UUID в payload
		Role:         role,
	}

	if message.From != nil {
		msgPayload.UserID = message.From.ID
		msgPayload.UserName = message.From.UserName
		msgPayload.FirstName = message.From.FirstName
		msgPayload.IsBot = message.From.IsBot
	}
	if message.ReplyToMessage != nil {
		msgPayload.ReplyToMsgID = message.ReplyToMessage.MessageID
	}

	// Конвертируем и сериализуем Entities
	if len(message.Entities) > 0 {
		typeEntities := convertTgEntitiesToTypesEntities(message.Entities)
		entitiesBytes, err := json.Marshal(typeEntities)
		if err == nil {
			msgPayload.Entities = entitiesBytes
		} else {
			log.Printf("[QdrantStorage AddMsg Chat %d Msg %d] Ошибка сериализации Entities: %v", chatID, message.MessageID, err)
		}
	}
	// Также обрабатываем CaptionEntities
	if len(message.CaptionEntities) > 0 {
		typeCaptionEntities := convertTgEntitiesToTypesEntities(message.CaptionEntities)
		// Если Entities уже были, добавляем к ним, иначе создаем
		var existingEntities []types.MessageEntity
		if len(msgPayload.Entities) > 0 {
			_ = json.Unmarshal(msgPayload.Entities, &existingEntities) // Игнорируем ошибку, если она есть
		}
		allEntities := append(existingEntities, typeCaptionEntities...)
		entitiesBytes, err := json.Marshal(allEntities)
		if err == nil {
			msgPayload.Entities = entitiesBytes
		} else {
			log.Printf("[QdrantStorage AddMsg Chat %d Msg %d] Ошибка сериализации CaptionEntities: %v", chatID, message.MessageID, err)
		}
	}

	// 1. Получаем эмбеддинг
	embedding, err := qs.geminiClient.GetEmbedding(text) // Используем извлеченный текст
	if err != nil {
		log.Printf("[QdrantStorage ERROR AddMsg Chat %d Msg %d] Ошибка получения эмбеддинга: %v", chatID, msgPayload.MessageID, err)
		return
	}
	if len(embedding) == 0 {
		log.Printf("[QdrantStorage WARN AddMsg Chat %d Msg %d] Получен пустой эмбеддинг для текста: %s", chatID, msgPayload.MessageID, truncateString(text, 50))
		return
	}

	// 2. Конвертируем MessagePayload в Qdrant Map
	payloadMap := qs.messagePayloadToQdrantMap(msgPayload)
	// uniqueID := msgPayload.UniqueID // Уже есть в msgPayload - И больше не нужен здесь

	// 3. Создаем Qdrant Point, используя сгенерированный UUID
	point := &qdrant.PointStruct{
		Id:      &qdrant.PointId{PointIdOptions: &qdrant.PointId_Uuid{Uuid: pointIDStrForUUID}}, // Используем настоящий UUID
		Vectors: &qdrant.Vectors{VectorsOptions: &qdrant.Vectors_Vector{Vector: &qdrant.Vector{Data: embedding}}},
		Payload: payloadMap,
	}

	// 4. Добавляем/Обновляем точку (Upsert)
	ctx, cancel := context.WithTimeout(context.Background(), qs.timeout)
	defer cancel()
	upsertCtx := ctx // Контекст для запроса Upsert
	if apiKey := qs.getApiKeyFromConfig(); apiKey != "" {
		md := metadata.New(map[string]string{"api-key": apiKey})
		upsertCtx = metadata.NewOutgoingContext(ctx, md)
	}

	waitUpsert := true
	_, err = qs.client.Upsert(upsertCtx, &qdrant.UpsertPoints{ // Используем upsertCtx
		CollectionName: qs.collectionName,
		Points:         []*qdrant.PointStruct{point},
		Wait:           &waitUpsert,
	})

	if err != nil {
		// Ошибка теперь не должна быть InvalidArgument из-за UUID
		log.Printf("[QdrantStorage ERROR AddMsg Chat %d Msg %d PointID %s] Ошибка Upsert точки: %v", chatID, msgPayload.MessageID, pointIDStrForUUID, err)
	} else if qs.debug {
		log.Printf("[QdrantStorage DEBUG AddMsg Chat %d Msg %d PointID %s] Точка успешно добавлена/обновлена.", chatID, msgPayload.MessageID, pointIDStrForUUID)
	}
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
	queryEmbedding, err := qs.geminiClient.GetEmbedding(queryText)
	if err != nil {
		log.Printf("[QdrantStorage ERROR FindRelevant Chat %d] Ошибка получения эмбеддинга для запроса '%s': %v", chatID, truncateString(queryText, 50), err)
		return nil, fmt.Errorf("ошибка получения эмбеддинга для поиска: %w", err)
	}
	if len(queryEmbedding) == 0 {
		log.Printf("[QdrantStorage WARN FindRelevant Chat %d] Получен пустой эмбеддинг для запроса: %s", chatID, truncateString(queryText, 50))
		return []types.Message{}, nil // Возвращаем пустой срез Message
	}

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

	// 1. Читаем файл
	data, err := os.ReadFile(filePath)
	if err != nil {
		log.Printf("[Qdrant Import ERROR] Чат %d: Ошибка чтения файла %s: %v", chatID, filePath, err)
		return 0, 0, fmt.Errorf("ошибка чтения файла импорта '%s': %w", filePath, err)
	}

	if len(data) == 0 {
		log.Printf("[Qdrant Import WARN] Чат %d: Файл %s пуст.", chatID, filePath)
		return 0, 0, nil
	}

	// 2. Десериализуем JSON в []types.Message
	var messages []types.Message
	if err := json.Unmarshal(data, &messages); err != nil {
		log.Printf("[Qdrant Import ERROR] Чат %d: Ошибка десериализации JSON из файла %s: %v", chatID, filePath, err)
		// Попытаемся переименовать поврежденный файл
		backupPath := filePath + ".corrupted_import." + time.Now().Format("20060102150405")
		if renameErr := os.Rename(filePath, backupPath); renameErr == nil {
			log.Printf("[Qdrant Import INFO] Поврежденный файл импорта %s переименован в %s", filePath, backupPath)
		} else {
			log.Printf("[Qdrant Import ERROR] Не удалось переименовать поврежденный файл импорта %s: %v", filePath, renameErr)
		}
		return 0, 0, fmt.Errorf("ошибка десериализации JSON из файла импорта '%s': %w", filePath, err)
	}

	totalMessages := len(messages)
	log.Printf("[Qdrant Import] Чат %d: Найдено %d сообщений в файле %s для импорта.", chatID, totalMessages, filePath)

	// 3. Обрабатываем и добавляем сообщения батчами
	batchSize := 100 // Размер батча для Qdrant Upsert
	pointsBatch := make([]*qdrant.PointStruct, 0, batchSize)
	existingPoints := make(map[string]bool) // Кеш для проверки дубликатов в рамках одного файла

	// Используем WaitGroup для ожидания завершения всех горутин получения эмбеддингов
	var wg sync.WaitGroup
	// Канал для передачи готовых точек в основной поток
	pointsChan := make(chan *qdrant.PointStruct, totalMessages)
	// Канал для отслеживания ошибок получения эмбеддингов
	errorChan := make(chan error, totalMessages)
	// Мьютекс для безопасного доступа к счетчикам
	var countMutex sync.Mutex

	// Ограничение количества одновременных горутин (например, 10)
	semaphore := make(chan struct{}, 10)

	for i, msg := range messages {
		// Пропускаем сообщения без текста или ID
		if msg.Text == "" || msg.ID == 0 {
			countMutex.Lock()
			skippedCount++
			countMutex.Unlock()
			if qs.debug {
				log.Printf("[Qdrant Import DEBUG] Чат %d: Пропуск сообщения %d/%d (пустой текст или ID=0).", chatID, i+1, totalMessages)
			}
			continue
		}

		// Генерируем уникальный строковый идентификатор для генерации UUID
		uniqueIDStr := fmt.Sprintf("%d_%d", chatID, msg.ID)
		// Генерируем UUID v5
		pointUUID := uuid.NewSHA1(uuid.NameSpaceDNS, []byte(uniqueIDStr))
		pointIDStrForUUID := pointUUID.String() // Строковое представление UUID

		// Проверка на дубликат в текущем файле (чтобы не делать лишние запросы к Gemini)
		if existingPoints[pointIDStrForUUID] { // Проверяем по сгенерированному UUID
			countMutex.Lock()
			skippedCount++
			countMutex.Unlock()
			if qs.debug {
				log.Printf("[Qdrant Import DEBUG] Чат %d: Пропуск дубликата сообщения %d/%d (UUID: %s) в файле.", chatID, i+1, totalMessages, pointIDStrForUUID)
			}
			continue
		}
		existingPoints[pointIDStrForUUID] = true // Запоминаем UUID

		wg.Add(1)
		go func(m types.Message, uID string, index int) {
			defer wg.Done()
			semaphore <- struct{}{}
			defer func() { <-semaphore }()

			// 1. Получаем эмбеддинг
			embedding, err := qs.geminiClient.GetEmbedding(m.Text)
			if err != nil {
				log.Printf("[Qdrant Import ERROR Emb] Чат %d: Сообщение %d/%d (UUID: %s): Ошибка получения эмбеддинга: %v", chatID, index+1, totalMessages, uID, err)
				errorChan <- err
				return
			}
			if len(embedding) == 0 {
				log.Printf("[Qdrant Import WARN Emb] Чат %d: Сообщение %d/%d (UUID: %s): Получен пустой эмбеддинг.", chatID, index+1, totalMessages, uID)
				pointsChan <- nil
				return
			}

			// 2. Создаем Payload (используем MessagePayload)
			msgPayload := &MessagePayload{
				ChatID:       chatID,
				MessageID:    int(m.ID),
				Text:         m.Text,
				Date:         m.Timestamp,
				ImportSource: "batch_old",
				UniqueID:     uID, // Сохраняем сгенерированный UUID в payload
				Role:         m.Role,
			}
			if len(m.Entities) > 0 {
				entitiesBytes, err := json.Marshal(m.Entities)
				if err == nil {
					msgPayload.Entities = entitiesBytes
				} else {
					log.Printf("[Qdrant Import WARN] Чат %d, Msg %d: Не удалось сериализовать Entities при импорте: %v", chatID, m.ID, err)
				}
			}
			payloadMap := qs.messagePayloadToQdrantMap(msgPayload)

			// 3. Создаем Qdrant Point (используем UUID)
			point := &qdrant.PointStruct{
				Id:      &qdrant.PointId{PointIdOptions: &qdrant.PointId_Uuid{Uuid: uID}}, // Используем переданный UUID
				Vectors: &qdrant.Vectors{VectorsOptions: &qdrant.Vectors_Vector{Vector: &qdrant.Vector{Data: embedding}}},
				Payload: payloadMap,
			}
			pointsChan <- point

		}(msg, pointIDStrForUUID, i) // Передаем сгенерированный UUID в горутину
	}

	// Горутина для закрытия каналов после завершения всех воркеров
	go func() {
		wg.Wait()
		close(pointsChan)
		close(errorChan)
	}()

	// Собираем результаты из канала
	processedCount := 0
	var firstEmbError error
	for point := range pointsChan {
		processedCount++
		if point != nil {
			pointsBatch = append(pointsBatch, point)
			if len(pointsBatch) >= batchSize {
				// Отправляем батч в Qdrant
				batchImported, batchSkipped, batchErr := qs.upsertPointsBatch(pointsBatch)
				countMutex.Lock()
				importedCount += batchImported
				skippedCount += batchSkipped
				countMutex.Unlock()
				if batchErr != nil {
					log.Printf("[Qdrant Import ERROR Batch] Чат %d: Ошибка при отправке батча: %v", chatID, batchErr)
					// Можно решить, прерывать ли импорт при ошибке батча
					// err = batchErr // Сохраняем ошибку, но продолжаем
				}
				pointsBatch = pointsBatch[:0] // Очищаем батч
			}
		} else {
			// Сообщение было пропущено из-за ошибки эмбеддинга или пустого эмбеддинга
			countMutex.Lock()
			skippedCount++
			countMutex.Unlock()
		}
		// Прогресс-лог
		if processedCount%100 == 0 || processedCount == totalMessages {
			log.Printf("[Qdrant Import Progress] Чат %d: Обработано %d/%d сообщений...", chatID, processedCount, totalMessages)
		}
	}

	// Отправляем оставшийся батч, если он не пуст
	if len(pointsBatch) > 0 {
		batchImported, batchSkipped, batchErr := qs.upsertPointsBatch(pointsBatch)
		countMutex.Lock()
		importedCount += batchImported
		skippedCount += batchSkipped
		countMutex.Unlock()
		if batchErr != nil {
			log.Printf("[Qdrant Import ERROR Batch] Чат %d: Ошибка при отправке последнего батча: %v", chatID, batchErr)
			// err = batchErr
		}
	}

	// Проверяем ошибки получения эмбеддингов
	for embErr := range errorChan {
		if firstEmbError == nil {
			firstEmbError = embErr // Сохраняем первую ошибку эмбеддинга
		}
	}

	if firstEmbError != nil {
		log.Printf("[Qdrant Import WARN] Чат %d: Были ошибки при получении эмбеддингов. Первая ошибка: %v", chatID, firstEmbError)
		// Можно вернуть эту ошибку, если она критична
		// if err == nil { err = firstEmbError }
	}

	log.Printf("[Qdrant Import OK] Чат %d: Импорт из файла %s завершен. Импортировано: %d, Пропущено (дубликаты/ошибки): %d.",
		chatID, filePath, importedCount, skippedCount)

	return importedCount, skippedCount, err
}

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

	waitUpsert := true                                             // Ждать подтверждения операции
	resp, err := qs.client.Upsert(upsertCtx, &qdrant.UpsertPoints{ // Используем upsertCtx
		CollectionName: qs.collectionName,
		Points:         points,
		Wait:           &waitUpsert,
	})

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

// AddMessage - Сохраняет сообщение в Qdrant (адаптировано)

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
