package hosting

import (
	"archive/tar"
	"compress/gzip"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base32"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"log"
	"math/big"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	defaultMaxUploadBytes = int64(25 << 20)
	defaultMaxSiteBytes   = int64(100 << 20)
	defaultMaxFiles       = 500
	sessionLifetime       = 12 * time.Hour
	failedPINLimit        = 5
	failedPINWindow       = 10 * time.Minute
)

type Config struct {
	DataDir        string
	APIToken       string
	PublicURL      string
	MaxUploadBytes int64
	MaxSiteBytes   int64
	MaxFiles       int
	TrustProxy     bool
	Logger         *log.Logger
}

type Server struct {
	dataDir        string
	sitesDir       string
	tmpDir         string
	apiTokenDigest [sha256.Size]byte
	publicURL      *url.URL
	maxUploadBytes int64
	maxSiteBytes   int64
	maxFiles       int
	trustProxy     bool
	secret         []byte
	logger         *log.Logger
	attempts       *pinAttempts
}

type siteMetadata struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	PINMAC    string    `json:"pin_mac"`
	CreatedAt time.Time `json:"created_at"`
	Files     int       `json:"files"`
	Bytes     int64     `json:"bytes"`
}

type publishResponse struct {
	ID    string `json:"id"`
	URL   string `json:"url"`
	PIN   string `json:"pin"`
	Title string `json:"title"`
	Files int    `json:"files"`
	Bytes int64  `json:"bytes"`
}

