package bedrock

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"hash/crc32"
	"io"
	"strings"
	"testing"
	"time"
)

// header is a test header with a raw type and value.
type header struct {
	name  string
	typ   byte
	value []byte
}

func stringHeader(name, value string) header {
	v := binary.BigEndian.AppendUint16(nil, uint16(len(value)))
	return header{name, typeString, append(v, value...)}
}

// frame encodes an event stream message.
func frame(payload []byte, headers ...header) []byte {
	var h []byte
	for _, hd := range headers {
		h = append(h, byte(len(hd.name)))
		h = append(h, hd.name...)
		h = append(h, hd.typ)
		h = append(h, hd.value...)
	}
	total := minMessageLen + len(h) + len(payload)
	b := binary.BigEndian.AppendUint32(nil, uint32(total))
	b = binary.BigEndian.AppendUint32(b, uint32(len(h)))
	b = binary.BigEndian.AppendUint32(b, crc32.ChecksumIEEE(b))
	b = append(b, h...)
	b = append(b, payload...)
	return binary.BigEndian.AppendUint32(b, crc32.ChecksumIEEE(b))
}

// event encodes a ConverseStream event with a JSON payload.
func event(eventType, payload string) []byte {
	return frame([]byte(payload),
		stringHeader(":event-type", eventType),
		stringHeader(":content-type", "application/json"),
		stringHeader(":message-type", "event"))
}

func TestEventReaderPublishedFrame(t *testing.T) {
	t.Parallel()
	// A signed empty frame from
	// https://docs.aws.amazon.com/transcribe/latest/dg/streaming-setting-up.html,
	// whose :date the page gives as 2019-01-29T01:56:17.291Z.
	raw, err := base64.StdEncoding.DecodeString("AAAAUwAAAEP1RHpYBTpkYXRlCAAAAWiXUkMLEDpjaHVuay1zaWduYXR1cmUGACCt6Zy+uymwEK2SrLp/zVBI5eGn83jdBwCaRUBJA+eaDafqjqI=")
	if err != nil {
		t.Fatal(err)
	}
	er := &eventReader{r: bytes.NewReader(raw)}
	msg, err := er.next()
	if err != nil {
		t.Fatal(err)
	}
	date, _ := msg.headers[":date"].(time.Time)
	sig, _ := msg.headers[":chunk-signature"].([]byte)
	if !date.Equal(time.Date(2019, 1, 29, 1, 56, 17, 291e6, time.UTC)) || hex.EncodeToString(sig) != "ade99cbebb29b010ad92acba7fcd5048e5e1a7f378dd07009a45404903e79a0d" || len(msg.payload) != 0 {
		t.Fatalf("message = %v %x", msg.headers, msg.payload)
	}
	if _, err := er.next(); !errors.Is(err, io.EOF) {
		t.Fatalf("end = %v", err)
	}
}

func TestEventReaderHeaderTypes(t *testing.T) {
	t.Parallel()
	uuid := bytes.Repeat([]byte{0xab}, 16)
	raw := frame([]byte(`{"x":1}`),
		header{"t", typeTrue, nil},
		header{"f", typeFalse, nil},
		header{"b", typeByte, []byte{0xff}},
		header{"s", typeShort, []byte{0x80, 0x00}},
		header{"i", typeInt, []byte{0, 0, 1, 0}},
		header{"l", typeLong, []byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xfe}},
		header{"bytes", typeBytes, []byte{0, 2, 1, 2}},
		stringHeader("str", "héllo"),
		header{"ts", typeTimestamp, binary.BigEndian.AppendUint64(nil, 1000)},
		header{"u", typeUUID, uuid},
	)
	msg, err := (&eventReader{r: bytes.NewReader(append(raw, raw...))}).next()
	if err != nil {
		t.Fatal(err)
	}
	h := msg.headers
	ts, _ := h["ts"].(time.Time)
	u, _ := h["u"].([16]byte)
	if h["t"] != true || h["f"] != false || h["b"] != int8(-1) || h["s"] != int16(-32768) || h["i"] != int32(256) ||
		h["l"] != int64(-2) || !bytes.Equal(h["bytes"].([]byte), []byte{1, 2}) || msg.header("str") != "héllo" ||
		!ts.Equal(time.Unix(1, 0)) || !bytes.Equal(u[:], uuid) || string(msg.payload) != `{"x":1}` {
		t.Fatalf("headers = %#v, payload %s", h, msg.payload)
	}
}

func TestEventReaderRejectsCorruptFrames(t *testing.T) {
	t.Parallel()
	good := event("messageStart", `{"role":"assistant"}`)
	corrupt := func(i int) []byte {
		b := bytes.Clone(good)
		b[i] ^= 0x01
		return b
	}
	withLengths := func(total, headers uint32) []byte {
		b := binary.BigEndian.AppendUint32(nil, total)
		b = binary.BigEndian.AppendUint32(b, headers)
		b = binary.BigEndian.AppendUint32(b, crc32.ChecksumIEEE(b))
		return append(b, make([]byte, 64)...)
	}
	for _, tc := range []struct {
		name  string
		input []byte
		want  error // nil for any non-EOF error
	}{
		{name: "prelude length", input: corrupt(3), want: errChecksum},
		{name: "prelude crc", input: corrupt(9), want: errChecksum},
		{name: "header byte", input: corrupt(14), want: errChecksum},
		{name: "payload byte", input: corrupt(len(good) - 6), want: errChecksum},
		{name: "message crc", input: corrupt(len(good) - 1), want: errChecksum},
		{name: "truncated prelude", input: good[:7], want: io.ErrUnexpectedEOF},
		{name: "truncated message", input: good[:len(good)-1], want: io.ErrUnexpectedEOF},
		{name: "too short", input: withLengths(15, 0)},
		{name: "headers beyond message", input: withLengths(40, 30)},
		{name: "oversized headers", input: withLengths(maxHeadersLen+minMessageLen+1, maxHeadersLen+1)},
		{name: "oversized payload", input: withLengths(minMessageLen+maxPayloadLen+1, 0)},
		{name: "truncated header", input: frame(nil, header{"name", typeString, []byte{0, 9, 'a'}})},
		{name: "unknown header type", input: frame(nil, header{"name", 42, nil})},
		{name: "empty header name", input: frame(nil, header{"", typeTrue, nil})},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := (&eventReader{r: bytes.NewReader(tc.input)}).next()
			if err == nil || errors.Is(err, io.EOF) || (tc.want != nil && !errors.Is(err, tc.want)) {
				t.Fatalf("error = %v, want %v", err, tc.want)
			}
		})
	}
	if _, err := (&eventReader{r: strings.NewReader("")}).next(); !errors.Is(err, io.EOF) {
		t.Fatalf("empty stream = %v", err)
	}
}
