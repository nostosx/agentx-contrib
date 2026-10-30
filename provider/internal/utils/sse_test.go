package utils_test

import (
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/nostosx/agentx-contrib/provider/internal/utils"
)

func TestReadSSELimit(t *testing.T) {
	t.Parallel()
	input := "data: " + strings.Repeat("x", 100) + "\n\n"
	for _, tc := range []struct {
		name  string
		limit int
		fail  bool
	}{
		{name: "within limit", limit: 128},
		{name: "over limit", limit: 32, fail: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var gotErr error
			var count int
			for _, err := range utils.ReadSSELimit(strings.NewReader(input), tc.limit) {
				if err != nil {
					gotErr = err
				} else {
					count++
				}
			}
			if (gotErr != nil) != tc.fail || !tc.fail && count != 1 || tc.fail && count != 0 {
				t.Fatalf("events=%d error=%v", count, gotErr)
			}
		})
	}
}

func TestReadSSE(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, sep string }{{name: "LF", sep: "\n"}, {name: "CRLF", sep: "\r\n"}, {name: "CR", sep: "\r"}} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			input := strings.Join([]string{": comment", "retry: 10", "event: delta", "id: 42", "data: one", "data:  two", "", "data:", "", "event: ping", "", "event: done", "data: final", "", "event: cut", "data: incomplete"}, tc.sep)
			var got []utils.SSEEvent
			for ev, err := range utils.ReadSSE(strings.NewReader(input)) {
				if err != nil {
					t.Fatal(err)
				}
				got = append(got, *ev)
			}
			// The data-less ping is not dispatched, and the event cut off by
			// the end of the body is discarded.
			want := []utils.SSEEvent{{Event: "delta", ID: "42", Data: "one\n two"}, {Data: ""}, {Event: "done", Data: "final"}}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("events = %#v", got)
			}
		})
	}
}

type failingReader struct{ err error }

func (r failingReader) Read([]byte) (int, error) { return 0, r.err }

func TestReadSSEErrorAndEarlyStop(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("read failed")
	for _, tc := range []struct {
		name   string
		reader io.Reader
		want   error
	}{
		{name: "reader error", reader: failingReader{sentinel}, want: sentinel},
		{name: "line limit", reader: strings.NewReader("data: " + strings.Repeat("x", 17<<20))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got error
			for _, err := range utils.ReadSSE(tc.reader) {
				got = err
			}
			if got == nil || (tc.want != nil && !errors.Is(got, tc.want)) {
				t.Fatalf("read error = %v", got)
			}
		})
	}
	count := 0
	for _, err := range utils.ReadSSE(strings.NewReader("data: first\n\ndata: second\n\n")) {
		if err != nil {
			t.Fatal(err)
		}
		count++
		break
	}
	if count != 1 {
		t.Fatalf("early stop yielded %d", count)
	}
}