func New(cfg Config) (*Server, error) {
	if strings.TrimSpace(cfg.DataDir) == "" {
		return nil, errors.New("data directory cannot be empty")
	}
	if strings.TrimSpace(cfg.APIToken) == "" {
		return nil, errors.New("API token cannot be empty")
	}
	if cfg.MaxUploadBytes <= 0 {
		cfg.MaxUploadBytes = defaultMaxUploadBytes
	}
	if cfg.MaxSiteBytes <= 0 {
		cfg.MaxSiteBytes = defaultMaxSiteBytes
	}
	if cfg.MaxFiles <= 0 {
		cfg.MaxFiles = defaultMaxFiles
	}
	if cfg.Logger == nil {
		cfg.Logger = log.New(io.Discard, "", 0)
	}

	var publicURL *url.URL
	if strings.TrimSpace(cfg.PublicURL) != "" {
		parsed, err := url.Parse(strings.TrimRight(strings.TrimSpace(cfg.PublicURL), "/"))
		if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
			return nil, errors.New("public URL must be an absolute http or https URL")
		}
		if parsed.RawQuery != "" || parsed.Fragment != "" || parsed.User != nil {
			return nil, errors.New("public URL cannot contain credentials, a query, or a fragment")
		}
		publicURL = parsed
	}

	sitesDir := filepath.Join(cfg.DataDir, "sites")
	tmpDir := filepath.Join(cfg.DataDir, "tmp")
	if err := os.MkdirAll(sitesDir, 0o700); err != nil {
		return nil, fmt.Errorf("create sites directory: %w", err)
	}
	if err := os.MkdirAll(tmpDir, 0o700); err != nil {
		return nil, fmt.Errorf("create temporary directory: %w", err)
	}
	secret, err := loadOrCreateSecret(filepath.Join(cfg.DataDir, ".server-secret"))
	if err != nil {
		return nil, err
	}

	return &Server{
		dataDir:        cfg.DataDir,
		sitesDir:       sitesDir,
		tmpDir:         tmpDir,
		apiTokenDigest: sha256.Sum256([]byte(cfg.APIToken)),
		publicURL:      publicURL,
		maxUploadBytes: cfg.MaxUploadBytes,
		maxSiteBytes:   cfg.MaxSiteBytes,
		maxFiles:       cfg.MaxFiles,
		trustProxy:     cfg.TrustProxy,
		secret:         secret,
		logger:         cfg.Logger,
		attempts:       newPINAttempts(),
	}, nil
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	switch {
	case r.URL.Path == "/healthz":
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			methodNotAllowed(w, http.MethodGet, http.MethodHead)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, "{\"status\":\"ok\"}\n")
	case r.URL.Path == "/":
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			methodNotAllowed(w, http.MethodGet, http.MethodHead)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, "{\"service\":\"atml\",\"publish\":\"POST /api/v1/sites\"}\n")
	case r.URL.Path == "/api/v1/sites":
		s.handlePublish(w, r)
	case strings.HasPrefix(r.URL.Path, "/s/"):
		s.handleSite(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) handlePublish(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, http.MethodPost)
		return
	}
	if !s.authorizedAPIRequest(r) {
		w.Header().Set("WWW-Authenticate", "Bearer")
		writeAPIError(w, http.StatusUnauthorized, "valid bearer token required")
		return
	}
	contentType := strings.ToLower(strings.TrimSpace(strings.Split(r.Header.Get("Content-Type"), ";")[0]))
	if contentType != "application/gzip" && contentType != "application/x-gzip" {
		writeAPIError(w, http.StatusUnsupportedMediaType, "Content-Type must be application/gzip")
		return
	}
	title, err := validateTitle(r.Header.Get("X-ATML-Title"))
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}

	stage, err := os.MkdirTemp(s.tmpDir, "upload-*")
	if err != nil {
		s.logger.Printf("create upload staging directory: %v", err)
		writeAPIError(w, http.StatusInternalServerError, "could not stage upload")
		return
	}
	keepStage := false
	defer func() {
		if !keepStage {
			_ = os.RemoveAll(stage)
		}
	}()
	contentDir := filepath.Join(stage, "files")
	if err := os.Mkdir(contentDir, 0o700); err != nil {
		s.logger.Printf("create content staging directory: %v", err)
		writeAPIError(w, http.StatusInternalServerError, "could not stage upload")
		return
	}

	limitedBody := http.MaxBytesReader(w, r.Body, s.maxUploadBytes)
	files, bytesWritten, err := extractArchive(limitedBody, contentDir, s.maxSiteBytes, s.maxFiles)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid upload: "+err.Error())
		return
	}
	if _, err := os.Stat(filepath.Join(contentDir, "index.html")); err != nil {
		writeAPIError(w, http.StatusBadRequest, "archive must contain index.html at its root")
		return
	}

	id, pin, err := s.allocateSiteIdentity()
	if err != nil {
		s.logger.Printf("allocate site identity: %v", err)
		writeAPIError(w, http.StatusInternalServerError, "could not allocate site")
		return
	}
	if title == "" {
		title = "Published site"
	}
	metadata := siteMetadata{
		ID:        id,
		Title:     title,
		PINMAC:    s.pinMAC(id, pin),
		CreatedAt: time.Now().UTC(),
		Files:     files,
		Bytes:     bytesWritten,
	}
	if err := writeMetadata(filepath.Join(stage, "site.json"), metadata); err != nil {
		s.logger.Printf("write site metadata: %v", err)
		writeAPIError(w, http.StatusInternalServerError, "could not save site")
		return
	}
	finalPath := filepath.Join(s.sitesDir, id)
	if err := os.Rename(stage, finalPath); err != nil {
		s.logger.Printf("commit site: %v", err)
		writeAPIError(w, http.StatusInternalServerError, "could not save site")
		return
	}
	keepStage = true

	response := publishResponse{
		ID:    id,
		URL:   s.siteURL(r, id),
		PIN:   pin,
		Title: title,
		Files: files,
		Bytes: bytesWritten,
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(response)
}

