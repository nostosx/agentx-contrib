package bedrock

import (
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"time"
)

// The AWS event stream encoding (application/vnd.amazon.eventstream) of
// ConverseStream responses, as specified at
// https://smithy.io/2.0/aws/amazon-eventstream.html and
// https://docs.aws.amazon.com/transcribe/latest/dg/streaming-setting-up.html#streaming-event-stream.
//
// A message is a prelude (total length and headers length, both big-endian
// uint32), a CRC32 of the prelude, the headers, the payload and a CRC32 of
// everything before it. Both checksums use the IEEE polynomial.

const (
	preludeLen = 12 // two lengths and the prelude CRC
	// minMessageLen is a message without headers or payload: the prelude
	// and the message CRC.
	minMessageLen = preludeLen + 4
	// maxHeadersLen and maxPayloadLen are the limits the specification
	// sets for services; a message beyond them is rejected rather than
	// allocated.
	maxHeadersLen = 128 << 10
	maxPayloadLen = 24 << 20
)

// errChecksum reports a corrupted event stream message.
var errChecksum = errors.New("event stream checksum mismatch")

// eventMessage is one decoded event stream message.
type eventMessage struct {
	headers map[string]any
	payload []byte
}

// header returns the string value of header name, or "".
func (m *eventMessage) header(name string) string {
	s, _ := m.headers[name].(string)
	return s
}

// eventReader decodes event stream messages from r.
type eventReader struct {
	r       io.Reader
	prelude [preludeLen]byte
}

// next returns the next message. It returns io.EOF at the end of the
// stream between messages and io.ErrUnexpectedEOF inside a message.
func (er *eventReader) next() (*eventMessage, error) {
	if _, err := io.ReadFull(er.r, er.prelude[:]); err != nil {
		return nil, err
	}
	total := binary.BigEndian.Uint32(er.prelude[0:4])
	headersLen := binary.BigEndian.Uint32(er.prelude[4:8])
	if crc32.ChecksumIEEE(er.prelude[:8]) != binary.BigEndian.Uint32(er.prelude[8:12]) {
		return nil, fmt.Errorf("prelude: %w", errChecksum)
	}
	if total < minMessageLen || headersLen > maxHeadersLen || total-minMessageLen < headersLen ||
		total-minMessageLen-headersLen > maxPayloadLen {
		return nil, fmt.Errorf("invalid event stream message lengths %d and %d", total, headersLen)
	}
	buf := make([]byte, total)
	copy(buf, er.prelude[:])
	if _, err := io.ReadFull(er.r, buf[preludeLen:]); err != nil {
		if errors.Is(err, io.EOF) {
			err = io.ErrUnexpectedEOF
		}
		return nil, err
	}
	end := total - 4
	if crc32.ChecksumIEEE(buf[:end]) != binary.BigEndian.Uint32(buf[end:]) {
		return nil, fmt.Errorf("message: %w", errChecksum)
	}
	headers, err := decodeHeaders(buf[preludeLen : preludeLen+headersLen])
	if err != nil {
		return nil, err
	}
	return &eventMessage{headers: headers, payload: buf[preludeLen+headersLen : end]}, nil
}

// Header value types.
const (
	typeTrue = iota
	typeFalse
	typeByte
	typeShort
	typeInt
	typeLong
	typeBytes
	typeString
	typeTimestamp
	typeUUID
)

// decodeHeaders decodes the headers section of a message. Values are bool,
// int8, int16, int32, int64, []byte, string, time.Time or [16]byte.
func decodeHeaders(b []byte) (map[string]any, error) {
	headers := map[string]any{}
	errShort := errors.New("truncated event stream header")
	for len(b) > 0 {
		n := int(b[0])
		if n == 0 || len(b) < 1+n+1 {
			return nil, errShort
		}
		name := string(b[1 : 1+n])
		typ := b[1+n]
		b = b[2+n:]
		var v any
		size := 0
		switch typ {
		case typeTrue:
			v = true
		case typeFalse:
			v = false
		case typeByte:
			size = 1
		case typeShort:
			size = 2
		case typeInt:
			size = 4
		case typeLong, typeTimestamp:
			size = 8
		case typeUUID:
			size = 16
		case typeBytes, typeString:
			if len(b) < 2 {
				return nil, errShort
			}
			l := int(binary.BigEndian.Uint16(b))
			if len(b) < 2+l {
				return nil, errShort
			}
			raw := b[2 : 2+l]
			if typ == typeString {
				v = string(raw)
			} else {
				v = append([]byte(nil), raw...)
			}
			b = b[2+l:]
		default:
			return nil, fmt.Errorf("unknown event stream header type %d", typ)
		}
		if size > 0 {
			if len(b) < size {
				return nil, errShort
			}
			switch typ {
			case typeByte:
				v = int8(b[0])
			case typeShort:
				v = int16(binary.BigEndian.Uint16(b))
			case typeInt:
				v = int32(binary.BigEndian.Uint32(b))
			case typeLong:
				v = int64(binary.BigEndian.Uint64(b))
			case typeTimestamp:
				v = time.UnixMilli(int64(binary.BigEndian.Uint64(b))).UTC()
			case typeUUID:
				var u [16]byte
				copy(u[:], b)
				v = u
			}
			b = b[size:]
		}
		headers[name] = v
	}
	return headers, nil
}
