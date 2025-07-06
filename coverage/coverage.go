package coverage

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"log"
	"net"
	"os"
	"path/filepath"
	rtcover "runtime/coverage"
	"strings"
	"sync"

	"github.com/google/uuid"
)

// Socket used by the coverage server and client
const socketPath = "/tmp/coverage_socket"

var (
	mu sync.Mutex // protects counter operations
)

func init() {
	go startUnixServer()
}

// ----------------------
//
//	Server implementation
//
// ----------------------
func startUnixServer() {
	// Ensure stale socket file is removed first
	_ = os.Remove(socketPath)

	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		log.Fatalf("[coverage] failed to start server: %v", err)
	}
	defer ln.Close()

	log.Printf("[coverage] server listening on %s", socketPath)

	for {
		conn, err := ln.Accept()
		if err != nil {
			log.Printf("[coverage] accept error: %v", err)
			continue
		}
		go handleRequest(conn)
	}
}

func handleRequest(conn net.Conn) {
	defer conn.Close()

	buf := make([]byte, 256)
	n, err := conn.Read(buf)
	if err != nil {
		log.Printf("[coverage] read error: %v", err)
		return
	}
	cmd := strings.TrimSpace(string(buf[:n]))

	switch cmd {
	case "dump":
		dumpCoverage(conn)
	case "reset":
		resetCoverage(conn)
	case "status":
		statusCoverage(conn)
	default:
		_, _ = conn.Write([]byte("ERR: unknown command\n"))
	}
}

// ----------------------
//
//	Coverage operations
//
// ----------------------
func dumpCoverage(conn net.Conn) {
	dir, hash, err := writeAndHashCounters()
	if err != nil {
		_, _ = conn.Write([]byte(fmt.Sprintf("ERR: %v\n", err)))
		return
	}
	_, _ = conn.Write([]byte(fmt.Sprintf("OK HASH:%s DIR:%s\n", hash, dir)))
}

func statusCoverage(conn net.Conn) {
	_, hash, err := writeAndHashCountersTemp()
	if err != nil {
		_, _ = conn.Write([]byte(fmt.Sprintf("ERR: %v\n", err)))
		return
	}
	_, _ = conn.Write([]byte(fmt.Sprintf("OK HASH:%s\n", hash)))
}

func resetCoverage(conn net.Conn) {
	mu.Lock()
	defer mu.Unlock()

	if err := rtcover.ClearCounters(); err != nil {
		_, _ = conn.Write([]byte(fmt.Sprintf("ERR: %v\n", err)))
		return
	}
	_, _ = conn.Write([]byte("OK RESET\n"))
}

// writeAndHashCounters writes the current counters & meta to a UUID dir under ./coverage
// and returns that dir path + the content hash.
func writeAndHashCounters() (string, string, error) {
	mu.Lock()
	defer mu.Unlock()

	tag := uuid.New().String()
	dir := filepath.Join("coverage", tag)

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", "", err
	}
	if err := rtcover.WriteCountersDir(dir); err != nil {
		return "", "", err
	}
	if err := rtcover.WriteMetaDir(dir); err != nil {
		return "", "", err
	}

	hash, err := hashDir(dir)
	return dir, hash, err
}

// writeAndHashCountersTemp is like writeAndHashCounters but cleans up the dir afterwards.
func writeAndHashCountersTemp() (string, string, error) {
	dir, hash, err := writeAndHashCounters()
	if err != nil {
		return dir, hash, err
	}
	_ = os.RemoveAll(dir)
	return dir, hash, nil
}

// hashDir walks a directory and returns the hex‑encoded SHA‑256 of every file's content.
func hashDir(dir string) (string, error) {
	h := sha256.New()
	walkFn := func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		h.Write(data)
		return nil
	}
	if err := filepath.WalkDir(dir, walkFn); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
