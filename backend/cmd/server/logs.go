package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"strconv"
	"time"

	"backend/internal/logstream"

	"github.com/gorilla/mux"
	"github.com/gorilla/websocket"
	"github.com/hibiken/asynq"
	"github.com/redis/go-redis/v9"
)

// WebSocket log-stream tuning.
const (
	// wsWriteWait bounds a single frame write.
	wsWriteWait = 10 * time.Second
	// wsPongWait is how long we wait for a pong before considering the client dead.
	wsPongWait = 60 * time.Second
	// wsPingPeriod must be less than wsPongWait; keeps the connection alive and
	// detects dead peers.
	wsPingPeriod = 30 * time.Second
	// wsMaxLifetime bounds the total socket lifetime, just over the asynq task
	// timeout (10 min) so a socket can't linger indefinitely.
	wsMaxLifetime = 11 * time.Minute
	// wsRedisBlock is how long each XREAD blocks waiting for new lines before we
	// loop (and re-check ctx). Short enough to notice cancellation promptly.
	wsRedisBlock = 5 * time.Second
	// wsReadCount caps entries returned per XREAD; also paces backfill.
	wsReadCount = 500
)

// wsLogFrame is a single log line forwarded to the browser.
type wsLogFrame struct {
	Ts     int64  `json:"ts,omitempty"`
	Source string `json:"source,omitempty"`
	Line   string `json:"line"`
}

// wsEndFrame is the terminal frame; after it the server closes the socket.
type wsEndFrame struct {
	Event  string `json:"event"`
	Status string `json:"status"`
}

// checkWSOrigin enforces the same origin policy as the REST CORS config: allow
// any origin in development, only the configured origin in production.
func (a *App) checkWSOrigin(r *http.Request) bool {
	if !a.production {
		return true
	}
	return r.Header.Get("Origin") == a.corsAllowedOrigin
}

// KicadGetTaskLogs upgrades to a WebSocket and streams a task's build log. It
// reads the Redis Stream from the beginning (backfill) and then block-tails new
// entries, forwarding each as a JSON text frame, until the terminal "end" entry,
// the client disconnects, or the lifetime bound elapses.
func (a *App) KicadGetTaskLogs(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	taskID := vars["task_id"]

	// An actively-watched task must not be reaped by the abandonment detector.
	a.taskAccessTracker.UpdateAccess(taskID)

	upgrader := websocket.Upgrader{
		ReadBufferSize:  1024,
		WriteBufferSize: 4096,
		CheckOrigin:     a.checkWSOrigin,
	}

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		// Upgrade already wrote an error response.
		log.Printf("[Logs %s] WebSocket upgrade failed: %v", taskID, err)
		return
	}
	defer conn.Close()

	// Bound the total lifetime of the connection.
	ctx, cancel := context.WithTimeout(r.Context(), wsMaxLifetime)
	defer cancel()

	// Read pump: we don't expect client messages, but reads are needed to
	// process control frames (pong/close) and to detect a dead/closed client.
	// Cancelling ctx on read error tears down the Redis pump and write loop.
	conn.SetReadLimit(512)
	conn.SetReadDeadline(time.Now().Add(wsPongWait))
	conn.SetPongHandler(func(string) error {
		conn.SetReadDeadline(time.Now().Add(wsPongWait))
		return nil
	})
	go func() {
		defer cancel()
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}()

	// Redis pump: a background goroutine reads the stream and hands pre-encoded
	// JSON frames to the write loop over a channel, so this (the sole writer)
	// can interleave pings without being blocked on XREAD.
	frames := make(chan []byte, 64)
	go a.pumpLogStream(ctx, taskID, frames)

	ticker := time.NewTicker(wsPingPeriod)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return

		case <-ticker.C:
			conn.SetWriteDeadline(time.Now().Add(wsWriteWait))
			if err := conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}

		case frame, ok := <-frames:
			if !ok {
				// Pump finished (end entry seen, key gone, or error). Ask the
				// client to close cleanly.
				conn.SetWriteDeadline(time.Now().Add(wsWriteWait))
				conn.WriteMessage(websocket.CloseMessage,
					websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
				return
			}
			conn.SetWriteDeadline(time.Now().Add(wsWriteWait))
			if err := conn.WriteMessage(websocket.TextMessage, frame); err != nil {
				return
			}
		}
	}
}

// pumpLogStream reads the task's Redis Stream from the start and block-tails new
// entries, sending each as a JSON-encoded frame on out. It closes out when it
// sees the terminal "end" entry, when ctx is cancelled, or on a non-recoverable
// Redis error. Reading from ID "0" yields the full backfill first, then blocks
// for new lines — one code path covers both backfill and live tail.
func (a *App) pumpLogStream(ctx context.Context, taskID string, out chan<- []byte) {
	defer close(out)

	key := logstream.StreamKey(taskID)
	lastID := "0"

	for {
		if ctx.Err() != nil {
			return
		}

		streams, err := a.redisClient.XRead(ctx, &redis.XReadArgs{
			Streams: []string{key, lastID},
			Count:   wsReadCount,
			Block:   wsRedisBlock,
		}).Result()
		if err != nil {
			if err == redis.Nil {
				// Block timeout with no new data (or the key doesn't exist yet).
				// If the task has already reached a terminal state (or no longer
				// exists) no further lines will ever arrive, so stop now instead
				// of blocking until the lifetime bound. The worker publishes its
				// terminal "end" entry before asynq marks the task done, so a
				// finished task whose stream is present would have been drained
				// (and returned) above rather than reaching this branch.
				if a.taskStreamFinished(taskID) {
					return
				}
				// Otherwise the task is still pending/active; keep waiting.
				continue
			}
			if ctx.Err() != nil {
				return
			}
			log.Printf("[Logs %s] XRead error: %v", taskID, err)
			return
		}

		for _, stream := range streams {
			for _, msg := range stream.Messages {
				lastID = msg.ID
				frame, isEnd := encodeFrame(msg.Values)
				if frame != nil {
					select {
					case out <- frame:
					case <-ctx.Done():
						return
					}
				}
				if isEnd {
					return
				}
			}
		}
	}
}

// encodeFrame converts a Redis Stream entry's fields into a JSON frame for the
// browser. It returns (frame, isEnd); isEnd is true for the terminal marker.
func encodeFrame(values map[string]interface{}) (frame []byte, isEnd bool) {
	if ev, _ := values["event"].(string); ev == "end" {
		status, _ := values["status"].(string)
		b, err := json.Marshal(wsEndFrame{Event: "end", Status: status})
		if err != nil {
			return nil, true
		}
		return b, true
	}

	line, _ := values["line"].(string)
	source, _ := values["source"].(string)
	var ts int64
	if s, ok := values["ts"].(string); ok {
		ts, _ = strconv.ParseInt(s, 10, 64)
	}

	b, err := json.Marshal(wsLogFrame{Ts: ts, Source: source, Line: line})
	if err != nil {
		return nil, false
	}
	return b, false
}

// taskStreamFinished reports whether a task will produce no further log lines:
// it no longer exists (unknown ID, or evicted after completion) or has reached a
// terminal state. The pump uses this to end a tail promptly when the stream key
// is absent, rather than blocking until the connection lifetime bound.
func (a *App) taskStreamFinished(taskID string) bool {
	info, err := a.asynqInspector.GetTaskInfo("kicad", taskID)
	if err != nil {
		return true
	}
	switch info.State {
	case asynq.TaskStateCompleted, asynq.TaskStateArchived:
		return true
	default:
		return false
	}
}
