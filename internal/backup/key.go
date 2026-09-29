package backup

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// KeySize is the length of a backup encryption key: a raw AES-256 key.
const KeySize = 32

// Key is an instance's backup encryption key. Generated once, the first
// time backups are configured (or the key is first exported), and stored
// in its own file next to the database — deliberately not inside the
// database itself, so it never travels inside the backups it protects.
type Key [KeySize]byte

// ErrInvalidKey is returned by ParseKey for anything that isn't a
// well-formed key, including one whose built-in checksum doesn't match
// (almost always a typo in a hand-copied key).
var ErrInvalidKey = errors.New("invalid backup key")

// keyTextPrefix tags the printable form of a key (see Key.String) so a
// pasted value is recognizably a Trakka backup key, and so a future key
// format can be told apart from this one.
const keyTextPrefix = "TRAKKA-BK1"

// keyChecksumSize is how many bytes of SHA-256(key) are appended to the
// printable form — enough to catch essentially every typo in a hand-copied
// key before it is mistaken for "the wrong key".
const keyChecksumSize = 2

var keyEncoding = base32.StdEncoding.WithPadding(base32.NoPadding)

// GenerateKey returns a fresh random key from crypto/rand.
func GenerateKey() (Key, error) {
	var k Key
	if _, err := rand.Read(k[:]); err != nil {
		return Key{}, fmt.Errorf("generating backup key: %w", err)
	}
	return k, nil
}

// String renders k in its printable, hand-copyable form:
// "TRAKKA-BK1-" followed by the base32 encoding (A–Z, 2–7) of the key plus
// a 2-byte checksum, in dash-separated groups of five characters.
func (k Key) String() string {
	sum := sha256.Sum256(k[:])
	raw := append(k[:len(k):len(k)], sum[:keyChecksumSize]...)
	encoded := keyEncoding.EncodeToString(raw)

	var b strings.Builder
	b.WriteString(keyTextPrefix)
	for i := 0; i < len(encoded); i += 5 {
		b.WriteByte('-')
		b.WriteString(encoded[i:min(i+5, len(encoded))])
	}
	return b.String()
}

// ParseKey accepts a key in its printable form (see String), tolerating
// everything a person or a text editor might do to it on the way back:
// surrounding whitespace, line breaks, lowercase, missing or extra dashes,
// and the "#"-prefixed comment lines of a downloaded .key file (see
// KeyFileContent) — so the whole file's content can be pasted as-is.
func ParseKey(s string) (Key, error) {
	var b strings.Builder
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		for _, r := range strings.ToUpper(line) {
			if r == '-' || r == ' ' || r == '\t' || r == '\r' {
				continue
			}
			b.WriteRune(r)
		}
	}
	compact, ok := strings.CutPrefix(b.String(), strings.ReplaceAll(keyTextPrefix, "-", ""))
	if !ok {
		return Key{}, ErrInvalidKey
	}
	raw, err := keyEncoding.DecodeString(compact)
	if err != nil || len(raw) != KeySize+keyChecksumSize {
		return Key{}, ErrInvalidKey
	}
	// The last base32 character carries only 2 data bits; the decoder
	// ignores its 3 padding bits, so a typo there could otherwise decode
	// silently. Requiring the canonical encoding rejects it.
	if keyEncoding.EncodeToString(raw) != compact {
		return Key{}, ErrInvalidKey
	}

	var k Key
	copy(k[:], raw[:KeySize])
	sum := sha256.Sum256(k[:])
	if string(sum[:keyChecksumSize]) != string(raw[KeySize:]) {
		return Key{}, ErrInvalidKey
	}
	return k, nil
}

// fingerprintBytes identifies a key without revealing it: a truncated,
// domain-separated SHA-256. Written into every backup's header so a restore
// attempt with the wrong key fails immediately with a clear "this backup
// needs key X, you gave key Y" rather than an opaque decryption error.
func (k Key) fingerprintBytes() [8]byte {
	sum := sha256.Sum256(append([]byte("trakka-backup-fingerprint-v1:"), k[:]...))
	var fp [8]byte
	copy(fp[:], sum[:8])
	return fp
}

// Fingerprint is fingerprintBytes in display form, e.g. "3f2a-91c0-7b4e-d215".
func (k Key) Fingerprint() string {
	fp := k.fingerprintBytes()
	return formatFingerprint(fp)
}

