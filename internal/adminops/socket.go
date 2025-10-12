package adminops

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// SocketName is the admin socket's file name in the data directory. Its
// file mode (0600) is the authorization: whoever can open it can read the
// data directory anyway.
const SocketName = "admin.sock"

// ErrNoServer means no service listens on the socket.
var ErrNoServer = errors.New("the service is not running")

// maxSocketPath is the longest unix socket path every platform accepts
// (macOS allows 104 bytes including the terminating NUL; Linux 108).
const maxSocketPath = 103

// SocketPath returns where the admin socket of a data directory lives: in
// the directory when the path fits a unix socket address, otherwise in a
// private (0700) directory under the system temp directory, named after the
// data directory so the service and the CLI agree on it.
func SocketPath(dataDir string) string {
	p := filepath.Join(dataDir, SocketName)
	if abs, err := filepath.Abs(p); err == nil {
		p = abs
	}
	if len(p) <= maxSocketPath {
		return p
	}
	sum := sha256.Sum256([]byte(filepath.Dir(p)))
	return filepath.Join(privateDir(), hex.EncodeToString(sum[:8])+".sock")
}

func privateDir() string {
	return filepath.Join(os.TempDir(), fmt.Sprintf("meilisearch-cdc-%d", os.Getuid()))
}

// checkSocketDir makes sure a socket outside the data directory sits in a
// directory only this user controls, so nobody else can plant a socket that
// receives admin commands (and the passwords they carry).
func checkSocketDir(path string, create bool) error {
	dir := filepath.Dir(path)
	if dir != privateDir() {
		return nil
	}
	if create {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
	}
	fi, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !fi.IsDir() || !private(fi) {
		return fmt.Errorf("%s must be a directory owned by this user with mode 0700", dir)
	}
	return nil
}

type wireResponse struct {
	Result Result `json:"result"`
	Error  string `json:"error,omitempty"`
}

// Listen creates the admin socket at path. A socket left behind by a
// crashed service is replaced; one a live service answers on is not.
func Listen(path string) (net.Listener, error) {
	if err := checkSocketDir(path, true); err != nil {
		return nil, err
	}
	if c, err := net.DialTimeout("unix", path, time.Second); err == nil {
		c.Close()
		return nil, fmt.Errorf("%s: another service is listening on it", path)
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	l, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		l.Close()
		return nil, err
	}
	return l, nil
}

// Handler answers commands sent by Call.
func (o *Ops) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/command", func(w http.ResponseWriter, r *http.Request) {
		var req Request
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
			http.Error(w, "invalid JSON body: "+err.Error(), http.StatusBadRequest)
			return
		}
		// A command runs to the end even if the CLI goes away, so a purge
		// never stops halfway.
		res, err := o.Do(context.WithoutCancel(r.Context()), req)
		out := wireResponse{Result: res}
		if err != nil {
			out.Error = err.Error()
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(out)
	})
	return mux
}

// Call sends a command to the service listening on the socket at path. It
// returns ErrNoServer when no service listens there.
func Call(ctx context.Context, path string, req Request) (Result, error) {
	if err := checkSocketDir(path, false); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Result{}, ErrNoServer
		}
		return Result{}, err
	}
	conn, err := net.DialTimeout("unix", path, 2*time.Second)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.ENOENT) {
			return Result{}, ErrNoServer
		}
		return Result{}, fmt.Errorf("connect to the admin socket %s: %w", path, err)
	}
	conn.Close()
	client := &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", path)
		},
	}}
	defer client.CloseIdleConnections()
	body, err := json.Marshal(req) // #nosec G117 -- password goes to the local admin socket
	if err != nil {
		return Result{}, err
	}
	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://admin/v1/command", bytes.NewReader(body))
	if err != nil {
		return Result{}, err
	}
	hreq.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(hreq)
	if err != nil {
		return Result{}, fmt.Errorf("admin socket %s: %w", path, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return Result{}, err
	}
	if resp.StatusCode != http.StatusOK {
		return Result{}, fmt.Errorf("admin socket: %s", bytes.TrimSpace(raw))
	}
	var out wireResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return Result{}, fmt.Errorf("admin socket: %w", err)
	}
	if out.Error != "" {
		return out.Result, errors.New(out.Error)
	}
	return out.Result, nil
}
