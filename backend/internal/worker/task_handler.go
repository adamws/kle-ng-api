package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"runtime/debug"
	"time"

	"backend/internal/common"
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
func (w *Worker) HandleGenerateKicadProject(ctx context.Context, task *asynq.Task) error {
	// Extract task ID (asynq provides this via task metadata)
	taskID := task.ResultWriter().TaskID()

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

	// Update progress: Starting
	if err := w.reportProgress(task, 0, "Initializing task"); err != nil {
		log.Printf("[Task %s] Failed to report progress: %v", taskID, err)
	}

	// Defer panic recovery
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[Task %s] Panic recovered: %v", taskID, r)
			log.Printf("[Task %s] Stack trace: %s", taskID, debug.Stack())

			// Report error progress
			w.reportProgress(task, 0, fmt.Sprintf("Panic: %v", r))
		}
	}()

	// Parse task payload
	var taskRequest map[string]interface{}
	if err := json.Unmarshal(task.Payload(), &taskRequest); err != nil {
		log.Printf("[Task %s] Failed to parse request JSON: %v", taskID, err)
		w.reportProgress(task, 0, "Invalid JSON payload")
		return fmt.Errorf("failed to parse request JSON: %w", err)
	}

	// Update progress: 10% - Generating PCB
	if err := w.reportProgress(task, 10, "Generating KiCad PCB files"); err != nil {
		log.Printf("[Task %s] Failed to report progress: %v", taskID, err)
	}
	_ = pub.PublishLine(ctx, logstream.SourceWorker, "Generating KiCad PCB files")

	// Generate KiCad project (pass context for cancellation support)
	workDir, files, err := kicad.NewPCB(ctx, taskID, taskRequest, pub)
	if err != nil {
		log.Printf("[Task %s] PCB generation failed: %v", taskID, err)
		errMsg := fmt.Sprintf("PCB generation failed: %v", err)
		w.reportProgress(task, 0, errMsg)
		return errors.New(errMsg)
	}

	log.Printf("[Task %s] PCB generated successfully, work directory: %s", taskID, workDir)

	// Update progress: 50% - Uploading to S3
	if err := w.reportProgress(task, 50, "Uploading files to storage"); err != nil {
		log.Printf("[Task %s] Failed to report progress: %v", taskID, err)
	}
	_ = pub.PublishLine(ctx, logstream.SourceWorker, "Uploading files to storage")

	// Upload to Filer
	if err := w.filerUploader.UploadToStorage(ctx, taskID, workDir, files.Renders); err != nil {
		log.Printf("[Task %s] Error uploading to storage: %v", taskID, err)
		w.reportProgress(task, 50, fmt.Sprintf("Upload failed: %v", err))
		return fmt.Errorf("Filer upload failed: %w", err)
	}

	log.Printf("[Task %s] Files uploaded to Filer successfully", taskID)

	// Update progress: 100% - Complete. The final result also carries the file
	// manifest so the frontend learns which renders/artifacts were produced.
	if err := w.reportResult(task, files); err != nil {
		log.Printf("[Task %s] Failed to report final result: %v", taskID, err)
	}

	_ = pub.PublishLine(ctx, logstream.SourceWorker, "Task completed successfully")

	log.Printf("[Task %s] Task completed successfully", taskID)
	buildStatus = "success"
	return nil
}

// reportProgress writes progress updates to the task's result writer
func (w *Worker) reportProgress(task *asynq.Task, percentage int, message string) error {
	progress := common.Progress{
		Percentage: percentage,
		Message:    message,
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
		Percentage: 100,
		Message:    "Task completed successfully",
		Files:      files,
	}

	progressJSON, err := json.Marshal(progress)
	if err != nil {
		return fmt.Errorf("failed to marshal result: %w", err)
	}

	_, err = task.ResultWriter().Write(progressJSON)
	return err
}
