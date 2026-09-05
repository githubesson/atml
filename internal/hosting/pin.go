package hosting

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

func (s *Server) pinCipher() (cipher.AEAD, error) {
	key := sha256.Sum256(append([]byte("atml-pin-encryption-v1\x00"), s.secret...))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func (s *Server) encryptPIN(id, pin string) (string, error) {
	aead, err := s.pinCipher()
	if err != nil {
		return "", err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(aead.Seal(nonce, nonce, []byte(pin), []byte(id))), nil
}

func (s *Server) decryptPIN(metadata siteMetadata) (string, error) {
	aead, err := s.pinCipher()
	if err != nil {
		return "", err
	}
	data, err := base64.RawURLEncoding.DecodeString(metadata.PINEncrypted)
	if err != nil || len(data) < aead.NonceSize() {
		return "", errors.New("invalid encrypted PIN")
	}
	pin, err := aead.Open(nil, data[:aead.NonceSize()], data[aead.NonceSize():], []byte(metadata.ID))
	if err != nil {
		return "", err
	}
	if len(pin) != 8 || s.pinMAC(metadata.ID, string(pin)) != metadata.PINMAC {
		return "", errors.New("PIN verifier mismatch")
	}
	return string(pin), nil
}

// Caller holds sitesMu exclusively. Replace metadata atomically for legacy sites.
func (s *Server) rememberPIN(metadata siteMetadata, pin string) error {
	encrypted, err := s.encryptPIN(metadata.ID, pin)
	if err != nil {
		return err
	}
	metadata.PINEncrypted = encrypted
	file, err := os.CreateTemp(filepath.Join(s.sitesDir, metadata.ID), ".pin-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if err := json.NewEncoder(file).Encode(metadata); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), filepath.Join(s.sitesDir, metadata.ID, "site.json"))
}

func (s *Server) handleRevealPIN(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, http.MethodGet)
		return
	}
	if !s.authorizedAPIRequest(r) {
		w.Header().Set("WWW-Authenticate", "Bearer")
		writeAPIError(w, http.StatusUnauthorized, "valid bearer token required")
		return
	}
	id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/v1/sites/"), "/pin")
	if !validSiteID(id) {
		http.NotFound(w, r)
		return
	}
	s.sitesMu.RLock()
	metadata, err := s.loadMetadata(id)
	s.sitesMu.RUnlock()
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if metadata.PINEncrypted == "" {
		writeAPIError(w, http.StatusConflict, "Open this page and unlock it with its original PIN once to enable PIN reveal.")
		return
	}
	pin, err := s.decryptPIN(metadata)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "could not read PIN")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(map[string]string{"pin": pin})
}
