package termproto

import (
	"bytes"
	"errors"
	"testing"
)

func TestRecordFrameRoundTrip(t *testing.T) {
	in := RecordChunk{Req: 7, Flags: RecFlagEOF | RecFlagLast, Seg: 3, Off: 4, NextSeg: 4, NextOff: 99, Data: []byte("events")}
	out, err := DecodeRecord(EncodeRecord(in))
	if err != nil {
		t.Fatal(err)
	}
	if out.Req != 7 || out.Flags != in.Flags || out.Seg != 3 || out.Off != 4 || out.NextSeg != 4 || out.NextOff != 99 ||
		!bytes.Equal(out.Data, in.Data) {
		t.Fatalf("round trip: %+v", out)
	}
	if _, err := DecodeRecord([]byte{FrameRecord, 0, 0}); !errors.Is(err, ErrShortFrame) {
		t.Fatalf("short frame err = %v", err)
	}
	wrong := EncodeRecord(in)
	wrong[0] = FrameOutput
	if _, err := DecodeRecord(wrong); !errors.Is(err, ErrWrongFrame) {
		t.Fatalf("wrong type err = %v", err)
	}
	big := EncodeRecord(RecordChunk{Data: make([]byte, MaxRecordFrameData+1)})
	if _, err := DecodeRecord(big); !errors.Is(err, ErrPayloadTooBig) {
		t.Fatalf("oversized err = %v", err)
	}
	if MaxRecordFrameData+recordHeaderLen >= MaxControlFrame {
		t.Fatal("a record frame must fit within the Hub read limit")
	}
}

func TestSessionInfoSanitizeRecordFields(t *testing.T) {
	sid := "0123456789abcdef0123456789abcdef"
	got, ok := SessionInfo{SID: sid, Rec: "bogus", RecBytes: -5, RecFirst: -1, RecLast: -2}.Sanitized()
	if !ok || got.Rec != "" || got.RecBytes != 0 || got.RecFirst != 0 || got.RecLast != 0 {
		t.Fatalf("sanitize: %+v", got)
	}
	for _, rec := range []string{RecOn, RecPaused, RecError, RecOff, RecVeto, RecIncognito} {
		got, _ := SessionInfo{SID: sid, Rec: rec}.Sanitized()
		if got.Rec != rec {
			t.Fatalf("rec %q lost", rec)
		}
	}
}
