package coverage

import (
	"bufio"
	"fmt"
	"net"
	"strings"
	"time"
)

// Client provides a convenient way to talk to the coverage server.
type Client struct{ socket string }

// NewClient creates a client that talks to the default socket path.
func NewClient() *Client { return &Client{socket: socketPath} }

// DumpAndHash triggers a dump and returns (hash, dir).
func (c *Client) DumpAndHash() (string, string, error) {
	resp, err := c.send("dump")
	if err != nil {
		return "", "", err
	}
	// Expected format: OK HASH:<hex> DIR:<dir>
	parts := strings.Fields(resp)
	if len(parts) < 3 {
		return "", "", fmt.Errorf("unexpected response: %q", resp)
	}
	hash := strings.TrimPrefix(parts[1], "HASH:")
	dir := strings.TrimPrefix(parts[2], "DIR:")
	return hash, dir, nil
}

// HashOnly returns only the current hash without persisting to disk.
func (c *Client) HashOnly() (string, error) {
	resp, err := c.send("status")
	if err != nil {
		return "", err
	}
	parts := strings.Fields(resp)
	if len(parts) < 2 {
		return "", fmt.Errorf("unexpected response: %q", resp)
	}
	return strings.TrimPrefix(parts[1], "HASH:"), nil
}

// ResetCoverage zeroes out counters on the server.
func (c *Client) ResetCoverage() error {
	_, err := c.send("reset")
	return err
}

// send opens a connection, writes cmd, waits for single‑line reply.
func (c *Client) send(cmd string) (string, error) {
	// tiny delay in case server hasn't started yet
	time.Sleep(50 * time.Millisecond)

	conn, err := net.Dial("unix", c.socket)
	if err != nil {
		return "", fmt.Errorf("connect: %w", err)
	}
	defer conn.Close()

	if _, err := conn.Write([]byte(cmd)); err != nil {
		return "", err
	}

	r := bufio.NewReader(conn)
	resp, err := r.ReadString('\n')
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(resp), nil
}