func (s *Server) handleSite(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/s/")
	parts := strings.SplitN(rest, "/", 2)
	id := parts[0]
	if !validSiteID(id) {
		http.NotFound(w, r)
		return
	}
	metadata, err := s.loadMetadata(id)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			s.logger.Printf("load metadata for %s: %v", id, err)
		}
		http.NotFound(w, r)
		return
	}
	if len(parts) == 1 {
		http.Redirect(w, r, "/s/"+id+"/", http.StatusMovedPermanently)
		return
	}
	asset := parts[1]
	if asset == "unlock" && r.Method == http.MethodPost {
		s.handleUnlock(w, r, metadata)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		methodNotAllowed(w, http.MethodGet, http.MethodHead)
		return
	}
	if !s.hasSiteSession(r, id) {
		s.servePINPage(w, http.StatusUnauthorized, metadata, false)
		return
	}
	s.serveAsset(w, r, id, asset)
}

func (s *Server) handleUnlock(w http.ResponseWriter, r *http.Request, metadata siteMetadata) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, http.MethodPost)
		return
	}
	clientKey := metadata.ID + "\x00" + s.remoteIP(r)
	if retryAfter, allowed := s.attempts.allowed(clientKey, time.Now()); !allowed {
		w.Header().Set("Retry-After", strconv.Itoa(int(retryAfter.Seconds())+1))
		s.servePINPage(w, http.StatusTooManyRequests, metadata, true)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 2<<10)
	if err := r.ParseForm(); err != nil {
		s.servePINPage(w, http.StatusBadRequest, metadata, true)
		return
	}
	pin := r.Form.Get("pin")
	actual := s.pinMAC(metadata.ID, pin)
	if len(pin) != 8 || subtle.ConstantTimeCompare([]byte(actual), []byte(metadata.PINMAC)) != 1 {
		s.attempts.failed(clientKey, time.Now())
		s.servePINPage(w, http.StatusUnauthorized, metadata, true)
		return
	}
	s.attempts.succeeded(clientKey)
	s.setSiteSession(w, r, metadata.ID)
	http.Redirect(w, r, "/s/"+metadata.ID+"/", http.StatusSeeOther)
}

func (s *Server) servePINPage(w http.ResponseWriter, status int, metadata siteMetadata, invalid bool) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; form-action 'self'; base-uri 'none'; frame-ancestors 'none'")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.WriteHeader(status)
	errorMessage := ""
	if invalid {
		errorMessage = `<p class="error" role="alert">That PIN is incorrect or temporarily rate-limited.</p>`
	}
	page := `<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>PIN required · ` + html.EscapeString(metadata.Title) + `</title>
<style>html{color-scheme:light dark;font:16px system-ui,sans-serif}body{display:grid;min-height:100vh;margin:0;place-items:center;background:#111827;color:#f9fafb}.card{width:min(22rem,calc(100% - 3rem));padding:2rem;border:1px solid #374151;border-radius:1rem;background:#1f2937;box-shadow:0 1rem 3rem #0006}h1{margin:0 0 .5rem;font-size:1.35rem}p{color:#d1d5db}.error{color:#fca5a5}label{display:block;margin:1.25rem 0 .4rem}input,button{box-sizing:border-box;width:100%;padding:.8rem;border-radius:.55rem;font:inherit}input{border:1px solid #6b7280;background:#111827;color:#fff;letter-spacing:.25em;text-align:center}button{margin-top:.8rem;border:0;background:#60a5fa;color:#07111f;font-weight:700;cursor:pointer}</style></head>
<body><main class="card"><h1>` + html.EscapeString(metadata.Title) + `</h1><p>This site is protected. Enter its 8-digit PIN to continue.</p>` + errorMessage + `<form method="post" action="/s/` + metadata.ID + `/unlock"><label for="pin">PIN</label><input id="pin" name="pin" type="password" inputmode="numeric" pattern="[0-9]{8}" minlength="8" maxlength="8" autocomplete="one-time-code" required autofocus><button type="submit">Open site</button></form></main></body></html>`
	_, _ = io.WriteString(w, page)
}

