package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	_ "github.com/lib/pq" // PostgreSQL driver
)

// Убедимся, что PostgresStorage реализует интерфейс ChatHistoryStorage
var _ ChatHistoryStorage = (*PostgresStorage)(nil)

// PostgresStorage реализует ChatHistoryStorage с использованием PostgreSQL.
type PostgresStorage struct {
	db            *sql.DB
	contextWindow int
	debug         bool
}

// NewPostgresStorage создает и инициализирует новый экземпляр PostgresStorage.
func NewPostgresStorage(dbHost, dbPort, dbUser, dbPassword, dbName string, contextWindow int, debug bool) (*PostgresStorage, error) {
	connStr := fmt.Sprintf("host=%s port=%s user=%s password=%s dbname=%s sslmode=require",
		dbHost, dbPort, dbUser, dbPassword, dbName)

	db, err := sql.Open("postgres", connStr)
	if err != nil {
		return nil, fmt.Errorf("ошибка открытия соединения с PostgreSQL: %w", err)
	}

	// Проверяем соединение
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err = db.PingContext(ctx); err != nil {
		db.Close() // Закрываем соединение, если пинг не удался
		return nil, fmt.Errorf("не удалось подключиться к PostgreSQL: %w", err)
	}

	storage := &PostgresStorage{
		db:            db,
		contextWindow: contextWindow,
		debug:         debug,
	}

	// Создаем таблицу, если она не существует
	if err = storage.createTableIfNotExists(); err != nil {
		db.Close()
		return nil, fmt.Errorf("ошибка создания таблицы chat_messages: %w", err)
	}

	log.Printf("Хранилище PostgreSQL успешно инициализировано и подключено к '%s' на '%s:%s'", dbName, dbHost, dbPort)
	return storage, nil
}

// createTableIfNotExists создает таблицу для хранения сообщений чата.
func (ps *PostgresStorage) createTableIfNotExists() error {
	query := `
	CREATE TABLE IF NOT EXISTS chat_messages (
		chat_id BIGINT NOT NULL,
		message_id INT NOT NULL,
		user_id BIGINT,
		username VARCHAR(255),
		first_name VARCHAR(255),
		last_name VARCHAR(255),
		is_bot BOOLEAN,
		message_text TEXT,
		message_date TIMESTAMP WITH TIME ZONE NOT NULL,
		reply_to_message_id INT,
		entities JSONB, -- Для хранения MessageEntity
		raw_message JSONB, -- Для хранения всего объекта сообщения (на всякий случай)
		PRIMARY KEY (chat_id, message_id)
	);

	-- Добавляем индекс для ускорения выборки по дате
	CREATE INDEX IF NOT EXISTS idx_chat_messages_chat_id_date ON chat_messages (chat_id, message_date DESC);
	-- Добавляем индекс для user_id (может быть полезно для аналитики)
	CREATE INDEX IF NOT EXISTS idx_chat_messages_user_id ON chat_messages (user_id);
	`
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, err := ps.db.ExecContext(ctx, query)
	if err != nil {
		return fmt.Errorf("ошибка выполнения SQL для создания таблицы/индексов: %w", err)
	}
	log.Println("Таблица 'chat_messages' и индексы проверены/созданы.")
	return nil
}

// Close закрывает соединение с базой данных.
func (ps *PostgresStorage) Close() error {
	if ps.db != nil {
		log.Println("Закрытие соединения с PostgreSQL...")
		return ps.db.Close()
	}
	return nil
}

// --- Заглушки для методов интерфейса ChatHistoryStorage --- //
// --- Они будут реализованы в следующих шагах --- //

