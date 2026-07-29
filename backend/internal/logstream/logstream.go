// Package logstream provides a thin contract, shared by the server and worker
// processes, for streaming a task's build logs through a Redis Stream.
//
// The worker publishes kbplacer output and its own step markers line-by-line to
// the stream keyed by task ID; the server tails that stream and forwards each
// entry to the browser over a WebSocket. A Redis Stream (rather than pub/sub) is
// used so a client connecting late or reconnecting can backfill every line from
// the start via XRANGE, then block-tail new lines via XREAD.
package logstream

import (
	"bytes"
	"context"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

// Source names identify who produced a given log line. They travel in the
// stream entry's "source" field and are surfaced to the frontend.
const (
	// SourceKbplacer marks lines emitted by the `python3 -m kbplacer` subprocess.
	SourceKbplacer = "kbplacer"
	// SourceWorker marks the worker's own step markers (e.g. "Uploading files").
	SourceWorker = "worker"
)

// Stream tuning. These bound Redis resource use: MAXLEN caps entries per stream
// and streamTTL guarantees cleanup even if a task is abandoned. streamTTL is set
// slightly above the asynq task timeout (10 min) to cover the abandonment window.
const (
	maxLen    = 2000
	streamTTL = 15 * time.Minute
)

// StreamKey returns the Redis key holding the log stream for a task. Both the
// worker (writer) and server (reader) derive the key through this helper so they
// always agree.
func StreamKey(taskID string) string {
	return "pcb:logs:" + taskID
}

// Publisher writes a single task's log lines to its Redis Stream. Construct one
// per task with NewPublisher. It is safe for sequential use by the task handler
// and the writers it hands out; it is not designed for concurrent PublishLine
// calls across goroutines.
type Publisher struct {
	rdb       *redis.Client
	key       string
	expireSet bool
}

// NewPublisher builds a Publisher for the given task.
func NewPublisher(rdb *redis.Client, taskID string) *Publisher {
	return &Publisher{
		rdb: rdb,
		key: StreamKey(taskID),
	}
}

// PublishLine appends one log line to the stream. The first successful write
// also arms the stream's TTL so an abandoned or crashed task's logs still expire.
// Errors are returned but are non-fatal for the build: streaming is a best-effort
// side channel and must never break PCB generation.
func (p *Publisher) PublishLine(ctx context.Context, source, line string) error {
	err := p.rdb.XAdd(ctx, &redis.XAddArgs{
		Stream: p.key,
		MaxLen: maxLen,
		Approx: true, // "MAXLEN ~" — trims in whole nodes, cheaper than exact.
		Values: map[string]interface{}{
			"ts":     strconv.FormatInt(time.Now().Unix(), 10),
			"source": source,
			"line":   line,
		},
	}).Err()
	if err != nil {
		return err
	}
	p.ensureExpire(ctx)
	return nil
}

// PublishEnd appends the terminal entry that tells the server the build is over
// and lets it close the WebSocket cleanly. status is "success" or "failure".
func (p *Publisher) PublishEnd(ctx context.Context, status string) error {
	err := p.rdb.XAdd(ctx, &redis.XAddArgs{
		Stream: p.key,
		MaxLen: maxLen,
		Approx: true,
		Values: map[string]interface{}{
			"event":  "end",
			"status": status,
		},
	}).Err()
	if err != nil {
		return err
	}
	p.ensureExpire(ctx)
	return nil
}

// ensureExpire sets the stream TTL exactly once per Publisher.
func (p *Publisher) ensureExpire(ctx context.Context) {
	if p.expireSet {
		return
	}
	// Best effort; if it fails we'll retry on the next publish.
	if err := p.rdb.Expire(ctx, p.key, streamTTL).Err(); err == nil {
		p.expireSet = true
	}
}

// Writer returns an io.WriteCloser that splits incoming bytes on newlines and
// publishes one line per newline, tagging each with source. Partial lines are
// buffered across writes until their newline arrives, so it can be handed
// directly to exec.Cmd's Stdout/Stderr. Close (optional) flushes any trailing
// unterminated line.
//
// The writer captures ctx at creation; the caller must keep that context alive
// for the lifetime of the subprocess.
func (p *Publisher) Writer(ctx context.Context, source string) *LineWriter {
	return newLineWriter(func(line string) {
		_ = p.PublishLine(ctx, source, line)
	})
}

// LineWriter buffers partial reads and calls emit once per newline-terminated
// line. It is decoupled from Redis (via the emit callback) so the splitting
// logic can be unit-tested without a broker.
type LineWriter struct {
	emit func(line string)
	buf  bytes.Buffer
}

func newLineWriter(emit func(line string)) *LineWriter {
	return &LineWriter{emit: emit}
}

// Write always reports len(b) consumed (and a nil error) so it never aborts the
// subprocess pipe even if publishing to Redis fails — streaming is best effort.
func (w *LineWriter) Write(b []byte) (int, error) {
	w.buf.Write(b)
	for {
		data := w.buf.Bytes()
		i := bytes.IndexByte(data, '\n')
		if i < 0 {
			break
		}
		w.emit(string(trimCR(data[:i])))
		w.buf.Next(i + 1) // discard the line plus its '\n'
	}
	return len(b), nil
}

// Close flushes any buffered bytes that were never newline-terminated (e.g. a
// final prompt without a trailing newline).
func (w *LineWriter) Close() error {
	if w.buf.Len() > 0 {
		w.emit(string(trimCR(w.buf.Bytes())))
		w.buf.Reset()
	}
	return nil
}

// trimCR strips a single trailing carriage return so CRLF output renders cleanly.
func trimCR(b []byte) []byte {
	if n := len(b); n > 0 && b[n-1] == '\r' {
		return b[:n-1]
	}
	return b
}
