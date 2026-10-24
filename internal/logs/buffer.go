package logs

import (
	"io"
	"strings"
	"sync"
	"time"
)

type Event struct {
	Sequence uint64    `json:"sequence"`
	Time     time.Time `json:"time"`
	Alias    string    `json:"alias"`
	Stream   string    `json:"stream"`
	Text     string    `json:"text"`
}

// Buffer is one bounded, session-wide log window. Disk capture is independent.
type Buffer struct {
	mu       sync.Mutex
	events   []Event
	bytes    int
	sequence uint64
}
type streamWriter struct {
	buffer        *Buffer
	alias, stream string
}

func (b *Buffer) Writer(alias, stream string) io.Writer { return streamWriter{b, alias, stream} }
func (w streamWriter) Write(p []byte) (int, error) {
	w.buffer.mu.Lock()
	defer w.buffer.mu.Unlock()
	b := w.buffer
	// Fixed-sized chunks keep memory bounded even without newline delimiters.
	for rest := p; len(rest) > 0; {
		n := min(len(rest), 8192)
		b.sequence++
		e := Event{b.sequence, time.Now(), w.alias, w.stream, string(rest[:n])}
		b.events = append(b.events, e)
		b.bytes += n
		rest = rest[n:]
	}
	for len(b.events) > 2048 || b.bytes > 2*1024*1024 {
		b.bytes -= len(b.events[0].Text)
		b.events[0] = Event{}
		b.events = b.events[1:]
	}
	return len(p), nil
}
func (b *Buffer) Read(alias, stream, search string, after uint64) []Event {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]Event, 0)
	for _, e := range b.events {
		if e.Sequence > after && (alias == "" || e.Alias == alias) && (stream == "" || e.Stream == stream) && (search == "" || strings.Contains(strings.ToLower(e.Text), strings.ToLower(search))) {
			out = append(out, e)
		}
	}
	return out
}
