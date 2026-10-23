package utils

import (
	"bufio"
	"bytes"
	"io"
	"iter"
	"strings"
)

// SSEEvent is one server-sent event.
type SSEEvent struct {
	Event string
	Data  string
	ID    string
}

// maxSSELine bounds a single SSE line. Tool arguments and images can make
// lines large.
const maxSSELine = 16 << 20

// ReadSSE parses a text/event-stream body. Data lines of an event are
// joined with newlines; comments and retry fields are ignored. As the SSE
// specification requires, an event without data lines is not dispatched,
// and an event that the body ends before completing is discarded.
func ReadSSE(r io.Reader) iter.Seq2[*SSEEvent, error] {
	return func(yield func(*SSEEvent, error) bool) {
		sc := bufio.NewScanner(r)
		sc.Buffer(make([]byte, 0, 64<<10), maxSSELine)
		sc.Split(scanLines)
		var ev SSEEvent
		var data []string
		hasData := false
		dispatch := func() bool {
			out := ev
			out.Data = strings.Join(data, "\n")
			send := hasData
			ev, data, hasData = SSEEvent{}, data[:0], false
			return !send || yield(&out, nil)
		}
		for sc.Scan() {
			line := sc.Text()
			if line == "" {
				if !dispatch() {
					return
				}
				continue
			}
			if line[0] == ':' {
				continue
			}
			field, value, _ := strings.Cut(line, ":")
			value = strings.TrimPrefix(value, " ")
			switch field {
			case "event":
				ev.Event = value
			case "data":
				data = append(data, value)
				hasData = true
			case "id":
				ev.ID = value
			}
		}
		if err := sc.Err(); err != nil {
			yield(nil, err)
		}
	}
}

// scanLines splits on \n, \r\n or a lone \r, as the SSE format requires.
func scanLines(data []byte, atEOF bool) (int, []byte, error) {
	if atEOF && len(data) == 0 {
		return 0, nil, nil
	}
	if i := bytes.IndexAny(data, "\r\n"); i >= 0 {
		if data[i] == '\r' {
			if i+1 < len(data) {
				if data[i+1] == '\n' {
					return i + 2, data[:i], nil
				}
				return i + 1, data[:i], nil
			}
			if !atEOF {
				// Need more data to know whether \n follows.
				return 0, nil, nil
			}
		}
		return i + 1, data[:i], nil
	}
	if atEOF {
		return len(data), data, nil
	}
	return 0, nil, nil
}
