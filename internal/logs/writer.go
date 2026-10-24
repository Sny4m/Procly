package logs

import (
	"fmt"
	"io"
	"os"
	"sync"
)

// Writer bounds disk usage even for a single very large Write or unterminated line.
type Writer struct {
	mu          sync.Mutex
	path        string
	limit, size int64
	backups     int
	file        *os.File
	err         error
}

func New(path string, limit int64, backups int) (*Writer, error) {
	if limit <= 0 || backups < 0 {
		return nil, fmt.Errorf("invalid log retention")
	}
	f, e := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		return nil, e
	}
	return &Writer{path: path, limit: limit, backups: backups, file: f}, nil
}
func (w *Writer) rotate() error {
	if e := w.file.Close(); e != nil {
		return e
	}
	if w.backups > 0 {
		if e := os.Remove(fmt.Sprintf("%s.%d", w.path, w.backups)); e != nil && !os.IsNotExist(e) {
			return e
		}
		for i := w.backups - 1; i >= 1; i-- {
			if e := os.Rename(fmt.Sprintf("%s.%d", w.path, i), fmt.Sprintf("%s.%d", w.path, i+1)); e != nil && !os.IsNotExist(e) {
				return e
			}
		}
		if e := os.Rename(w.path, w.path+".1"); e != nil {
			return e
		}
	}
	f, e := os.OpenFile(w.path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if e != nil {
		return e
	}
	w.file = f
	w.size = 0
	return nil
}
func (w *Writer) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.err != nil {
		return 0, w.err
	}
	total := 0
	for len(p) > 0 {
		if w.size == w.limit {
			if e := w.rotate(); e != nil {
				w.err = e
				return total, e
			}
		}
		n := int64(len(p))
		if n > w.limit-w.size {
			n = w.limit - w.size
		}
		written, e := w.file.Write(p[:n])
		w.size += int64(written)
		total += written
		p = p[written:]
		if e != nil {
			w.err = e
			return total, e
		}
		if written == 0 {
			w.err = io.ErrShortWrite
			return total, w.err
		}
	}
	return total, nil
}
func (w *Writer) Close() error { w.mu.Lock(); defer w.mu.Unlock(); return w.file.Close() }

// Read streams retained segments oldest first. Missing rotated segments are normal.
func Read(out io.Writer, path string, backups int) error {
	for i := backups; i >= 0; i-- {
		p := path
		if i > 0 {
			p = fmt.Sprintf("%s.%d", path, i)
		}
		f, e := os.Open(p)
		if os.IsNotExist(e) && i > 0 {
			continue
		}
		if e != nil {
			return e
		}
		_, e = io.Copy(out, f)
		f.Close()
		if e != nil {
			return e
		}
	}
	return nil
}
