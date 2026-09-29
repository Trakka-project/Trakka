package backup

import (
	"bytes"
	"crypto/rand"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

func mustKey(t *testing.T) Key {
	t.Helper()
	k, err := GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	return k
}

// TestEncryptDecryptRoundTrip covers the chunk-boundary sizes the stream
// format has to get exactly right: empty input, less than one chunk,
// exactly one chunk (the "full final chunk" case), one byte over, and a
// multi-chunk payload — and checks EncryptedSize predicts every one of them
// to the byte, since the WebDAV upload's Content-Length relies on it.
func TestEncryptDecryptRoundTrip(t *testing.T) {
	key := mustKey(t)
	for _, size := range []int{0, 1, 1000, chunkSize - 1, chunkSize, chunkSize + 1, 3*chunkSize + 17, 4 * chunkSize} {
		plain := make([]byte, size)
		if _, err := rand.Read(plain); err != nil {
			t.Fatal(err)
		}
		var enc bytes.Buffer
		if err := Encrypt(&enc, bytes.NewReader(plain), key); err != nil {
			t.Fatalf("size %d: Encrypt: %v", size, err)
		}
		if got, want := int64(enc.Len()), EncryptedSize(int64(size)); got != want {
			t.Fatalf("size %d: encrypted length %d, EncryptedSize says %d", size, got, want)
		}
		var dec bytes.Buffer
		if err := Decrypt(&dec, bytes.NewReader(enc.Bytes()), key); err != nil {
			t.Fatalf("size %d: Decrypt: %v", size, err)
		}
		if !bytes.Equal(dec.Bytes(), plain) {
			t.Fatalf("size %d: round trip mismatch", size)
		}
	}
}

// TestDecryptRejectsTampering flips a ciphertext byte, truncates at a chunk
// boundary, drops the last chunk, and appends trailing data — every one
// must fail authentication rather than yield a silently shortened or
// altered database.
func TestDecryptRejectsTampering(t *testing.T) {
	key := mustKey(t)
	plain := bytes.Repeat([]byte("trakka"), chunkSize) // 6 chunks
	var enc bytes.Buffer
	if err := Encrypt(&enc, bytes.NewReader(plain), key); err != nil {
		t.Fatal(err)
	}
	good := enc.Bytes()
	sealedChunk := chunkSize + tagSize

	cases := map[string][]byte{
		"flipped byte":            append([]byte{}, good...),
		"truncated at boundary":   good[:headerSize+2*sealedChunk],
		"last chunk dropped":      good[:len(good)-sealedChunk],
		"trailing data":           append(append([]byte{}, good...), 0x00),
		"header salt altered":     append([]byte{}, good...),
		"truncated mid-chunk":     good[:headerSize+sealedChunk+100],
		"header only, no content": good[:headerSize],
	}
	cases["flipped byte"][headerSize+500] ^= 0x01
	cases["header salt altered"][saltOffset] ^= 0x01

	for name, data := range cases {
		err := Decrypt(&bytes.Buffer{}, bytes.NewReader(data), key)
		if !errors.Is(err, ErrCorrupt) {
			t.Errorf("%s: got %v, want ErrCorrupt", name, err)
		}
	}
}

// TestDecryptWrongKeyAndGarbage checks the two errors surfaced before any
// decryption happens: a different key (reported with both fingerprints),
// and input that isn't a backup file at all.
func TestDecryptWrongKeyAndGarbage(t *testing.T) {
	key, other := mustKey(t), mustKey(t)
	var enc bytes.Buffer
	if err := Encrypt(&enc, strings.NewReader("hello"), key); err != nil {
		t.Fatal(err)
	}

	var wrong *WrongKeyError
	if err := Decrypt(&bytes.Buffer{}, bytes.NewReader(enc.Bytes()), other); !errors.As(err, &wrong) {
		t.Fatalf("wrong key: got %v, want *WrongKeyError", err)
	}
	if wrong.BackupFingerprint != key.Fingerprint() || wrong.SuppliedFingerprint != other.Fingerprint() {
		t.Fatalf("fingerprints in error = %+v", wrong)
	}

	for _, garbage := range []string{"", "short", "SQLite format 3\x00 and then a lot more bytes to pass the header length check"} {
		if err := Decrypt(&bytes.Buffer{}, strings.NewReader(garbage), key); !errors.Is(err, ErrNotBackup) {
			t.Errorf("garbage %q: got %v, want ErrNotBackup", garbage, err)
		}
	}
}

// TestKeyTextRoundTrip checks the printable form survives what people do to
// it (lowercase, lost dashes, line breaks, the whole .key file pasted in)
// and that the checksum catches a single-character typo.
func TestKeyTextRoundTrip(t *testing.T) {
	key := mustKey(t)
	text := key.String()
	if !strings.HasPrefix(text, "TRAKKA-BK1-") {
		t.Fatalf("unexpected key text %q", text)
	}

	variants := []string{
		text,
		strings.ToLower(text),
		strings.ReplaceAll(text, "-", ""),
		"  " + strings.ReplaceAll(text, "-", "-\n") + "\n",
		KeyFileContent(key, time.Now()),
	}
	for _, v := range variants {
		got, err := ParseKey(v)
		if err != nil {
			t.Fatalf("ParseKey(%q): %v", v, err)
		}
		if got != key {
			t.Fatalf("ParseKey(%q) returned a different key", v)
		}
	}

	// Change one base32 character of the key body.
	typo := []byte(text)
	i := len("TRAKKA-BK1-") + 3
	if typo[i] == 'A' {
		typo[i] = 'B'
	} else {
		typo[i] = 'A'
	}
	if _, err := ParseKey(string(typo)); !errors.Is(err, ErrInvalidKey) {
		t.Fatalf("typo'd key: got %v, want ErrInvalidKey", err)
	}
	// Every other value of the last character — which only carries 2 data
	// bits — must be rejected too, not silently decoded.
	for _, c := range "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567" {
		if byte(c) == text[len(text)-1] {
			continue
		}
		alt := text[:len(text)-1] + string(c)
		if _, err := ParseKey(alt); !errors.Is(err, ErrInvalidKey) {
			t.Fatalf("last character changed to %c: got %v, want ErrInvalidKey", c, err)
		}
	}
	for _, bad := range []string{"", "hello", "TRAKKA-BK1-AAAA", "TRAKKA-BK2-" + text[len("TRAKKA-BK1-"):]} {
		if _, err := ParseKey(bad); !errors.Is(err, ErrInvalidKey) {
			t.Errorf("ParseKey(%q): got %v, want ErrInvalidKey", bad, err)
		}
	}
}

// TestSealSecret round-trips a credential and confirms another key can't
// open it.
func TestSealSecret(t *testing.T) {
	key, other := mustKey(t), mustKey(t)
	sealed, err := sealSecret(key, "app-password-123")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(sealed, "app-password-123") {
		t.Fatal("sealed secret contains the plaintext")
	}
	got, err := openSecret(key, sealed)
	if err != nil || got != "app-password-123" {
		t.Fatalf("openSecret = %q, %v", got, err)
	}
	if _, err := openSecret(other, sealed); !errors.Is(err, ErrSecretUnreadable) {
		t.Fatalf("other key: got %v, want ErrSecretUnreadable", err)
	}
}

// TestKeyStore covers first-use generation (with owner-only permissions),
// stability across reloads, and replace keeping the previous key aside.
func TestKeyStore(t *testing.T) {
	dir := t.TempDir()
	s := &keyStore{path: dir + "/backup.key"}

	if _, ok, err := s.load(); ok || err != nil {
		t.Fatalf("empty store: ok=%v err=%v", ok, err)
	}
	now := time.Date(2026, 9, 29, 3, 0, 0, 0, time.UTC)
	k1, err := s.loadOrCreate(now)
	if err != nil {
		t.Fatal(err)
	}
	k1again, err := s.loadOrCreate(now)
	if err != nil || k1again != k1 {
		t.Fatalf("second loadOrCreate returned a different key (err=%v)", err)
	}
	info, err := os.Stat(s.path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("key file permissions %o, want 600", perm)
	}

	k2 := mustKey(t)
	if err := s.replace(k2, now); err != nil {
		t.Fatal(err)
	}
	got, ok, err := s.load()
	if err != nil || !ok || got != k2 {
		t.Fatalf("after replace: ok=%v err=%v", ok, err)
	}
	aside, err := os.ReadFile(s.path + ".pre-restore-20260929T030000Z")
	if err != nil {
		t.Fatalf("previous key not kept aside: %v", err)
	}
	if prev, err := ParseKey(string(aside)); err != nil || prev != k1 {
		t.Fatalf("kept-aside key doesn't parse back to the original (err=%v)", err)
	}
}
