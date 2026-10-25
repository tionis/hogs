package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
)

type fakeJournal struct {
	mu    sync.Mutex
	calls [][]string
	reply func(args []string) (output string, exitCode string)
}

func (f *fakeJournal) install(t *testing.T) {
	t.Helper()
	previous := journalCommand
	journalCommand = func(ctx context.Context, args ...string) *exec.Cmd {
		f.mu.Lock()
		f.calls = append(f.calls, args)
		f.mu.Unlock()
		output, code := f.reply(args)
		cmd := exec.CommandContext(ctx, "sh", "-c", `printf '%s' "$OUT"; exit "$CODE"`)
		cmd.Env = append(os.Environ(), "OUT="+output, "CODE="+code)
		return cmd
	}
	t.Cleanup(func() { journalCommand = previous })
}

func journalRecord(cursor, message string) string {
	raw, _ := json.Marshal(map[string]string{
		"__CURSOR": cursor, "__REALTIME_TIMESTAMP": "1761400000000000", "MESSAGE": message,
	})
	return string(raw) + "\n"
}

func hasArg(args []string, want string) bool {
	for _, arg := range args {
		if arg == want {
			return true
		}
	}
	return false
}

func consoleLines(t *testing.T, body string) []map[string]string {
	t.Helper()
	var lines []map[string]string
	for _, raw := range strings.Split(strings.TrimSpace(body), "\n") {
		if raw == "" {
			continue
		}
		var line map[string]string
		if err := json.Unmarshal([]byte(raw), &line); err != nil {
			t.Fatalf("decode %q: %v", raw, err)
		}
		lines = append(lines, line)
	}
	return lines
}

func serveConsole(query string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodGet, "/v1/servers/pal/console"+query, nil)
	request.SetPathValue("serverID", "pal")
	recorder := httptest.NewRecorder()
	handleConsole(recorder, request, &ServerConfig{Unit: "palworld.service", GameType: "palworld"})
	return recorder
}

func TestConsoleReplaysAfterCursorThenFollows(t *testing.T) {
	journal := &fakeJournal{reply: func(args []string) (string, string) {
		if hasArg(args, "-f") {
			return journalRecord("c4", "world"), "0"
		}
		return journalRecord("c2", "hello") +
			journalRecord("c3", "[LOG] REST accessed endpoint /v1/api/metrics OK"), "0"
	}}
	journal.install(t)

	recorder := serveConsole("?after_cursor=c1")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body)
	}
	lines := consoleLines(t, recorder.Body.String())
	if len(lines) != 2 || lines[0]["line"] != "hello" || lines[0]["cursor"] != "c2" ||
		lines[1]["line"] != "world" || lines[1]["cursor"] != "c4" {
		t.Fatalf("lines=%v", lines)
	}
	if len(journal.calls) != 2 || !hasArg(journal.calls[0], "--after-cursor=c1") {
		t.Fatalf("replay calls=%v", journal.calls)
	}
	// Follow resumes after the newest replayed entry, including a filtered one.
	if !hasArg(journal.calls[1], "-f") || !hasArg(journal.calls[1], "--after-cursor=c3") {
		t.Fatalf("follow args=%v", journal.calls[1])
	}
}

func TestConsoleFallsBackWhenCursorIsStale(t *testing.T) {
	journal := &fakeJournal{reply: func(args []string) (string, string) {
		switch {
		case hasArg(args, "-f"):
			return "", "0"
		case hasArg(args, "--after-cursor=gone"):
			return "", "1"
		default:
			// journalctl --reverse prints the newest entry first.
			return journalRecord("c9", "recent") + journalRecord("c8", "older"), "0"
		}
	}}
	journal.install(t)

	recorder := serveConsole("?after_cursor=gone")
	lines := consoleLines(t, recorder.Body.String())
	if recorder.Code != http.StatusOK || len(lines) != 2 || lines[0]["line"] != "older" || lines[1]["line"] != "recent" {
		t.Fatalf("status=%d lines=%v", recorder.Code, lines)
	}
	if len(journal.calls) != 3 || !hasArg(journal.calls[1], "--reverse") || !hasArg(journal.calls[2], "--after-cursor=c9") {
		t.Fatalf("calls=%v", journal.calls)
	}
}

func TestConsoleBacklogCountsOnlyVisibleLines(t *testing.T) {
	routine := journalRecord("r", "[LOG] REST accessed endpoint /v1/api/metrics OK")
	journal := &fakeJournal{reply: func(args []string) (string, string) {
		if hasArg(args, "-f") {
			return "", "0"
		}
		// journalctl --reverse prints the newest entry first.
		out := journalRecord("c9", "[LOG] REST accessed endpoint /v1/api/info OK")
		for i := 0; i < 50; i++ {
			out += routine
		}
		return out + journalRecord("c3", "started") + routine + journalRecord("c2", "loading") + journalRecord("c1", "booting"), "0"
	}}
	journal.install(t)

	lines := consoleLines(t, serveConsole("?backlog=2").Body.String())
	if len(lines) != 2 || lines[0]["line"] != "loading" || lines[1]["line"] != "started" {
		t.Fatalf("lines=%v", lines)
	}
	if !hasArg(journal.calls[0], "--reverse") {
		t.Fatalf("replay args=%v", journal.calls[0])
	}
	// Follow resumes after the newest entry read, even though it was hidden.
	if !hasArg(journal.calls[1], "--after-cursor=c9") {
		t.Fatalf("follow args=%v", journal.calls[1])
	}
}

func TestConsoleWithoutCursorStartsLive(t *testing.T) {
	journal := &fakeJournal{reply: func(args []string) (string, string) { return "", "0" }}
	journal.install(t)

	if recorder := serveConsole(""); recorder.Code != http.StatusOK {
		t.Fatalf("status=%d", recorder.Code)
	}
	if len(journal.calls) != 1 || !hasArg(journal.calls[0], "-f") || !hasArg(journal.calls[0], "0") {
		t.Fatalf("calls=%v", journal.calls)
	}
}

func TestConsoleRejectsInvalidReplayParameters(t *testing.T) {
	journal := &fakeJournal{reply: func(args []string) (string, string) { return "", "0" }}
	journal.install(t)

	for _, query := range []string{"?after_cursor=bad%20cursor", "?backlog=501", "?backlog=-1"} {
		if recorder := serveConsole(query); recorder.Code != http.StatusBadRequest {
			t.Fatalf("%s status=%d", query, recorder.Code)
		}
	}
	if len(journal.calls) != 0 {
		t.Fatalf("journalctl ran for invalid input: %v", journal.calls)
	}
}

func TestParseJournalEntryDecodesByteArrayMessages(t *testing.T) {
	entry, ok := parseJournalEntry([]byte(`{"__CURSOR":"c1","__REALTIME_TIMESTAMP":"1761400000000000","MESSAGE":[104,105,255]}`))
	if !ok || entry.Message != "hi\xff" || entry.Cursor != "c1" || entry.Time.Unix() != 1761400000 {
		t.Fatalf("entry=%#v ok=%v", entry, ok)
	}
	if _, ok := parseJournalEntry([]byte(`{"MESSAGE":"no cursor"}`)); ok {
		t.Fatal("entry without cursor was accepted")
	}
}