func (s *Server) serveAsset(w http.ResponseWriter, r *http.Request, id, asset string) {
	if asset == "" {
		asset = "index.html"
	}
	clean := path.Clean(asset)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") || strings.HasPrefix(asset, "/") || clean != strings.TrimSuffix(asset, "/") {
		http.NotFound(w, r)
		return
	}
	diskPath := filepath.Join(s.sitesDir, id, "files", filepath.FromSlash(clean))
	file, err := os.Open(diskPath)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if info.IsDir() {
		if !strings.HasSuffix(r.URL.Path, "/") {
			http.Redirect(w, r, r.URL.Path+"/", http.StatusMovedPermanently)
			return
		}
		_ = file.Close()
		diskPath = filepath.Join(diskPath, "index.html")
		file, err = os.Open(diskPath)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		defer file.Close()
		info, err = file.Stat()
		if err != nil || !info.Mode().IsRegular() {
			http.NotFound(w, r)
			return
		}
	}
	if !info.Mode().IsRegular() {
		http.NotFound(w, r)
		return
	}
	if contentType := mime.TypeByExtension(filepath.Ext(diskPath)); contentType != "" {
		w.Header().Set("Content-Type", contentType)
	}
	w.Header().Set("Cache-Control", "private, max-age=300")
	w.Header().Set("Referrer-Policy", "same-origin")
	http.ServeContent(w, r, info.Name(), info.ModTime(), file)
}

func (s *Server) authorizedAPIRequest(r *http.Request) bool {
	const prefix = "Bearer "
	header := r.Header.Get("Authorization")
	if !strings.HasPrefix(header, prefix) {
		return false
	}
	digest := sha256.Sum256([]byte(strings.TrimPrefix(header, prefix)))
	return subtle.ConstantTimeCompare(digest[:], s.apiTokenDigest[:]) == 1
}

func (s *Server) allocateSiteIdentity() (string, string, error) {
	for range 5 {
		randomID := make([]byte, 10)
		if _, err := rand.Read(randomID); err != nil {
			return "", "", err
		}
		id := strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(randomID))
		if _, err := os.Stat(filepath.Join(s.sitesDir, id)); err == nil {
			continue
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", "", err
		}
		pinValue, err := rand.Int(rand.Reader, big.NewInt(100_000_000))
		if err != nil {
			return "", "", err
		}
		return id, fmt.Sprintf("%08d", pinValue.Int64()), nil
	}
	return "", "", errors.New("could not allocate a unique site ID")
}

