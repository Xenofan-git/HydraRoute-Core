package watchlist

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"sync"
)

const (
	maxClients   = 4
	inputBuffer  = 512
	outputBuffer = 8192
)

type Server struct {
	listener *net.UnixListener
	mu       sync.RWMutex
	snapshot *Snapshot
	clients  map[net.Conn]struct{}
	sem      chan struct{}
	done     chan struct{}
	wg       sync.WaitGroup
	once     sync.Once
}

func Listen(path string, snapshot *Snapshot) (*Server, error) {
	if path == "" || len(path) >= 108 {
		return nil, fmt.Errorf("invalid watchlist socket path")
	}
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSocket == 0 || info.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("watchlist socket path exists and is not a socket")
		}
		if err := os.Remove(path); err != nil {
			return nil, err
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	addr := &net.UnixAddr{Name: path, Net: "unix"}
	ln, err := net.ListenUnix("unix", addr)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0600); err != nil {
		_ = ln.Close()
		_ = os.Remove(path)
		return nil, err
	}
	s := &Server{listener: ln, snapshot: snapshot, clients: make(map[net.Conn]struct{}), sem: make(chan struct{}, maxClients), done: make(chan struct{})}
	go s.acceptLoop()
	return s, nil
}

func (s *Server) Update(snapshot *Snapshot) {
	s.mu.Lock()
	s.snapshot = snapshot
	s.mu.Unlock()
}

func (s *Server) acceptLoop() {
	defer close(s.done)
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			return
		}
		select {
		case s.sem <- struct{}{}:
			s.mu.Lock()
			s.clients[conn] = struct{}{}
			s.mu.Unlock()
			s.wg.Add(1)
			go s.serveClient(conn)
		default:
			_, _ = io.WriteString(conn, "ERR busy\n")
			_ = conn.Close()
		}
	}
}

func (s *Server) currentSnapshot() *Snapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.snapshot
}

func (s *Server) serveClient(conn net.Conn) {
	defer s.wg.Done()
	defer func() {
		_ = conn.Close()
		s.mu.Lock()
		delete(s.clients, conn)
		s.mu.Unlock()
		<-s.sem
	}()
	reader := bufio.NewReaderSize(conn, inputBuffer)
	writer := bufio.NewWriterSize(conn, outputBuffer)
	for {
		line, err := reader.ReadSlice('\n')
		if errors.Is(err, bufio.ErrBufferFull) {
			return
		}
		if err != nil {
			// An incomplete final line is intentionally ignored.
			return
		}
		if len(line) > inputBuffer-1 {
			return
		}
		line = line[:len(line)-1]
		if len(line) > 0 && line[len(line)-1] == '\r' {
			line = line[:len(line)-1]
		}
		switch {
		case string(line) == "DUMP":
			snapshot := s.currentSnapshot()
			if snapshot == nil {
				if _, err := writer.WriteString("END 0\n"); err != nil {
					return
				}
			} else if err := snapshot.Dump(writer); err != nil {
				return
			}
		case len(line) >= 6 && string(line[:6]) == "MATCH ":
			name := string(line[6:])
			snapshot := s.currentSnapshot()
			if snapshot == nil {
				if _, err := writer.WriteString("NO\n"); err != nil {
					return
				}
				break
			}
			entry, ok, matchErr := snapshot.Match(name)
			if matchErr != nil {
				if _, err := writer.WriteString("ERR name\n"); err != nil {
					return
				}
			} else if !ok {
				if _, err := writer.WriteString("NO\n"); err != nil {
					return
				}
			} else {
				if _, err := fmt.Fprintf(writer, "OK %s %d %s\n", entry.Target, entry.Rank, entry.Key); err != nil {
					return
				}
			}
		default:
			if _, err := writer.WriteString("ERR command\n"); err != nil {
				return
			}
		}
		if err := writer.Flush(); err != nil {
			return
		}
	}
}

func (s *Server) Close() error {
	var closeErr error
	s.once.Do(func() {
		closeErr = s.listener.Close()
		s.mu.Lock()
		for conn := range s.clients {
			_ = conn.Close()
		}
		s.mu.Unlock()
		<-s.done
		s.wg.Wait()
		_ = os.Remove(s.listener.Addr().String())
	})
	return closeErr
}
