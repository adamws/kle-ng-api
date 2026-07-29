package logstream

import (
	"reflect"
	"testing"
)

func TestStreamKey(t *testing.T) {
	if got := StreamKey("abc123"); got != "pcb:logs:abc123" {
		t.Fatalf("StreamKey = %q, want pcb:logs:abc123", got)
	}
}

// collect returns a LineWriter that appends each emitted line to *out.
func collect(out *[]string) *LineWriter {
	return newLineWriter(func(line string) { *out = append(*out, line) })
}

func TestLineWriter_SplitsOnNewline(t *testing.T) {
	var got []string
	w := collect(&got)

	if _, err := w.Write([]byte("first\nsecond\nthird\n")); err != nil {
		t.Fatalf("Write error: %v", err)
	}

	want := []string{"first", "second", "third"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("lines = %#v, want %#v", got, want)
	}
}

func TestLineWriter_BuffersPartialChunks(t *testing.T) {
	var got []string
	w := collect(&got)

	// A single logical line delivered across several Write calls (as a pipe
	// would), with no trailing newline until the last chunk.
	w.Write([]byte("Routing "))
	w.Write([]byte("SW1 "))
	w.Write([]byte("with D1\n"))

	if want := []string{"Routing SW1 with D1"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("lines = %#v, want %#v", got, want)
	}
}

func TestLineWriter_MultipleLinesInOneChunkPlusRemainder(t *testing.T) {
	var got []string
	w := collect(&got)

	// Two complete lines and a partial third arrive together; the partial is
	// held back until its newline shows up in a later write.
	w.Write([]byte("a\nb\nc-part"))
	if want := []string{"a", "b"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("after first write lines = %#v, want %#v", got, want)
	}

	w.Write([]byte("ial\n"))
	if want := []string{"a", "b", "c-partial"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("after second write lines = %#v, want %#v", got, want)
	}
}

func TestLineWriter_CloseFlushesUnterminatedLine(t *testing.T) {
	var got []string
	w := collect(&got)

	w.Write([]byte("no trailing newline"))
	if len(got) != 0 {
		t.Fatalf("expected nothing emitted before Close, got %#v", got)
	}

	if err := w.Close(); err != nil {
		t.Fatalf("Close error: %v", err)
	}
	if want := []string{"no trailing newline"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("after Close lines = %#v, want %#v", got, want)
	}
}

func TestLineWriter_CloseWithNoRemainderEmitsNothing(t *testing.T) {
	var got []string
	w := collect(&got)

	w.Write([]byte("done\n"))
	w.Close()

	if want := []string{"done"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("lines = %#v, want %#v", got, want)
	}
}

func TestLineWriter_TrimsCarriageReturn(t *testing.T) {
	var got []string
	w := collect(&got)

	w.Write([]byte("crlf line\r\nplain\n"))

	if want := []string{"crlf line", "plain"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("lines = %#v, want %#v", got, want)
	}
}

func TestLineWriter_WriteReportsFullLength(t *testing.T) {
	w := collect(&[]string{})
	chunk := []byte("partial-no-newline")
	n, err := w.Write(chunk)
	if err != nil || n != len(chunk) {
		t.Fatalf("Write = (%d, %v), want (%d, nil)", n, err, len(chunk))
	}
}