func (s *Server) pinMAC(id, pin string) string {
	mac := hmac.New(sha256.New, s.secret)
	_, _ = io.WriteString(mac, "pin\x00"+id+"\x00"+pin)
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (s *Server) setSiteSession(w http.ResponseWriter, r *http.Request, id string) {
	expires := time.Now().Add(sessionLifetime)
	expiry := strconv.FormatInt(expires.Unix(), 10)
	mac := hmac.New(sha256.New, s.secret)
	_, _ = io.WriteString(mac, "session\x00"+id+"\x00"+expiry)
	value := expiry + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	secure := r.TLS != nil || (s.publicURL != nil && s.publicURL.Scheme == "https")
	http.SetCookie(w, &http.Cookie{
		Name:     "atml_access",
		Value:    value,
		Path:     "/s/" + id + "/",
		Expires:  expires,
		MaxAge:   int(sessionLifetime.Seconds()),
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteStrictMode,
	})
}

func (s *Server) hasSiteSession(r *http.Request, id string) bool {
	cookie, err := r.Cookie("atml_access")
	if err != nil {
		return false
	}
	parts := strings.Split(cookie.Value, ".")
	if len(parts) != 2 {
		return false
	}
	expires, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || time.Now().Unix() >= expires {
		return false
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, s.secret)
	_, _ = io.WriteString(mac, "session\x00"+id+"\x00"+parts[0])
	return hmac.Equal(signature, mac.Sum(nil))
}

func (s *Server) loadMetadata(id string) (siteMetadata, error) {
	file, err := os.Open(filepath.Join(s.sitesDir, id, "site.json"))
	if err != nil {
		return siteMetadata{}, err
	}
	defer file.Close()
	var metadata siteMetadata
	if err := json.NewDecoder(io.LimitReader(file, 64<<10)).Decode(&metadata); err != nil {
		return siteMetadata{}, err
	}
	if metadata.ID != id || metadata.PINMAC == "" {
		return siteMetadata{}, errors.New("invalid site metadata")
	}
	return metadata, nil
}

func (s *Server) siteURL(r *http.Request, id string) string {
	if s.publicURL != nil {
		copyURL := *s.publicURL
		copyURL.Path = strings.TrimRight(copyURL.Path, "/") + "/s/" + id + "/"
		return copyURL.String()
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return (&url.URL{Scheme: scheme, Host: r.Host, Path: "/s/" + id + "/"}).String()
}

func extractArchive(body io.Reader, destination string, maxBytes int64, maxFiles int) (int, int64, error) {
	gz, err := gzip.NewReader(body)
	if err != nil {
		return 0, 0, errors.New("body is not a valid gzip stream")
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	seen := make(map[string]struct{})
	var total int64
	files := 0
	entries := 0
	for {
		header, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return 0, 0, fmt.Errorf("read tar archive: %w", err)
		}
		entries++
		if entries > maxFiles {
			return 0, 0, fmt.Errorf("archive exceeds the %d-entry limit", maxFiles)
		}
		name, err := safeArchiveName(header.Name)
		if err != nil {
			return 0, 0, err
		}
		if _, exists := seen[name]; exists {
			return 0, 0, fmt.Errorf("duplicate archive entry %q", name)
		}
		seen[name] = struct{}{}
		target := filepath.Join(destination, filepath.FromSlash(name))
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return 0, 0, fmt.Errorf("create directory %q: %w", name, err)
			}
		case tar.TypeReg, tar.TypeRegA:
			files++
			if files > maxFiles {
				return 0, 0, fmt.Errorf("archive exceeds the %d-file limit", maxFiles)
			}
			if header.Size < 0 || header.Size > maxBytes-total {
				return 0, 0, fmt.Errorf("archive exceeds the %d-byte expanded-size limit", maxBytes)
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return 0, 0, fmt.Errorf("create parent for %q: %w", name, err)
			}
			file, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
			if err != nil {
				return 0, 0, fmt.Errorf("create file %q: %w", name, err)
			}
			written, copyErr := io.CopyN(file, tr, header.Size)
			closeErr := file.Close()
			if copyErr != nil {
				return 0, 0, fmt.Errorf("extract file %q: %w", name, copyErr)
			}
			if closeErr != nil {
				return 0, 0, fmt.Errorf("close file %q: %w", name, closeErr)
			}
			total += written
		default:
			return 0, 0, fmt.Errorf("unsupported archive entry type for %q", name)
		}
	}
	if files == 0 {
		return 0, 0, errors.New("archive contains no files")
	}
	return files, total, nil
}

func safeArchiveName(name string) (string, error) {
	if name == "" || strings.ContainsRune(name, '\x00') || strings.Contains(name, "\\") || path.IsAbs(name) {
		return "", fmt.Errorf("unsafe archive path %q", name)
	}
	clean := path.Clean(name)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("unsafe archive path %q", name)
	}
	firstSegment := strings.SplitN(clean, "/", 2)[0]
	if strings.Contains(firstSegment, ":") {
		return "", fmt.Errorf("unsafe archive path %q", name)
	}
	return clean, nil
}

func validateTitle(value string) (string, error) {
	value = strings.TrimSpace(value)
	if len([]rune(value)) > 200 {
		return "", errors.New("X-ATML-Title cannot exceed 200 characters")
	}
	for _, char := range value {
		if char < 0x20 || char == 0x7f {
			return "", errors.New("X-ATML-Title contains control characters")
		}
	}
	return value, nil
}

