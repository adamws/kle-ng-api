package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime/debug"
	"time"

	"backend/internal/common"
	"backend/internal/failedtasks"
	"backend/internal/kicad"
	"backend/internal/logstream"

	"github.com/hibiken/asynq"
)

const (
	TaskTypeGenerateKicad = "generate_kicad_project"
)

// RegisterTasks registers all task handlers with the mux
func (w *Worker) RegisterTasks() {
	log.Println("Registering task: generate_kicad_project")
	w.mux.HandleFunc(TaskTypeGenerateKicad, w.HandleGenerateKicadProject)
}

// HandleGenerateKicadProject is the asynq task handler for KiCad project generation
func (w *Worker) HandleGenerateKicadProject(ctx context.Context, task *asynq.Task) (err error) {
	// Extract task ID (asynq provides this via task metadata)
	taskID := task.ResultWriter().TaskID()
	startedAt := time.Now()

	log.Printf("[Task %s] Starting KiCad project generation", taskID)

	// Build-log stream publisher for this task. Streaming is best-effort: a
	// failure to publish never affects the build outcome. Because a task runs at
	// most once (MaxRetry 0), each stream sees exactly one attempt and ends with
	// a single terminal "end" marker.
	pub := logstream.NewPublisher(w.redisClient, taskID)

	// Publish the terminal "end" marker on every exit path (error, success, or
	// recovered panic). buildStatus defaults to "failure" and is flipped to
	// "success" only on the happy path, so any early return reports a failure.
	// Registered before the panic-recovery defer below so it runs last, after
	// recovery has completed. A fresh context is used because the task context
	// may already be cancelled (e.g. on timeout) by the time we get here.
	buildStatus := "failure"
	defer func() {
		endCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = pub.PublishEnd(endCtx, buildStatus)
	}()

	// Keep the failed task (request, error, build logs) for troubleshooting.
	// Registered before the panic-recovery defer so a recovered panic, which
	// sets err, is recorded too.
	var workDir string
	defer func() {
		if err != nil {
			w.saveFailedTask(task, err, startedAt, workDir)
		}
	}()

	// Update progress: Starting
	if err := w.reportProgress(task, "Initializing task"); err != nil {
		log.Printf("[Task %s] Failed to report progress: %v", taskID, err)
	}

	// Defer panic recovery
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[Task %s] Panic recovered: %v", taskID, r)
			log.Printf("[Task %s] Stack trace: %s", taskID, debug.Stack())

			// Report error progress
			w.reportProgress(task, fmt.Sprintf("Panic: %v", r))
			err = fmt.Errorf("panic: %v", r)
		}
	}()

	// Parse task payload
	var taskRequest map[string]interface{}
	if err := json.Unmarshal(task.Payload(), &taskRequest); err != nil {
		log.Printf("[Task %s] Failed to parse request JSON: %v", taskID, err)
		w.reportProgress(task, "Invalid JSON payload")
		return fmt.Errorf("failed to parse request JSON: %w", err)
	}

	// Update progress: Generating PCB
	if err := w.reportProgress(task, "Generating KiCad PCB files"); err != nil {
		log.Printf("[Task %s] Failed to report progress: %v", taskID, err)
	}
	_ = pub.PublishLine(ctx, logstream.SourceWorker, "Generating KiCad PCB files")

	// Generate KiCad project (pass context for cancellation support)
	var files *common.ProjectFiles
	workDir, files, err = kicad.NewPCB(ctx, taskID, taskRequest, pub)
	if err != nil {
		log.Printf("[Task %s] PCB generation failed: %v", taskID, err)
		errMsg := fmt.Sprintf("PCB generation failed: %v", err)
		w.reportProgress(task, errMsg)
		return errors.New(errMsg)
	}

	log.Printf("[Task %s] PCB generated successfully, work directory: %s", taskID, workDir)

	// Update progress: Uploading to S3
	if err := w.reportProgress(task, "Uploading files to storage"); err != nil {
		log.Printf("[Task %s] Failed to report progress: %v", taskID, err)
	}
	_ = pub.PublishLine(ctx, logstream.SourceWorker, "Uploading files to storage")

	// Upload to Filer
	if err := w.filerUploader.UploadToStorage(ctx, taskID, workDir, files.Renders); err != nil {
		log.Printf("[Task %s] Error uploading to storage: %v", taskID, err)
		w.reportProgress(task, fmt.Sprintf("Upload failed: %v", err))
		return fmt.Errorf("Filer upload failed: %w", err)
	}

	log.Printf("[Task %s] Files uploaded to Filer successfully", taskID)

	// Complete. The final result also carries the file manifest so the frontend
	// learns which renders/artifacts were produced.
	if err := w.reportResult(task, files); err != nil {
		log.Printf("[Task %s] Failed to report final result: %v", taskID, err)
	}

	_ = pub.PublishLine(ctx, logstream.SourceWorker, "Task completed successfully")

	log.Printf("[Task %s] Task completed successfully", taskID)
	buildStatus = "success"
	return nil
}

// saveFailedTask stores the failed task in the failed tasks directory. It is
// best-effort: errors are logged and never change the task outcome.
func (w *Worker) saveFailedTask(task *asynq.Task, taskErr error, startedAt time.Time, workDir string) {
	taskID := task.ResultWriter().TaskID()
	rec := failedtasks.Record{
		TaskID:    taskID,
		TaskType:  task.Type(),
		Payload:   task.Payload(),
		Err:       taskErr,
		StartedAt: startedAt,
		Env: map[string]string{
			"kbplacerVersion": os.Getenv("KBPLACER_VERSION"),
		},
	}
	if workDir != "" {
		rec.LogDir = filepath.Join(workDir, "logs")
	}
	path, err := w.failedTasks.Save(rec)
	if err != nil {
		log.Printf("[Task %s] Failed to save failed task: %v", taskID, err)
	} else if path != "" {
		log.Printf("[Task %s] Failed task saved to %s", taskID, path)
	}
}

// reportProgress writes a progress message to the task's result writer
func (w *Worker) reportProgress(task *asynq.Task, message string) error {
	progress := common.Progress{
		Message: message,
	}

	progressJSON, err := json.Marshal(progress)
	if err != nil {
		return fmt.Errorf("failed to marshal progress: %w", err)
	}

	// Write progress to result (stored in Redis)
	_, err = task.ResultWriter().Write(progressJSON)
	return err
}

// reportResult writes the final successful result, including the manifest of
// generated files, to the task's result writer. This is the last write of the
// task, so the file list is what the server returns for a SUCCESS status.
func (w *Worker) reportResult(task *asynq.Task, files *common.ProjectFiles) error {
	progress := common.Progress{
		Message: "Task completed successfully",
		Files:   files,
	}

	progressJSON, err := json.Marshal(progress)
	if err != nil {
		return fmt.Errorf("failed to marshal result: %w", err)
	}

	_, err = task.ResultWriter().Write(progressJSON)
	return err
}
