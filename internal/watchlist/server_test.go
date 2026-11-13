package watchlist

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testSnapshot() *Snapshot {
	return &Snapshot{
		targets: []Target{{Rank: 0, Name: "RU"}, {Rank: 1, Name: "HydraRoute"}},
		entries: map[string]Entry{
			"ru":           {Key: "ru", Rank: 0, Target: "RU"},
			"telegram.org": {Key: "telegram.org", Rank: 1, Target: "HydraRoute"},
		},
		keys: []Entry{{Key: "ru", Rank: 0, Target: "RU"}, {Key: "telegram.org", Rank: 1, Target: "HydraRoute"}},
	}
}

func dialSocket(t *testing.T, path string) net.Conn {
	t.Helper()
	conn, err := net.DialTimeout("unix", path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	return conn
}

func TestUnixSocketProtocolPipelineHalfCloseAndPermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "watchlist.sock")
	s, err := Listen(path, testSnapshot())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("socket permissions=%o, want 600", info.Mode().Perm())
	}
	conn := dialSocket(t, path)
	_, err = io.WriteString(conn, "MATCH Mail.Google.RU.\r\nMATCH sub.telegram.org\nBOGUS\nDUMP\n")
	if err != nil {
		t.Fatal(err)
	}
	if err := conn.(*net.UnixConn).CloseWrite(); err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(conn)
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()
	got := string(data)
	want := "OK RU 0 ru\nOK HydraRoute 1 telegram.org\nERR command\nT 0 RU\nT 1 HydraRoute\nK ru 0\nK telegram.org 1\nEND 2\n"
	if got != want {
		t.Fatalf("protocol output:\n%s\nwant:\n%s", got, want)
	}
}

func TestUnixSocketClientLimitAndCloseRemovesSocket(t *testing.T) {
	path := filepath.Join(t.TempDir(), "watchlist.sock")
	s, err := Listen(path, testSnapshot())
	if err != nil {
		t.Fatal(err)
	}
	conns := make([]net.Conn, 0, maxClients)
	for i := 0; i < maxClients; i++ {
		conns = append(conns, dialSocket(t, path))
	}
	time.Sleep(30 * time.Millisecond)
	busy := dialSocket(t, path)
	_ = busy.SetReadDeadline(time.Now().Add(time.Second))
	line, err := bufio.NewReader(busy).ReadString('\n')
	if err != nil || line != "ERR busy\n" {
		t.Fatalf("fifth client response=%q err=%v", line, err)
	}
	_ = busy.Close()
	for _, conn := range conns {
		_ = conn.Close()
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("socket path remains after close: %v", err)
	}
}

func TestSlowDumpDoesNotBlockOtherClients(t *testing.T) {
	path := filepath.Join(t.TempDir(), "watchlist.sock")
	snapshot := &Snapshot{targets: []Target{{Rank: 0, Name: "DIRECT"}}, entries: make(map[string]Entry)}
	for i := 0; i < 20000; i++ {
		key := fmt.Sprintf("k%05d.example", i)
		snapshot.entries[key] = Entry{Key: key, Rank: 0, Target: "DIRECT"}
		snapshot.keys = append(snapshot.keys, Entry{Key: key, Rank: 0, Target: "DIRECT"})
	}
	s, err := Listen(path, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	dumper := dialSocket(t, path)
	if _, err := io.WriteString(dumper, "DUMP\n"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(30 * time.Millisecond)
	matcher := dialSocket(t, path)
	_ = matcher.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := io.WriteString(matcher, "MATCH k00042.example\n"); err != nil {
		t.Fatal(err)
	}
	reply, err := bufio.NewReader(matcher).ReadString('\n')
	if err != nil || reply != "OK DIRECT 0 k00042.example\n" {
		t.Fatalf("MATCH while DUMP blocked = %q err=%v", reply, err)
	}
	_ = matcher.Close()
	_ = dumper.Close()
}

func TestOverlongRequestClosesConnection(t *testing.T) {
	path := filepath.Join(t.TempDir(), "watchlist.sock")
	s, err := Listen(path, testSnapshot())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	conn := dialSocket(t, path)
	_ = conn.SetDeadline(time.Now().Add(time.Second))
	_, _ = io.WriteString(conn, "MATCH "+strings.Repeat("a", inputBuffer)+"\n")
	_, err = bufio.NewReader(conn).ReadByte()
	if err == nil {
		t.Fatal("overlong request should close connection without a reply")
	}
	_ = conn.Close()
}