// AddMessage добавляет сообщение в базу данных PostgreSQL.
func (ps *PostgresStorage) AddMessage(chatID int64, message *tgbotapi.Message) {
	if message == nil {
		log.Printf("[PostgresStorage Add WARN] Попытка добавить nil сообщение для chatID %d", chatID)
		return
	}

	query := `
	INSERT INTO chat_messages (
		chat_id, message_id, user_id, username, first_name, last_name, is_bot, 
		message_text, message_date, reply_to_message_id, entities, raw_message
	) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
	ON CONFLICT (chat_id, message_id) DO NOTHING; -- Игнорируем дубликаты
	`

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Подготовка данных
	var userID sql.NullInt64
	var userName, firstName, lastName sql.NullString
	var isBot sql.NullBool
	if message.From != nil {
		userID = sql.NullInt64{Int64: message.From.ID, Valid: true}
		userName = sql.NullString{String: message.From.UserName, Valid: message.From.UserName != ""}
		firstName = sql.NullString{String: message.From.FirstName, Valid: message.From.FirstName != ""}
		lastName = sql.NullString{String: message.From.LastName, Valid: message.From.LastName != ""}
		isBot = sql.NullBool{Bool: message.From.IsBot, Valid: true}
	}

	messageDate := time.Unix(int64(message.Date), 0)
	messageText := sql.NullString{String: message.Text, Valid: message.Text != ""}

	var replyToMessageID sql.NullInt32
	if message.ReplyToMessage != nil {
		replyToMessageID = sql.NullInt32{Int32: int32(message.ReplyToMessage.MessageID), Valid: true}
	}

	entitiesJSON := jsonify(message.Entities)
	rawMessageJSON := jsonify(message)

	_, err := ps.db.ExecContext(ctx, query,
		chatID, message.MessageID, userID, userName, firstName, lastName, isBot,
		messageText, messageDate, replyToMessageID, entitiesJSON, rawMessageJSON,
	)

	if err != nil {
		log.Printf("[PostgresStorage Add ERROR] Ошибка добавления сообщения (ChatID: %d, MsgID: %d): %v", chatID, message.MessageID, err)
	} else if ps.debug {
		// log.Printf("[PostgresStorage Add DEBUG] Сообщение (ChatID: %d, MsgID: %d) добавлено/проигнорировано.", chatID, message.MessageID)
	}
}

// GetMessages возвращает последние N (contextWindow) сообщений для чата из БД.
func (ps *PostgresStorage) GetMessages(chatID int64) []*tgbotapi.Message {
	query := `
	SELECT raw_message
	FROM chat_messages
	WHERE chat_id = $1
	ORDER BY message_date DESC
	LIMIT $2;
	`
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	rows, err := ps.db.QueryContext(ctx, query, chatID, ps.contextWindow)
	if err != nil {
		log.Printf("[PostgresStorage GetMessages ERROR] Ошибка запроса сообщений для chatID %d: %v", chatID, err)
		return nil
	}
	defer rows.Close()

	var messages []*tgbotapi.Message
	for rows.Next() {
		var rawMessageJSON []byte
		if err := rows.Scan(&rawMessageJSON); err != nil {
			log.Printf("[PostgresStorage GetMessages ERROR] Ошибка сканирования raw_message для chatID %d: %v", chatID, err)
			continue // Пропускаем это сообщение, но пытаемся прочитать остальные
		}

		var msg tgbotapi.Message
		if err := json.Unmarshal(rawMessageJSON, &msg); err != nil {
			log.Printf("[PostgresStorage GetMessages ERROR] Ошибка десериализации raw_message для chatID %d: %v", chatID, err)
			continue // Пропускаем некорректное сообщение
		}
		messages = append(messages, &msg)
	}

	if err := rows.Err(); err != nil {
		log.Printf("[PostgresStorage GetMessages ERROR] Ошибка после итерации по строкам для chatID %d: %v", chatID, err)
	}

	// Так как мы выбирали ORDER BY DESC, нужно перевернуть срез, чтобы сообщения были в хронологическом порядке
	for i, j := 0, len(messages)-1; i < j; i, j = i+1, j-1 {
		messages[i], messages[j] = messages[j], messages[i]
	}

	if ps.debug {
		log.Printf("[PostgresStorage GetMessages DEBUG] Запрошено %d сообщений для chatID %d.", len(messages), chatID)
	}

	return messages
}

// GetMessagesSince возвращает сообщения для чата, начиная с указанного времени.
func (ps *PostgresStorage) GetMessagesSince(chatID int64, since time.Time) []*tgbotapi.Message {
	query := `
	SELECT raw_message
	FROM chat_messages
	WHERE chat_id = $1 AND message_date >= $2
	ORDER BY message_date ASC; -- Сразу сортируем в хронологическом порядке
	`
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	rows, err := ps.db.QueryContext(ctx, query, chatID, since)
	if err != nil {
		log.Printf("[PostgresStorage GetSince ERROR] Ошибка запроса сообщений для chatID %d с %s: %v", chatID, since, err)
		return nil
	}
	defer rows.Close()

	var messages []*tgbotapi.Message
	for rows.Next() {
		var rawMessageJSON []byte
		if err := rows.Scan(&rawMessageJSON); err != nil {
			log.Printf("[PostgresStorage GetSince ERROR] Ошибка сканирования raw_message для chatID %d: %v", chatID, err)
			continue
		}

		var msg tgbotapi.Message
		if err := json.Unmarshal(rawMessageJSON, &msg); err != nil {
			log.Printf("[PostgresStorage GetSince ERROR] Ошибка десериализации raw_message для chatID %d: %v", chatID, err)
			continue
		}
		messages = append(messages, &msg)
	}

	if err := rows.Err(); err != nil {
		log.Printf("[PostgresStorage GetSince ERROR] Ошибка после итерации по строкам для chatID %d: %v", chatID, err)
	}

	if ps.debug {
		log.Printf("[PostgresStorage GetSince DEBUG] Запрошено %d сообщений для chatID %d с %s.", len(messages), chatID, since)
	}

	return messages
}

