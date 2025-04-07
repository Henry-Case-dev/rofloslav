package main

import (
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/Henry-Case-dev/rofloslav/internal/bot"
	"github.com/Henry-Case-dev/rofloslav/internal/config"
)

// handleRoot - простой обработчик HTTP запросов
func handleRoot(w http.ResponseWriter, r *http.Request) {
	// Логируем каждый входящий запрос к этому обработчику
	log.Printf("--> HTTP Request Received: Method=%s, Path=%s, RemoteAddr=%s", r.Method, r.URL.Path, r.RemoteAddr)
	fmt.Fprintf(w, "Hello from Rofloslav Bot server!")
	// Логируем после отправки ответа
	log.Printf("<-- HTTP Response Sent for: %s %s", r.Method, r.URL.Path)
}

func main() {
	log.Println("=== Application Starting ===")
	log.Printf("Timestamp: %s", time.Now().UTC().Format(time.RFC3339))

	cfg, err := config.Load()
	if err != nil {
		log.Printf("!!! FATAL: Ошибка загрузки конфигурации: %v", err)
		time.Sleep(15 * time.Second)
		panic(fmt.Sprintf("Configuration error: %v", err))
	}
	log.Println("--- Configuration Loaded ---")

	botInstance, err := bot.New(cfg)
	if err != nil {
		log.Printf("!!! FATAL: Ошибка создания бота: %v", err)
		time.Sleep(15 * time.Second)
		panic(fmt.Sprintf("Bot creation error: %v", err))
	}
	log.Println("--- Bot Initialized ---")

	// Запускаем бота в отдельной горутине
	go func() {
		if startErr := botInstance.Start(); startErr != nil {
			log.Printf("!!! CRITICAL: Критическая ошибка запуска бота: %v", startErr)
		}
	}()
	log.Println("--- Bot Start Goroutine Launched ---")
	log.Println("Бот запущен.")

	// --- Запуск Dummy HTTP сервера ---
	http.HandleFunc("/", handleRoot) // Регистрируем обработчик
	serverAddr := ":80"              // Порт из amvera.yml
	log.Printf("--- Starting HTTP server on %s ---", serverAddr)

	go func() {
		// Логируем перед запуском сервера
		log.Printf("[HTTP Goroutine] Attempting to start HTTP server on %s", serverAddr)
		if httpErr := http.ListenAndServe(serverAddr, nil); httpErr != nil {
			// Логируем ошибку, если ListenAndServe вернул ее
			log.Printf("!!! [HTTP Goroutine] HTTP Server Error: %v", httpErr)
		}
		// Логируем, если ListenAndServe завершился (даже без ошибки, хотя это маловероятно)
		log.Printf("[HTTP Goroutine] ListenAndServe on %s finished.", serverAddr)
	}()
	// Добавляем лог сразу после запуска горутины
	log.Printf("--- HTTP Server Goroutine Launched on %s ---", serverAddr)
	// --- Конец HTTP сервера ---

	log.Printf("--- Application Ready. Waiting indefinitely. ---")

	// Ожидаем бесконечно, игнорируем сигналы завершения.
	// Это нужно для Amvera, чтобы контейнер оставался RUNNING.
	select {}

	// Этот код больше никогда не будет выполнен в Amvera.
	// Оставляем его закомментированным на случай локальных тестов.
	/*
		log.Println("Остановка бота (из main)...") // Добавляем лог для ясности
		bot.Stop()
		log.Println("Приложение остановлено")
	*/
}
