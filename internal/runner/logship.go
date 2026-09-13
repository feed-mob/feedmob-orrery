package runner

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/feed-mob/feedmob-orrery/internal/protocol"
)

// logShipper buffers log lines and ships them to the server.
//
// Delivery is defined by the server's ack_index, not by the HTTP status of the
// submission. We keep everything past the ack and resend it; a reply that gets
// lost on the way back costs a duplicate submission, never a missing line.
type logShipper struct {
	cl       *Client
	taskID   int64
	interval time.Duration
	log      *slog.Logger

	mu      sync.Mutex
	buf     []protocol.LogRow
	base    int64 // absolute index of buf[0]
	acked   int64
	pending bool
	last    time.Time
}

func newLogShipper(cl *Client, taskID int64, interval time.Duration, log *slog.Logger) *logShipper {
	return &logShipper{cl: cl, taskID: taskID, interval: interval, log: log, last: time.Now()}
}

// write appends a line. Shipping happens on flush or when the buffer grows
// past a threshold, so a chatty step does not turn into one HTTP call per line.
func (s *logShipper) write(line string) {
	s.mu.Lock()
	s.buf = append(s.buf, protocol.LogRow{Time: time.Now().UTC(), Content: line})
	shouldShip := len(s.buf) >= 200 || time.Since(s.last) > s.interval
	s.mu.Unlock()
	if shouldShip {
		s.flush(context.Background())
	}
}

// nextIndex is the absolute index the next line will take. Steps record this
// before and after running so the server knows which slice of the stream
// belongs to which step.
func (s *logShipper) nextIndex() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.base + int64(len(s.buf))
}

// buffered returns the lines waiting to be shipped. Test-only: it is how a
// unit test sees what the shipper would send without standing up a server.
func (s *logShipper) buffered() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.buf))
	for _, r := range s.buf {
		out = append(out, r.Content)
	}
	return out
}

// flush ships everything past the last ack.
func (s *logShipper) flush(ctx context.Context) {
	s.mu.Lock()
	if s.pending || len(s.buf) == 0 {
		s.mu.Unlock()
		return
	}
	s.pending = true
	base, rows := s.base, append([]protocol.LogRow(nil), s.buf...)
	s.mu.Unlock()

	res, err := s.cl.UpdateLog(ctx, &protocol.UpdateLogRequest{
		TaskID: s.taskID, Index: base, Rows: rows,
	})

	s.mu.Lock()
	defer s.mu.Unlock()
	s.pending = false
	s.last = time.Now()
	if err != nil {
		// Keep the buffer; the next flush resends from the same base.
		s.log.Debug("log ship failed, will retry", "task", s.taskID, "err", err)
		return
	}
	s.acked = res.AckIndex
	if drop := int(res.AckIndex - s.base); drop > 0 {
		if drop >= len(s.buf) {
			s.buf = s.buf[:0]
		} else {
			s.buf = append([]protocol.LogRow(nil), s.buf[drop:]...)
		}
		s.base = res.AckIndex
	}
}

// close ships whatever is left and tells the server no more is coming.
func (s *logShipper) close(ctx context.Context) {
	s.flush(ctx)
	s.mu.Lock()
	base := s.base
	s.mu.Unlock()
	if _, err := s.cl.UpdateLog(ctx, &protocol.UpdateLogRequest{
		TaskID: s.taskID, Index: base, NoMore: true,
	}); err != nil {
		s.log.Debug("log close failed", "task", s.taskID, "err", err)
	}
}