func (ps *PostgresStorage) LoadChatHistory(chatID int64) ([]*tgbotapi.Message, error) {
	// В PostgresStorage этот метод не нужен так, как в FileStorage, так как данные всегда в БД.
	// Но он нужен для реализации интерфейса.
	// Можно просто вернуть результат GetMessages или nil, nil.
	log.Printf("[PostgresStorage STUB] LoadChatHistory вызван для chatID %d (возвращает nil)", chatID)
	return nil, nil // Заглушка, этот метод не актуален для БД-хранилища в том же смысле
}

// SaveChatHistory для PostgresStorage ничего не делает, так как данные сохраняются в AddMessage.
func (ps *PostgresStorage) SaveChatHistory(chatID int64) error {
	// В PostgresStorage этот метод не нужен, так как сообщения сохраняются сразу в AddMessage.
	// Но он нужен для реализации интерфейса.
	if ps.debug {
		// log.Printf("[PostgresStorage SaveChatHistory DEBUG] Вызван для chatID %d (ничего не делает)", chatID)
	}
	return nil // Заглушка
}

// ClearChatHistory удаляет все сообщения для указанного чата из базы данных.
func (ps *PostgresStorage) ClearChatHistory(chatID int64) error {
	query := `DELETE FROM chat_messages WHERE chat_id = $1;`
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	result, err := ps.db.ExecContext(ctx, query, chatID)
	if err != nil {
		log.Printf("[PostgresStorage Clear ERROR] Ошибка удаления истории для chatID %d: %v", chatID, err)
		return fmt.Errorf("ошибка удаления истории чата %d: %w", chatID, err)
	}

	rowsAffected, _ := result.RowsAffected()
	if ps.debug {
		log.Printf("[PostgresStorage Clear DEBUG] Удалена история для chatID %d (%d строк).", chatID, rowsAffected)
	}

	return nil
}

// AddMessagesToContext для PostgresStorage не имеет смысла в том же виде, что и для FileStorage.
// Сообщения добавляются по одному через AddMessage.
func (ps *PostgresStorage) AddMessagesToContext(chatID int64, messages []*tgbotapi.Message) {
	// В PostgresStorage этот метод не нужен так, как в FileStorage.
	// Сообщения добавляются по одному через AddMessage.
	// Но он нужен для реализации интерфейса.
	if ps.debug {
		// log.Printf("[PostgresStorage AddMessagesToContext DEBUG] Вызван для chatID %d с %d сообщениями (ничего не делает)", chatID, len(messages))
	}
	// Можно опционально реализовать пакетное добавление через AddMessage в цикле, если это будет необходимо.
	// for _, msg := range messages {
	// 	 ps.AddMessage(chatID, msg)
	// }
}

// GetAllChatIDs возвращает список уникальных ID чатов из базы данных.
func (ps *PostgresStorage) GetAllChatIDs() ([]int64, error) {
	query := `SELECT DISTINCT chat_id FROM chat_messages;`
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	rows, err := ps.db.QueryContext(ctx, query)
	if err != nil {
		log.Printf("[PostgresStorage GetAllChatIDs ERROR] Ошибка запроса ID чатов: %v", err)
		return nil, fmt.Errorf("ошибка получения списка chatID: %w", err)
	}
	defer rows.Close()

	var chatIDs []int64
	for rows.Next() {
		var chatID int64
		if err := rows.Scan(&chatID); err != nil {
			log.Printf("[PostgresStorage GetAllChatIDs ERROR] Ошибка сканирования chatID: %v", err)
			continue // Пропускаем некорректный ID
		}
		chatIDs = append(chatIDs, chatID)
	}

	if err := rows.Err(); err != nil {
		log.Printf("[PostgresStorage GetAllChatIDs ERROR] Ошибка после итерации по строкам: %v", err)
	}

	if ps.debug {
		log.Printf("[PostgresStorage GetAllChatIDs DEBUG] Найдено %d уникальных ChatID.", len(chatIDs))
	}

	return chatIDs, nil
}

// Вспомогательная функция для безопасного получения строкового представления JSON
func jsonify(v interface{}) sql.NullString {
	if v == nil {
		return sql.NullString{}
	}
	data, err := json.Marshal(v)
	if err != nil {
		log.Printf("Ошибка JSON маршалинга для БД: %v", err)
		return sql.NullString{}
	}
	return sql.NullString{
		String: string(data),
		Valid:  true,
	}
}