func validSiteID(id string) bool {
	if len(id) != 16 {
		return false
	}
	for _, char := range id {
		if (char < 'a' || char > 'z') && (char < '2' || char > '7') {
			return false
		}
	}
	return true
}

func loadOrCreateSecret(secretPath string) ([]byte, error) {
	secret, err := os.ReadFile(secretPath)
	if err == nil {
		if len(secret) != 32 {
			return nil, errors.New("server secret has an invalid length")
		}
		return secret, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("read server secret: %w", err)
	}
	secret = make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return nil, fmt.Errorf("generate server secret: %w", err)
	}
	file, err := os.OpenFile(secretPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		return loadOrCreateSecret(secretPath)
	}
	if err != nil {
		return nil, fmt.Errorf("create server secret: %w", err)
	}
	if _, err := file.Write(secret); err != nil {
		file.Close()
		_ = os.Remove(secretPath)
		return nil, fmt.Errorf("write server secret: %w", err)
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(secretPath)
		return nil, fmt.Errorf("close server secret: %w", err)
	}
	return secret, nil
}

func writeMetadata(metadataPath string, metadata siteMetadata) error {
	file, err := os.OpenFile(metadataPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if err := json.NewEncoder(file).Encode(metadata); err != nil {
		file.Close()
		return err
	}
	return file.Close()
}

func (s *Server) remoteIP(r *http.Request) string {
	if s.trustProxy {
		cloudflareIP := strings.TrimSpace(r.Header.Get("CF-Connecting-IP"))
		if net.ParseIP(cloudflareIP) != nil {
			return cloudflareIP
		}
		realIP := strings.TrimSpace(r.Header.Get("X-Real-IP"))
		if net.ParseIP(realIP) != nil {
			return realIP
		}
		forwarded := strings.Split(r.Header.Get("X-Forwarded-For"), ",")
		for index := len(forwarded) - 1; index >= 0; index-- {
			candidate := strings.TrimSpace(forwarded[index])
			if net.ParseIP(candidate) != nil {
				return candidate
			}
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil {
		return host
	}
	return r.RemoteAddr
}

func methodNotAllowed(w http.ResponseWriter, allowed ...string) {
	w.Header().Set("Allow", strings.Join(allowed, ", "))
	http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
}

func writeAPIError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
}

type attemptRecord struct {
	windowStart time.Time
	failures    int
	lockedUntil time.Time
}

type pinAttempts struct {
	mu      sync.Mutex
	records map[string]attemptRecord
}

func newPINAttempts() *pinAttempts {
	return &pinAttempts{records: make(map[string]attemptRecord)}
}

func (a *pinAttempts) allowed(key string, now time.Time) (time.Duration, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	record, exists := a.records[key]
	if !exists {
		return 0, true
	}
	if now.Before(record.lockedUntil) {
		return record.lockedUntil.Sub(now), false
	}
	if now.Sub(record.windowStart) >= failedPINWindow {
		delete(a.records, key)
	}
	return 0, true
}

func (a *pinAttempts) failed(key string, now time.Time) {
	a.mu.Lock()
	defer a.mu.Unlock()
	record := a.records[key]
	if record.windowStart.IsZero() || now.Sub(record.windowStart) >= failedPINWindow {
		record = attemptRecord{windowStart: now}
	}
	record.failures++
	if record.failures >= failedPINLimit {
		record.lockedUntil = now.Add(failedPINWindow)
	}
	a.records[key] = record
	if len(a.records) > 10_000 {
		for recordKey, existing := range a.records {
			if now.After(existing.lockedUntil) && now.Sub(existing.windowStart) >= failedPINWindow {
				delete(a.records, recordKey)
			}
		}
	}
}

func (a *pinAttempts) succeeded(key string) {
	a.mu.Lock()
	delete(a.records, key)
	a.mu.Unlock()
}
