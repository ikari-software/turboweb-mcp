package main

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// filehost is a tiny loopback HTTP server that hands the bytes of a single
// host file to the browser extension. It exists for drag_drop_file, which —
// unlike set_input_files — never touches CDP and so cannot use
// DOM.setFileInputFiles to inject file bytes. The content script fetches
// GET /file/{token} and gets a Blob it can wrap in a real File and drop on
// a drag-drop zone.
//
// Security posture (see design/drag_drop_file.md §5):
//   - Binds 127.0.0.1 only — never reachable off-host.
//   - Serves files by opaque single-use token, never by path. The HTTP
//     request carries no filesystem path, so there is no traversal surface.
//   - Tokens are 32 bytes of crypto/rand, expire ~30s after minting, and
//     are invalidated on first successful GET.
//
// This is a sibling of the WS daemon (:18321) and the optional native
// resizer (:18322); it defaults to :18323.

// fileHostPort is the preferred fixed port. A fixed default keeps any
// CSP allow-listing predictable; if the port is taken we fall back to an
// ephemeral one and report whatever we actually bound.
const fileHostPort = 18323

// fileGrantTTL is how long a minted token stays valid. 30s is ample for
// the extension to fetch the file once; past that the grant 410s.
const fileGrantTTL = 30 * time.Second

// fileGrant records the single file a token is allowed to fetch. The path
// is validated and canonicalised before the grant is created, so the HTTP
// handler never has to touch user-controlled path input.
type fileGrant struct {
	absPath   string
	mimeType  string
	size      int64
	fileName  string
	expiresAt time.Time
	used      bool
}

// fileHost owns the loopback listener and the live token→grant map.
type fileHostServer struct {
	mu      sync.Mutex
	grants  map[string]*fileGrant
	port    int // the port actually bound (fixed or ephemeral)
	started bool
}

var (
	fileHost     = &fileHostServer{grants: map[string]*fileGrant{}}
	fileHostOnce sync.Once
)

// ensureFileHost starts the loopback file server on first use and leaves it
// running for the process lifetime. It is idempotent and safe to call from
// every drag_drop_file invocation. Returns the port that was bound, or an
// error if even the ephemeral fallback fails.
func ensureFileHost() (int, error) {
	var startErr error
	fileHostOnce.Do(func() {
		// Try the fixed port first, then fall back to an OS-assigned one.
		ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", fileHostPort))
		if err != nil {
			ln, err = net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				startErr = fmt.Errorf("file host: cannot bind loopback: %w", err)
				return
			}
		}
		fileHost.port = ln.Addr().(*net.TCPAddr).Port
		fileHost.started = true

		mux := http.NewServeMux()
		mux.HandleFunc("/file/", fileHost.handleFile)
		mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		})
		srv := &http.Server{Handler: mux}
		go func() {
			if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
				logger.Printf("file host: serve error: %v", err)
			}
		}()
		go fileHost.sweepLoop()
		logger.Printf("file host listening on 127.0.0.1:%d", fileHost.port)
	})
	if startErr != nil {
		return 0, startErr
	}
	return fileHost.port, nil
}

// mintFileGrant records a single-use grant for an already-validated absolute
// path and returns its opaque token. The caller is responsible for path
// validation (see resolveHostPath); the token is the only thing that ever
// reaches the browser.
func (h *fileHostServer) mintFileGrant(absPath, fileName, mime string, size int64) (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("file host: token generation failed: %w", err)
	}
	token := hex.EncodeToString(buf)
	h.mu.Lock()
	h.grants[token] = &fileGrant{
		absPath:   absPath,
		mimeType:  mime,
		size:      size,
		fileName:  fileName,
		expiresAt: time.Now().Add(fileGrantTTL),
	}
	h.mu.Unlock()
	return token, nil
}

// handleFile serves GET /file/{token}. It rejects anything that is not a
// known, unexpired, unused token; on success it streams the file once and
// invalidates the grant. There is no path in the URL, so no traversal is
// possible — a hostile page can at most guess a 256-bit token.
func (h *fileHostServer) handleFile(w http.ResponseWriter, r *http.Request) {
	// CORS: the content-script fetch carries the page's Origin. The
	// endpoint is loopback, single-use, and token-gated, so there is
	// nothing for CORS to protect that the token does not already — a
	// permissive ACAO simply lets the cross-origin fetch through.
	w.Header().Set("Access-Control-Allow-Origin", "*")
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	token := strings.TrimPrefix(r.URL.Path, "/file/")
	// A token is a fixed 64-char hex string; anything else (empty, a
	// stray "../", a path segment) can never match a real grant.
	if token == "" || strings.ContainsAny(token, "/.") {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	h.mu.Lock()
	grant, ok := h.grants[token]
	if !ok {
		h.mu.Unlock()
		http.Error(w, "unknown file token", http.StatusNotFound)
		return
	}
	if grant.used || time.Now().After(grant.expiresAt) {
		h.mu.Unlock()
		http.Error(w, "file grant expired or already used", http.StatusGone)
		return
	}
	// Mark used before serving so a concurrent or repeat request for the
	// same token loses the race and 410s. The spent grant is kept as a
	// tombstone (not deleted) until expiry so a retry gets an honest
	// "already used" 410 rather than an ambiguous 404; the sweeper drops
	// it once expiresAt passes.
	grant.used = true
	h.mu.Unlock()

	f, err := os.Open(grant.absPath)
	if err != nil {
		http.Error(w, "file no longer readable", http.StatusInternalServerError)
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		http.Error(w, "file no longer readable", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", grant.mimeType)
	// http.ServeContent sets Content-Length and handles range requests;
	// the modtime drives only its caching headers, harmless here.
	http.ServeContent(w, r, grant.fileName, info.ModTime(), f)
}

// sweepLoop drops expired grants every TTL interval so a token that is
// minted but never fetched does not linger in the map.
func (h *fileHostServer) sweepLoop() {
	ticker := time.NewTicker(fileGrantTTL)
	defer ticker.Stop()
	for range ticker.C {
		now := time.Now()
		h.mu.Lock()
		for token, g := range h.grants {
			if g.used || now.After(g.expiresAt) {
				delete(h.grants, token)
			}
		}
		h.mu.Unlock()
	}
}
