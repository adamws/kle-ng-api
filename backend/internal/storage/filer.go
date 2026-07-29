package storage

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"backend/internal/common"
	"backend/internal/retry"
)

// Upload retry tuning. Uploads are the one genuinely transient step in PCB
// generation; tasks themselves no longer auto-retry, so each network POST is
// retried here on transport errors and 5xx/429 responses. All attempts run under
// the task context, so asynq's 10-minute task timeout still bounds the total.
const (
	uploadAttempts  = 3
	uploadBaseDelay = 1 * time.Second
)

// FilerUploader handles uploading files to SeaweedFS Filer
type FilerUploader struct {
	httpClient *http.Client
	filerURL   string
	ttl        string
}

// NewFilerUploader creates a new FilerUploader
func NewFilerUploader(filerURL string) *FilerUploader {
	return &FilerUploader{
		httpClient: &http.Client{
			Timeout: 5 * time.Minute, // 5-minute timeout for large file uploads
		},
		filerURL: filerURL,
		ttl:      "1h", // 1-hour TTL for uploaded files
	}
}

// retryableUploadError marks errors worth retrying: any transport-level error
// from httpClient.Do (wrapped by sendUpload), and 5xx/429 server responses.
// Permanent failures (4xx, or the multipart-assembly errors below) return false
// so the upload fails fast.
type retryableUploadError struct{ err error }

func (e *retryableUploadError) Error() string { return e.err.Error() }
func (e *retryableUploadError) Unwrap() error { return e.err }

func isRetryableUpload(err error) bool {
	var re *retryableUploadError
	return errors.As(err, &re)
}

// uploadFile uploads a file to SeaweedFS Filer using multipart/form-data. The
// multipart body is assembled once (consuming data), then the HTTP POST is
// retried with backoff on transient failures.
func (u *FilerUploader) uploadFile(ctx context.Context, path string, data io.Reader, contentType string) error {
	// Assemble the multipart body once — data is a one-shot reader, so we must
	// not re-copy it per attempt. Only the HTTP send below is retried.
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)

	part, err := writer.CreateFormFile("file", filepath.Base(path))
	if err != nil {
		return fmt.Errorf("failed to create form file: %w", err)
	}

	if _, err := io.Copy(part, data); err != nil {
		return fmt.Errorf("failed to copy data: %w", err)
	}

	writer.Close()

	bodyBytes := body.Bytes()
	url := fmt.Sprintf("%s/%s?ttl=%s", u.filerURL, path, u.ttl)
	formContentType := writer.FormDataContentType()

	return retry.Do(ctx, uploadAttempts, uploadBaseDelay, isRetryableUpload, func() error {
		return u.sendUpload(ctx, url, bodyBytes, formContentType)
	})
}

// sendUpload performs a single POST of the pre-assembled multipart body. It
// wraps transient failures in *retryableUploadError so retry.Do knows to retry;
// non-retryable failures (4xx) are returned bare.
func (u *FilerUploader) sendUpload(ctx context.Context, url string, bodyBytes []byte, formContentType string) error {
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(bodyBytes))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", formContentType)

	resp, err := u.httpClient.Do(req)
	if err != nil {
		// Transport errors (network, timeout) are transient.
		return &retryableUploadError{fmt.Errorf("upload failed: %w", err)}
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		respBody, _ := io.ReadAll(resp.Body)
		statusErr := fmt.Errorf("upload failed with status %d: %s", resp.StatusCode, string(respBody))
		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
			return &retryableUploadError{statusErr}
		}
		return statusErr
	}

	return nil
}

// UploadZip uploads a ZIP file from a memory buffer
func (u *FilerUploader) UploadZip(ctx context.Context, taskID string, zipBuffer *bytes.Buffer) error {
	path := fmt.Sprintf("%s/%s.zip", taskID, taskID)
	return u.uploadFile(ctx, path, zipBuffer, "application/zip")
}

// UploadSVG uploads an SVG file with proper content type
func (u *FilerUploader) UploadSVG(ctx context.Context, taskID, name, filePath string) error {
	// Read file contents
	fileData, err := os.ReadFile(filePath)
	if err != nil {
		return fmt.Errorf("failed to read SVG file: %w", err)
	}

	path := fmt.Sprintf("%s/%s.svg", taskID, name)
	return u.uploadFile(ctx, path, bytes.NewReader(fileData), "image/svg+xml")
}

// UploadToStorage uploads all project files to Filer: the result zip plus every
// SVG render in the manifest. Each render was written to the logs directory as
// "<name>.svg" by the generator, and is exposed at {taskID}/{name}.svg so the
// /render/{name} endpoint can serve it.
func (u *FilerUploader) UploadToStorage(ctx context.Context, taskID, workDir string, renders []common.RenderFile) error {
	// Create ZIP archive in memory
	zipBuffer, err := CreateZipInMemory(workDir)
	if err != nil {
		return fmt.Errorf("failed to create ZIP: %w", err)
	}

	// Upload ZIP file
	if err := u.UploadZip(ctx, taskID, zipBuffer); err != nil {
		return err
	}

	// Upload SVG renders (PCB front/back and one per schematic sheet)
	logPath := filepath.Join(workDir, "logs")
	for _, render := range renders {
		svgPath := filepath.Join(logPath, render.Name+".svg")
		if err := u.UploadSVG(ctx, taskID, render.Name, svgPath); err != nil {
			return err
		}
	}

	return nil
}
