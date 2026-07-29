package worker

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	"backend/internal/storage"

	"github.com/hibiken/asynq"
	"github.com/redis/go-redis/v9"
)

// Worker represents the asynq worker with all its dependencies
type Worker struct {
	asynqServer   *asynq.Server
	mux           *asynq.ServeMux
	filerUploader *storage.FilerUploader
	redisClient   *redis.Client
	config        Config
}

// NewWorker creates and initializes a new Worker instance
func NewWorker() *Worker {
	log.Println("Initializing worker...")

	// Load configuration
	config := LoadConfig()

	// Create asynq Redis connection options
	redisOpt := asynq.RedisClientOpt{
		Addr:     config.RedisAddr,
		Password: config.RedisPassword,
		DB:       config.RedisDB,
	}

	// Create asynq server with configuration
	asynqServer := asynq.NewServer(
		redisOpt,
		asynq.Config{
			Concurrency: config.Concurrency,
			Queues: map[string]int{
				config.QueueName: 10, // priority weight
				"critical":       20, // higher priority for critical tasks
			},
			// No RetryDelayFunc: tasks are enqueued with MaxRetry(0), so they
			// never retry at the asynq level. Transient failures are retried
			// inside the task around the specific fallible chunk (Filer upload).
			// Error handler
			ErrorHandler: asynq.ErrorHandlerFunc(func(ctx context.Context, task *asynq.Task, err error) {
				log.Printf("[ERROR] Task %s failed: %v", task.Type(), err)
			}),
			// Logging
			LogLevel: asynq.InfoLevel,
		},
	)

	log.Println("Asynq server created")

	// Create ServeMux for task routing
	mux := asynq.NewServeMux()

	// Initialize Filer uploader
	filerUploader := storage.NewFilerUploader(config.FilerURL)

	log.Println("Filer uploader created")

	// Redis client for publishing build-log streams (separate from the asynq
	// connection pool; used only for the pcb:logs:{taskID} streams).
	redisClient := redis.NewClient(&redis.Options{
		Addr:     config.RedisAddr,
		Password: config.RedisPassword,
		DB:       config.RedisDB,
	})

	log.Println("Redis log-stream client created")
	log.Println("Worker initialized successfully")

	return &Worker{
		asynqServer:   asynqServer,
		mux:           mux,
		filerUploader: filerUploader,
		redisClient:   redisClient,
		config:        config,
	}
}

// Start starts the worker and waits for tasks
func (w *Worker) Start() {
	log.Println("Starting Asynq worker...")

	// Run server in goroutine
	go func() {
		if err := w.asynqServer.Run(w.mux); err != nil {
			log.Fatalf("Could not run asynq server: %v", err)
		}
	}()

	log.Println("Worker started, waiting for tasks...")

	// Wait for termination signal
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGTERM, syscall.SIGINT)
	<-sigChan

	log.Println("Shutting down worker...")
	w.asynqServer.Shutdown()
	if err := w.redisClient.Close(); err != nil {
		log.Printf("Error closing Redis log-stream client: %v", err)
	}
	log.Println("Worker stopped")
}
