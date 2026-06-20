package client

import "testing"

// TestParseSSE_BasicFrames splits two complete frames and leaves no remainder.
func TestParseSSE_BasicFrames(t *testing.T) {
	in := "event: log\ndata: {\"stream\":\"stdout\",\"text\":\"hi\"}\n\nevent: end\ndata: {}\n\n"
	frames, rest := ParseSSE(in)
	if rest != "" {
		t.Fatalf("rest=%q, want empty", rest)
	}
	if len(frames) != 2 {
		t.Fatalf("got %d frames, want 2: %+v", len(frames), frames)
	}
	if frames[0].Event != "log" || string(frames[0].Data) != `{"stream":"stdout","text":"hi"}` {
		t.Fatalf("frame0=%+v", frames[0])
	}
	if frames[1].Event != "end" {
		t.Fatalf("frame1=%+v", frames[1])
	}
}

// TestParseSSE_PartialRemainder keeps an unterminated trailing frame as rest so
// the caller can carry it into the next read.
func TestParseSSE_PartialRemainder(t *testing.T) {
	in := "event: log\ndata: {\"text\":\"a\"}\n\nevent: log\ndata: {\"text\"" // no closing \n\n
	frames, rest := ParseSSE(in)
	if len(frames) != 1 {
		t.Fatalf("got %d frames, want 1", len(frames))
	}
	if rest != "event: log\ndata: {\"text\"" {
		t.Fatalf("rest=%q", rest)
	}
}

// TestParseSSE_CRLFAndLeadingSpace normalises \r\n and strips one leading space
// after data:.
func TestParseSSE_CRLFAndLeadingSpace(t *testing.T) {
	in := "event: status\r\ndata:  {\"status\":\"done\"}\r\n\r\n"
	frames, _ := ParseSSE(in)
	if len(frames) != 1 {
		t.Fatalf("got %d frames, want 1", len(frames))
	}
	// data: has TWO spaces; only the first is stripped, leaving one.
	if frames[0].Event != "status" || string(frames[0].Data) != ` {"status":"done"}` {
		t.Fatalf("frame=%+v", frames[0])
	}
}

// TestParseSSE_IgnoresCommentAndOpenLine skips the SSE open comment (": open")
// and frames without an event line.
func TestParseSSE_IgnoresCommentAndOpenLine(t *testing.T) {
	in := ": open\n\nevent: log\ndata: {}\n\n"
	frames, _ := ParseSSE(in)
	if len(frames) != 1 || frames[0].Event != "log" {
		t.Fatalf("frames=%+v", frames)
	}
}
