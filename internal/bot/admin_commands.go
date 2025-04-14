package bot

import (
	"fmt"
	"log"
	"strings"
	"time"

	// Нужен для проверки типа хранилища
	"github.com/Henry-Case-dev/rofloslav/internal/storage"
)

// runBackfillEmbeddings выполняет процесс заполнения эмбеддингов для сообщений чата.
// Запускается в отдельной горутине.
func (b *Bot) runBackfillEmbeddings(chatID int64) {
	log.Printf("[Backfill START] Chat %d: Начало процесса бэкфилла эмбеддингов.", chatID)

	// Убедимся, что хранилище - это MongoDB
	mongoStore, ok := b.storage.(*storage.MongoStorage)
	if !ok {
		log.Printf("[Backfill ERROR] Chat %d: Хранилище не является MongoStorage. Бэкфилл невозможен.", chatID)
		b.sendReply(chatID, "❌ Ошибка: Бэкфилл возможен только для MongoDB.")
		return
	}

	processedCount := 0
	errorCount := 0
	totalMessagesEstimated := int64(-1) // -1 означает, что еще не оценено

	// Получаем оценку общего количества сообщений для прогресса
	estimatedCount, errEst := mongoStore.GetTotalMessagesCount(chatID)
	if errEst == nil {
		totalMessagesEstimated = estimatedCount
		log.Printf("[Backfill INFO] Chat %d: Примерное общее количество сообщений: %d", chatID, totalMessagesEstimated)
	} else {
		log.Printf("[Backfill WARN] Chat %d: Не удалось оценить общее количество сообщений: %v", chatID, errEst)
	}

	startTime := time.Now()

	// Набор для отслеживания ID, которые постоянно не модифицируются
	failedToModifyIDs := make(map[int]bool)

	// Итерация по сообщениям без эмбеддингов пакетами
	for {
		// Проверяем сигнал остановки бота
		select {
		case <-b.stop:
			log.Printf("[Backfill STOP] Chat %d: Получен сигнал остановки бота. Прерывание бэкфилла.", chatID)
			b.sendReply(chatID, "⚠️ Процесс бэкфилла прерван из-за остановки бота.")
			return
		default:
			// Продолжаем
		}

		// --- Собираем ID для пропуска ---
		skipIDs := make([]int, 0, len(failedToModifyIDs))
		for id := range failedToModifyIDs {
			skipIDs = append(skipIDs, id)
		}

		// Ищем следующий пакет сообщений без message_vector, пропуская проблемные ID
		messagesToProcess, findErr := mongoStore.FindMessagesWithoutEmbedding(chatID, b.config.BackfillBatchSize, skipIDs)
		if findErr != nil {
			log.Printf("[Backfill ERROR] Chat %d: Ошибка поиска сообщений без эмбеддинга (пропущено %d ID): %v", chatID, len(skipIDs), findErr)
			errorCount++
			// Решаем, стоит ли продолжать или прервать? Пока продолжим, но сообщим об ошибке.
			b.sendReply(chatID, fmt.Sprintf("❌ Ошибка поиска сообщений для обработки (пакет %d). Пробую продолжить.", (processedCount/b.config.BackfillBatchSize)+1))
			time.Sleep(5 * time.Second) // Небольшая пауза после ошибки
			continue
		}

		// Если сообщений для обработки больше нет
		if len(messagesToProcess) == 0 {
			log.Printf("[Backfill INFO] Chat %d: Не найдено больше сообщений без эмбеддингов.", chatID)
			break // Выходим из цикла
		}

		log.Printf("[Backfill PROCESS] Chat %d: Обработка пакета из %d сообщений (Всего успешно обновлено: %d)...", chatID, len(messagesToProcess), processedCount)

		batchStartTime := time.Now()
		batchErrorCount := 0
		processedInBatch := 0 // Счетчик успешно обработанных в этом пакете

		// Обрабатываем пакет
		for _, msg := range messagesToProcess {
			// --- Проверка, не зациклились ли мы на этом ID ---
			if failedToModifyIDs[msg.MessageID] {
				log.Printf("[Backfill SKIP LOOP] Chat %d, Msg %d: Пропуск, так как ранее не удалось модифицировать.", chatID, msg.MessageID)
				continue // Пропускаем это сообщение в текущем пакете
			}

			// Собираем текст
			var textToEmbed string
			if msg.Text != "" {
				textToEmbed = msg.Text
			} else if msg.Caption != "" {
				textToEmbed = msg.Caption
			}

			if textToEmbed == "" {
				log.Printf("[Backfill SKIP] Chat %d, Msg %d: Пустой текст, пропуск генерации эмбеддинга.", chatID, msg.MessageID)
				continue
			}

			// Генерируем эмбеддинг
			vector, embedErr := b.llm.EmbedContent(textToEmbed)
			if embedErr != nil {
				log.Printf("[Backfill ERROR] Chat %d, Msg %d: Ошибка генерации эмбеддинга: %v", chatID, msg.MessageID, embedErr)
				errorCount++
				batchErrorCount++
				// Если ошибка - лимит запросов, делаем большую паузу
				if strings.Contains(embedErr.Error(), "лимит запросов") || strings.Contains(embedErr.Error(), "429") {
					log.Printf("[Backfill PAUSE] Chat %d: Обнаружен лимит запросов API. Пауза на %v...", chatID, b.config.BackfillBatchDelaySeconds)
					time.Sleep(b.config.BackfillBatchDelaySeconds)
					// Повторяем попытку для этого же сообщения? Или пропускаем?
					// Пока пропустим, чтобы не усложнять цикл.
					continue
				}
				// Пропускаем обновление для этого сообщения
				continue
			}

			// Обновляем документ в MongoDB
			updateErr := mongoStore.UpdateMessageEmbedding(chatID, msg.MessageID, vector)
			if updateErr != nil {
				// Проверяем, является ли ошибка "не модифицирован"
				if strings.Contains(updateErr.Error(), "не модифицирован") {
					log.Printf("[Backfill WARN] Chat %d, Msg %d: Пропуск обновления, документ не модифицирован (вектор уже может существовать). Отмечаем ID %d.", chatID, msg.MessageID, msg.MessageID)
					// Отмечаем этот ID, чтобы пропустить его в следующих пакетах этого запуска
					failedToModifyIDs[msg.MessageID] = true
				} else {
					// Другая ошибка обновления
					log.Printf("[Backfill ERROR] Chat %d, Msg %d: Ошибка обновления эмбеддинга в MongoDB: %v", chatID, msg.MessageID, updateErr)
					errorCount++
					batchErrorCount++
				}
				continue // Пропускаем увеличение processedCount
			}
			// Успешное обновление
			processedCount++
			processedInBatch++
			// Сбрасываем флаг ошибки для этого ID, если он был
			delete(failedToModifyIDs, msg.MessageID)
		}

		batchDuration := time.Since(batchStartTime)
		log.Printf("[Backfill BATCH OK] Chat %d: Пакет обработан за %v. Успешно обновлено в пакете: %d. Ошибок (не считая 'не мод.'): %d.", chatID, batchDuration, processedInBatch, batchErrorCount)

		// Отправляем промежуточный статус каждые N пакетов или M сообщений
		if processedCount > 0 && processedCount%(b.config.BackfillBatchSize*5) == 0 { // Каждые 5 пакетов
			progressPercent := -1.0
			if totalMessagesEstimated > 0 {
				progressPercent = (float64(processedCount) / float64(totalMessagesEstimated)) * 100
			}
			elapsedTime := time.Since(startTime)
			statusMsg := fmt.Sprintf("⏳ Прогресс бэкфилла: Обработано ~%d сообщений. Ошибок: %d. Время: %v.", processedCount, errorCount, elapsedTime.Round(time.Second))
			if progressPercent >= 0 {
				statusMsg = fmt.Sprintf("⏳ Прогресс бэкфилла: Обработано ~%d (%.1f%%) сообщений. Ошибок: %d. Время: %v.", processedCount, progressPercent, errorCount, elapsedTime.Round(time.Second))
			}
			b.sendReply(chatID, statusMsg)
		}

		// Задержка между пакетами, чтобы не перегружать API/DB
		if len(messagesToProcess) == b.config.BackfillBatchSize { // Только если пакет был полным
			log.Printf("[Backfill DELAY] Chat %d: Задержка перед следующим пакетом: %v", chatID, b.config.BackfillBatchDelaySeconds)
			time.Sleep(b.config.BackfillBatchDelaySeconds)
		}
	}

	// Завершение
	elapsedTime := time.Since(startTime)
	finalMsg := fmt.Sprintf(`✅ Бэкфилл эмбеддингов завершен.
Обработано сообщений: %d
Ошибок генерации/обновления: %d
Затрачено времени: %v`, processedCount, errorCount, elapsedTime.Round(time.Second))
	log.Printf("[Backfill COMPLETE] Chat %d: %s", chatID, finalMsg)
	b.sendReply(chatID, finalMsg)
}
