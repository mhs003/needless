package worker

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"

	"reflect"
	"strings"
	"testing"
)

func TestFrameRoundTrip(t *testing.T) {
	cases := []Request{
		{Op: "complete", Text: "dim the living room to 30", MaxNew: 256},
		{Op: "embed", Text: "hello world", Dim: 8},
		{Op: "reset"},
		{Op: "close"},
		{Op: "complete", Text: ""}, // empty text
		{Op: "complete", Text: "héllo 世界 😀"},              // multi-byte UTF-8
		{Op: "complete", Text: strings.Repeat("a", 4096)}, // long
		{Op: "complete", Text: "line1\nline2\ttab\r\n"},   // control chars
		{Op: "complete", Text: `{"nested":"\"quoted\""}`}, // JSON-ish text
	}
	for _, want := range cases {
		var buf bytes.Buffer
		if err := writeFrame(&buf, want); err != nil {
			t.Fatalf("writeFrame(%+v): %v", want, err)
		}
		var got Request
		if err := readFrame(&buf, &got); err != nil {
			t.Fatalf("readFrame(%+v): %v", want, err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("round trip mismatch:\n got %+v\nwant %+v", got, want)
		}
		if buf.Len() != 0 {
			t.Errorf("readFrame left %d unread bytes", buf.Len())
		}
	}
}

func TestFrameRoundTripResponseWithEmbedding(t *testing.T) {
	want := Response{Status: "ok", Embedding: []float32{1, 2.5, -3, 0}}
	var buf bytes.Buffer
	if err := writeFrame(&buf, want); err != nil {
		t.Fatal(err)
	}
	var got Response
	if err := readFrame(&buf, &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v want %+v", got, want)
	}
}

func TestWriteFrameRejectsUnmarshalable(t *testing.T) {
	var buf bytes.Buffer
	err := writeFrame(&buf, make(chan int))
	if err == nil {
		t.Fatal("expected an error marshalling a channel, got nil")
	}
}

func TestReadFrameMultipleBackToBack(t *testing.T) {
	var buf bytes.Buffer
	inputs := []Request{{Op: "a"}, {Op: "b"}, {Op: "c"}}
	for _, in := range inputs {
		if err := writeFrame(&buf, in); err != nil {
			t.Fatal(err)
		}
	}
	for i, want := range inputs {
		var got Request
		if err := readFrame(&buf, &got); err != nil {
			t.Fatalf("frame %d: %v", i, err)
		}
		if got.Op != want.Op {
			t.Errorf("frame %d: got %q want %q", i, got.Op, want.Op)
		}
	}
	// Stream is exhausted.
	var extra Request
	if err := readFrame(&buf, &extra); !errors.Is(err, io.EOF) {
		t.Fatalf("expected io.EOF after last frame, got %v", err)
	}
}

func TestReadFrameEmptyStreamIsEOF(t *testing.T) {
	var got Request
	err := readFrame(bytes.NewReader(nil), &got)
	if !errors.Is(err, io.EOF) {
		t.Fatalf("expected io.EOF, got %v", err)
	}
}

func TestReadFrameTruncatedHeader(t *testing.T) {
	var got Request
	err := readFrame(bytes.NewReader([]byte{0, 0, 0}), &got)
	if err == nil {
		t.Fatal("expected an error for a partial header")
	}
	if errors.Is(err, io.EOF) {
		t.Fatalf("partial header must not look like a clean EOF, got %v", err)
	}
}

func TestReadFrameTruncatedPayload(t *testing.T) {
	var buf bytes.Buffer
	var hdr [8]byte
	binary.BigEndian.PutUint64(hdr[:], 100) // announce 100 bytes...
	buf.Write(hdr[:])
	buf.WriteString(`{"op":"x"}`) // ...but supply fewer
	var got Request
	err := readFrame(&buf, &got)
	if err == nil || !strings.Contains(err.Error(), "truncated") {
		t.Fatalf("expected a truncation error, got %v", err)
	}
}

func TestReadFrameRejectsOversizedLength(t *testing.T) {
	var buf bytes.Buffer
	var hdr [8]byte
	binary.BigEndian.PutUint64(hdr[:], ^uint64(0)) // absurd length
	buf.Write(hdr[:])
	var got Request
	err := readFrame(&buf, &got)
	if err == nil || !strings.Contains(err.Error(), "1 GiB") {
		t.Fatalf("expected an oversize guard error, got %v", err)
	}
}

func TestReadFrameTreatsZeroLengthAsEOF(t *testing.T) {
	var buf bytes.Buffer
	var hdr [8]byte
	binary.BigEndian.PutUint64(hdr[:], 0)
	buf.Write(hdr[:])
	var got Request
	if err := readFrame(&buf, &got); !errors.Is(err, io.EOF) {
		t.Fatalf("expected io.EOF for a zero-length frame, got %v", err)
	}
}

func TestReadFrameRejectsMalformedJSON(t *testing.T) {
	var buf bytes.Buffer
	payload := []byte(`{"op": not json}`)
	var hdr [8]byte
	binary.BigEndian.PutUint64(hdr[:], uint64(len(payload)))
	buf.Write(hdr[:])
	buf.Write(payload)
	var got Request
	if err := readFrame(&buf, &got); err == nil {
		t.Fatal("expected a JSON error, got nil")
	}
}

func TestFrameHeaderIsBigEndianUint64(t *testing.T) {
	// Pin the wire format: the Python worker uses struct.Struct("!Q"), a
	// big-endian unsigned 64-bit length prefix. A 300-byte payload must
	// produce the exact header bytes below; little-endian would differ.
	payload := append([]byte(`{"op":"x","text":"`), bytes.Repeat([]byte("z"), 280)...)
	payload = append(payload, '"', '}')
	if len(payload) != 300 {
		t.Fatalf("test setup: payload is %d bytes, want 300", len(payload))
	}
	var buf bytes.Buffer
	var hdr [8]byte
	binary.BigEndian.PutUint64(hdr[:], uint64(len(payload)))
	buf.Write(hdr[:])
	buf.Write(payload)

	got := buf.Bytes()[:8]
	want := []byte{0, 0, 0, 0, 0, 0, 0x01, 0x2C} // 300 = 0x012C
	if !bytes.Equal(got, want) {
		t.Fatalf("header = % x, want % x", got, want)
	}
	var req Request
	if err := readFrame(&buf, &req); err != nil {
		t.Fatalf("readFrame on a 300-byte payload: %v", err)
	}
}
