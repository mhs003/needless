package worker

import (
	"bytes"
	"encoding/binary"
	"reflect"
	"testing"
)

// FuzzReadFrame drives the parent/child framing with arbitrary bytes.
//
// The two processes are always the same build of this program, so hostile input
// is unlikely — but a truncated or half-written stream is not, since that is
// exactly what a child dying mid-frame looks like. The reader already claims to
// tell "the peer shut down" from "the peer died mid-write"; this is what holds
// it to that.
func FuzzReadFrame(f *testing.F) {
	// Well-formed frames, produced by the writer itself.
	for _, req := range []Request{
		{Op: "complete", Text: "hello", MaxNew: 64},
		{Op: "embed", Text: "", Dim: 0},
		{Op: "reset"},
		{Op: "close"},
	} {
		var buf bytes.Buffer
		if err := writeFrame(&buf, req); err != nil {
			f.Fatalf("seeding: %v", err)
		}
		f.Add(buf.Bytes())
	}

	// And the shapes the reader explicitly guards: too short, zero length,
	// truncated payload, an absurd announced length, invalid JSON, trailing
	// bytes after a valid frame.
	f.Add([]byte{})
	f.Add([]byte{0, 0, 0})
	f.Add([]byte{0, 0, 0, 0, 0, 0, 0, 0})
	f.Add([]byte{0, 0, 0, 0, 0, 0, 0, 3, 'a'})
	f.Add([]byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff})
	f.Add([]byte{0, 0, 0, 0, 0, 0, 0, 5, '{', 'x', '}', 'y', 'z'})
	f.Add([]byte{0, 0, 0, 0, 0, 0, 0, 3, '{', '}', 'x'})

	f.Fuzz(func(t *testing.T, data []byte) {
		var got Request
		err := readFrame(bytes.NewReader(data), &got)

		// The size guard exists so that a damaged header cannot make the
		// parent allocate gigabytes before noticing. It must fire from the
		// header alone, without reading a payload.
		if len(data) >= 8 {
			if announced := binary.BigEndian.Uint64(data[:8]); announced > 1<<30 && err == nil {
				t.Fatalf("accepted a frame announcing %d bytes", announced)
			}
		}

		if err != nil {
			return
		}

		// Whatever decoded has to survive a round trip: that is the property
		// the parent and the child rely on to stay in step.
		var buf bytes.Buffer
		if err := writeFrame(&buf, got); err != nil {
			t.Fatalf("writeFrame after a successful read: %v", err)
		}
		var again Request
		if err := readFrame(&buf, &again); err != nil {
			t.Fatalf("re-reading a frame we just wrote: %v", err)
		}
		if !reflect.DeepEqual(got, again) {
			t.Fatalf("round trip changed the request: %+v -> %+v", got, again)
		}
	})
}
