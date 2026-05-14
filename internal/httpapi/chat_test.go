package httpapi

import "testing"

func TestInspectSSEChunkDetectsDone(t *testing.T) {
	partial, count, sawDone, last, snapshot := inspectSSEChunk("", "data: {\"id\":\"1\"}\n\ndata: [DONE]\n\n", 0, false, nil, streamUsageSnapshot{})
	if partial != "" {
		t.Fatalf("partial = %q, want empty", partial)
	}
	if count != 2 {
		t.Fatalf("count = %d, want 2", count)
	}
	if !sawDone {
		t.Fatal("sawDone = false, want true")
	}
	if len(last) != 2 || last[1] != "[DONE]" {
		t.Fatalf("last = %#v", last)
	}
	if snapshot.present {
		t.Fatalf("snapshot.present = true, want false")
	}
}

func TestInspectSSEChunkHandlesSplitLines(t *testing.T) {
	partial, count, sawDone, last, snapshot := inspectSSEChunk("", "data: {\"id\":\"1\"", 0, false, nil, streamUsageSnapshot{})
	if partial == "" {
		t.Fatal("partial should keep unfinished line")
	}
	if count != 0 || sawDone {
		t.Fatalf("count=%d sawDone=%t", count, sawDone)
	}
	partial, count, sawDone, last, snapshot = inspectSSEChunk(partial, "}\n\n", count, sawDone, last, snapshot)
	if partial != "" {
		t.Fatalf("partial = %q, want empty", partial)
	}
	if count != 1 {
		t.Fatalf("count = %d, want 1", count)
	}
	if sawDone {
		t.Fatal("sawDone = true, want false")
	}
	if len(last) != 1 {
		t.Fatalf("last = %#v", last)
	}
	if last[0] != "data" {
		t.Fatalf("last[0] = %q, want data", last[0])
	}
	if snapshot.present {
		t.Fatalf("snapshot.present = true, want false")
	}
}

func TestInspectSSEChunkExtractsUsageFromPayload(t *testing.T) {
	_, count, sawDone, last, snapshot := inspectSSEChunk("", "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":12,\"completion_tokens\":3,\"total_tokens\":15}}\n\n", 0, false, nil, streamUsageSnapshot{})
	if count != 1 {
		t.Fatalf("count = %d, want 1", count)
	}
	if sawDone {
		t.Fatal("sawDone = true, want false")
	}
	if len(last) != 1 {
		t.Fatalf("last = %#v", last)
	}
	if last[0] != "usage" {
		t.Fatalf("last[0] = %q, want usage", last[0])
	}
	if !snapshot.present {
		t.Fatal("snapshot.present = false, want true")
	}
	if snapshot.requestTokens != 12 || snapshot.responseTokens != 3 || snapshot.totalTokens != 15 {
		t.Fatalf("snapshot = %+v, want request=12 response=3 total=15", snapshot)
	}
}
