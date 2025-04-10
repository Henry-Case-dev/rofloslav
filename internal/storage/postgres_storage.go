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

	// Создаем таблицы, если их нет
	if err := storage.createTablesIfNotExists(); err != nil {
		db.Close()
		return nil, fmt.Errorf("ошибка создания таблиц PostgreSQL: %w", err)
	}

	log.Println("Таблицы PostgreSQL проверены/созданы.")

	return storage, nil
}

// createTablesIfNotExists создает необходимые таблицы в базе данных, если они не существуют.
func (ps *PostgresStorage) createTablesIfNotExists() error {
	ctx := context.Background()
	tx, err := ps.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("ошибка начала транзакции: %w", err)
	}
	defer tx.Rollback()

	// Таблица для сообщений чата
	chatMessagesQuery := `
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
	if _, err := tx.ExecContext(ctx, chatMessagesQuery); err != nil {
		return fmt.Errorf("ошибка создания таблицы chat_messages: %w", err)
	}
	log.Println("Индекс для chat_messages проверен/создан.")

	// Таблица для профилей пользователей
	profilesTableQuery := `
	CREATE TABLE IF NOT EXISTS user_profiles (
		chat_id BIGINT NOT NULL,
		user_id BIGINT NOT NULL,
		username VARCHAR(255),
		first_name VARCHAR(255),
		last_name VARCHAR(255),
		real_name TEXT DEFAULT '', -- Реальное имя
		bio TEXT DEFAULT '',       -- Био/Описание
		last_seen TIMESTAMPTZ,     -- Время последнего сообщения
		created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		PRIMARY KEY (chat_id, user_id) -- Составной первичный ключ
	);`
	if _, err := tx.ExecContext(ctx, profilesTableQuery); err != nil {
		return fmt.Errorf("ошибка создания таблицы user_profiles: %w", err)
	}
	log.Println("Таблица user_profiles проверена/создана.")

	// Триггер для автоматического обновления updated_at в user_profiles
	triggerFunctionQuery := `
	CREATE OR REPLACE FUNCTION update_updated_at_column()
	RETURNS TRIGGER AS $$
	BEGIN
	   NEW.updated_at = NOW();
	   RETURN NEW;
	END;
	$$ language 'plpgsql';`
	if _, err := tx.ExecContext(ctx, triggerFunctionQuery); err != nil {
		return fmt.Errorf("ошибка создания триггерной функции update_updated_at_column: %w", err)
	}

	triggerQuery := `
	DROP TRIGGER IF EXISTS update_user_profiles_updated_at ON user_profiles; -- Удаляем старый триггер, если он есть
	CREATE TRIGGER update_user_profiles_updated_at
	BEFORE UPDATE ON user_profiles
	FOR EACH ROW
	EXECUTE FUNCTION update_updated_at_column();`
	if _, err := tx.ExecContext(ctx, triggerQuery); err != nil {
		return fmt.Errorf("ошибка создания триггера для user_profiles: %w", err)
	}
	log.Println("Триггер updated_at для user_profiles проверен/создан.")

	// Коммит транзакции
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("ошибка коммита транзакции: %w", err)
	}

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

// --- Методы для профилей пользователей ---

// GetUserProfile возвращает профиль пользователя из PostgreSQL.
func (ps *PostgresStorage) GetUserProfile(chatID int64, userID int64) (*UserProfile, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	query := `
		SELECT username, first_name, last_name, real_name, bio, last_seen, created_at, updated_at
		FROM user_profiles
		WHERE chat_id = $1 AND user_id = $2;
	`
	row := ps.db.QueryRowContext(ctx, query, chatID, userID)

	var profile UserProfile
	profile.ChatID = chatID // Заполняем известные поля
	profile.UserID = userID

	// Используем NullString и NullTime для полей, которые могут быть NULL
	var username, firstName, lastName, realName, bio sql.NullString
	var lastSeen, createdAt, updatedAt sql.NullTime

	err := row.Scan(
		&username, &firstName, &lastName, &realName, &bio, &lastSeen, &createdAt, &updatedAt,
	)

	if err != nil {
		if err == sql.ErrNoRows {
			if ps.debug {
				log.Printf("DEBUG: Профиль пользователя userID %d в чате %d не найден в PostgreSQL.", userID, chatID)
			}
			return nil, nil // Не найдено - не ошибка
		}
		log.Printf("ERROR: Ошибка получения профиля пользователя из PostgreSQL (чат %d, user %d): %v", chatID, userID, err)
		return nil, fmt.Errorf("ошибка запроса профиля пользователя: %w", err)
	}

	// Заполняем профиль данными из базы, проверяя Valid
	if username.Valid {
		profile.Username = username.String
	}
	if firstName.Valid {
		profile.FirstName = firstName.String
	}
	if lastName.Valid {
		profile.LastName = lastName.String
	}
	if realName.Valid {
		profile.RealName = realName.String
	}
	if bio.Valid {
		profile.Bio = bio.String
	}
	if lastSeen.Valid {
		profile.LastSeen = lastSeen.Time
	}
	if createdAt.Valid {
		profile.CreatedAt = createdAt.Time
	}
	if updatedAt.Valid {
		profile.UpdatedAt = updatedAt.Time
	}

	if ps.debug {
		log.Printf("DEBUG: Профиль пользователя userID %d в чате %d успешно получен из PostgreSQL.", userID, chatID)
	}
	return &profile, nil
}

// SetUserProfile создает или обновляет профиль пользователя в PostgreSQL (UPSERT).
func (ps *PostgresStorage) SetUserProfile(profile *UserProfile) error {
	if profile == nil || profile.ChatID == 0 || profile.UserID == 0 {
		return fmt.Errorf("невалидный профиль для сохранения (nil, chat_id=0 или user_id=0)")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Используем INSERT ... ON CONFLICT ... DO UPDATE для UPSERT
	query := `
		INSERT INTO user_profiles (
			chat_id, user_id, username, first_name, last_name, real_name, bio, last_seen, created_at, updated_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, NOW(), NOW())
		ON CONFLICT (chat_id, user_id) DO UPDATE SET
			username = EXCLUDED.username,
			first_name = EXCLUDED.first_name,
			last_name = EXCLUDED.last_name,
			real_name = EXCLUDED.real_name,
			bio = EXCLUDED.bio,
			last_seen = EXCLUDED.last_seen,
			updated_at = NOW(); -- Обновляем updated_at при обновлении (триггер тоже это делает, но так надежнее)
	`

	_, err := ps.db.ExecContext(ctx, query,
		profile.ChatID,
		profile.UserID,
		sql.NullString{String: profile.Username, Valid: profile.Username != ""},
		sql.NullString{String: profile.FirstName, Valid: profile.FirstName != ""},
		sql.NullString{String: profile.LastName, Valid: profile.LastName != ""},
		sql.NullString{String: profile.RealName, Valid: true}, // real_name и bio всегда Valid, но могут быть пустыми
		sql.NullString{String: profile.Bio, Valid: true},
		sql.NullTime{Time: profile.LastSeen, Valid: !profile.LastSeen.IsZero()}, // Сохраняем, если не нулевое время
	)

	if err != nil {
		log.Printf("ERROR: Ошибка сохранения/обновления профиля пользователя в PostgreSQL (чат %d, user %d): %v", profile.ChatID, profile.UserID, err)
		return fmt.Errorf("ошибка сохранения профиля: %w", err)
	}

	if ps.debug {
		log.Printf("DEBUG: Профиль пользователя userID %d в чате %d успешно сохранен/обновлен в PostgreSQL.", profile.UserID, profile.ChatID)
	}
	return nil
}

// GetAllUserProfiles возвращает все профили пользователей для указанного чата из PostgreSQL.
func (ps *PostgresStorage) GetAllUserProfiles(chatID int64) ([]*UserProfile, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	query := `
		SELECT user_id, username, first_name, last_name, real_name, bio, last_seen, created_at, updated_at
		FROM user_profiles
		WHERE chat_id = $1
		ORDER BY last_seen DESC NULLS LAST, updated_at DESC; -- Сортируем для возможной релевантности
	`
	rows, err := ps.db.QueryContext(ctx, query, chatID)
	if err != nil {
		log.Printf("ERROR: Ошибка получения всех профилей пользователей из PostgreSQL (чат %d): %v", chatID, err)
		return nil, fmt.Errorf("ошибка запроса профилей: %w", err)
	}
	defer rows.Close()

	var profiles []*UserProfile
	for rows.Next() {
		var profile UserProfile
		profile.ChatID = chatID // Заполняем chatID

		var userID int64
		var username, firstName, lastName, realName, bio sql.NullString
		var lastSeen, createdAt, updatedAt sql.NullTime

		if err := rows.Scan(
			&userID, &username, &firstName, &lastName, &realName, &bio, &lastSeen, &createdAt, &updatedAt,
		); err != nil {
			log.Printf("ERROR: Ошибка сканирования строки профиля пользователя PostgreSQL (чат %d): %v", chatID, err)
			continue
		}

		profile.UserID = userID
		if username.Valid {
			profile.Username = username.String
		}
		if firstName.Valid {
			profile.FirstName = firstName.String
		}
		if lastName.Valid {
			profile.LastName = lastName.String
		}
		if realName.Valid {
			profile.RealName = realName.String
		}
		if bio.Valid {
			profile.Bio = bio.String
		}
		if lastSeen.Valid {
			profile.LastSeen = lastSeen.Time
		}
		if createdAt.Valid {
			profile.CreatedAt = createdAt.Time
		}
		if updatedAt.Valid {
			profile.UpdatedAt = updatedAt.Time
		}

		profiles = append(profiles, &profile)
	}

	if ps.debug {
		log.Printf("DEBUG: Получено %d профилей пользователей из PostgreSQL для чата %d.", len(profiles), chatID)
	}
	return profiles, nil
}