func formatFingerprint(fp [8]byte) string {
	h := hex.EncodeToString(fp[:])
	return h[0:4] + "-" + h[4:8] + "-" + h[8:12] + "-" + h[12:16]
}

// KeyFileContent is what the admin console's "download the key" button
// saves, and also exactly what is stored on the server's own disk: the
// printable key plus comment lines ParseKey skips, so the same file can be
// pasted or uploaded back verbatim at restore time.
func KeyFileContent(k Key, created time.Time) string {
	return strings.Join([]string{
		"# Trakka backup encryption key",
		"# Fingerprint: " + k.Fingerprint(),
		"# Created: " + created.UTC().Format(time.RFC3339),
		"#",
		"# Keep this file somewhere safe and OFF the Trakka server (a password",
		"# manager, an offline copy). Without it, none of this instance's backups",
		"# can ever be decrypted or restored.",
		k.String(),
		"",
	}, "\n")
}

// KeyFileName is the instance key file's name, in the same directory as
// the database (DB_PATH).
const KeyFileName = "backup.key"

// InstallKey writes k as the instance key file in dir if none exists there
// yet, reporting whether it did — used by `trakka -decrypt-backup`, so that
// a database decrypted offline for disaster recovery comes up with the key
// its own backup settings (and encrypted WebDAV password) were sealed
// with. An existing key file is never overwritten.
func InstallKey(dir string, k Key, now time.Time) (bool, error) {
	s := &keyStore{path: filepath.Join(dir, KeyFileName)}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok, err := s.loadLocked(); err != nil || ok {
		return false, err
	}
	return true, s.writeLocked(k, now)
}

// keyStore reads and writes the instance's key file. The mutex serializes
// the "load, or generate and persist" sequence so two concurrent callers
// (e.g. a config save and a key export) can never each generate a
// different key.
type keyStore struct {
	path string
	mu   sync.Mutex
}

// load returns the stored key, or ok=false if none has been generated yet.
func (s *keyStore) load() (Key, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadLocked()
}

func (s *keyStore) loadLocked() (Key, bool, error) {
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return Key{}, false, nil
	}
	if err != nil {
		return Key{}, false, fmt.Errorf("reading backup key file: %w", err)
	}
	k, err := ParseKey(string(data))
	if err != nil {
		return Key{}, false, fmt.Errorf("backup key file %s is unreadable: %w", s.path, err)
	}
	return k, true, nil
}

// loadOrCreate returns the stored key, generating and persisting a new one
// first if none exists yet.
func (s *keyStore) loadOrCreate(now time.Time) (Key, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	k, ok, err := s.loadLocked()
	if err != nil || ok {
		return k, err
	}
	k, err = GenerateKey()
	if err != nil {
		return Key{}, err
	}
	if err := s.writeLocked(k, now); err != nil {
		return Key{}, err
	}
	return k, nil
}

// replace installs k as the instance key (after a restore made with a
// different key than the current one), first moving any existing key file
// aside to <path>.pre-restore-<timestamp> rather than overwriting it — old
// backups encrypted with that previous key stay decryptable by whoever
// retrieves it from the volume.
func (s *keyStore) replace(k Key, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := os.Stat(s.path); err == nil {
		aside := fmt.Sprintf("%s.pre-restore-%s", s.path, now.UTC().Format("20060102T150405Z"))
		if err := os.Rename(s.path, aside); err != nil {
			return fmt.Errorf("moving previous backup key aside: %w", err)
		}
	}
	return s.writeLocked(k, now)
}

// writeLocked persists k with owner-only permissions, via a temporary file
// renamed into place so a crash mid-write can never leave a truncated key.
func (s *keyStore) writeLocked(k Key, now time.Time) error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o750); err != nil {
		return fmt.Errorf("creating backup key directory: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".backup-key-*")
	if err != nil {
		return fmt.Errorf("creating backup key file: %w", err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }() // no-op once renamed
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("restricting backup key file permissions: %w", err)
	}
	if _, err := tmp.WriteString(KeyFileContent(k, now)); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("writing backup key file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("syncing backup key file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("closing backup key file: %w", err)
	}
	if err := os.Rename(tmp.Name(), s.path); err != nil {
		return fmt.Errorf("installing backup key file: %w", err)
	}
	return nil
}
