package backup

import (
	"bufio"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"strings"
)

// Backup file format (".tkb"), version 1:
//
//	header (52 bytes)
//	  magic        8 bytes  "TRAKKABK"
//	  version      1 byte   0x01
//	  reserved     3 bytes  zero
//	  fingerprint  8 bytes  Key.fingerprintBytes() of the encrypting key
//	  salt        32 bytes  random, fresh per file
//	chunks, each: AES-256-GCM(chunk plaintext) || 16-byte tag
//
// The plaintext (a SQLite snapshot) is split into 64 KiB chunks, sealed one
// at a time, so neither encryption nor decryption ever holds more than one
// chunk in memory — a hard requirement for a server that targets <20 MB of
// RAM while its database may be larger than that. This is the same
// "STREAM" construction age and Tink use:
//
//   - each file gets its own subkey, HKDF-SHA256(key, salt), so chunk
//     nonces can safely be a plain counter without ever repeating a
//     (key, nonce) pair across files;
//   - the nonce is the chunk counter plus a final-chunk flag, so dropping,
//     reordering or duplicating chunks, truncating the file at a chunk
//     boundary, or appending data after the last chunk all fail
//     authentication;
//   - the whole header is every chunk's additional data, so it can't be
//     altered (e.g. swapping in another file's salt) either.
const (
	fileMagic     = "TRAKKABK"
	formatVersion = 1
	headerSize    = 8 + 1 + 3 + 8 + 32
	saltOffset    = 20
	chunkSize     = 64 * 1024
	tagSize       = 16
)

// Errors Decrypt reports for input it refuses — each maps to a distinct,
// actionable message in the admin console.
var (
	ErrNotBackup          = errors.New("not a Trakka backup file")
	ErrUnsupportedVersion = errors.New("unsupported backup file version")
	ErrCorrupt            = errors.New("backup file is corrupted or was modified")
)

// WrongKeyError is returned by Decrypt when the backup's header names a
// different key than the one supplied — checked before any decryption is
// attempted.
type WrongKeyError struct {
	BackupFingerprint   string
	SuppliedFingerprint string
}

func (e *WrongKeyError) Error() string {
	return fmt.Sprintf("backup was encrypted with key %s, not with the supplied key %s", e.BackupFingerprint, e.SuppliedFingerprint)
}

func newStreamAEAD(key Key, salt []byte) (cipher.AEAD, error) {
	subkey, err := hkdf.Key(sha256.New, key[:], salt, "trakka-backup-v1 payload", KeySize)
	if err != nil {
		return nil, fmt.Errorf("deriving file key: %w", err)
	}
	block, err := aes.NewCipher(subkey)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func chunkNonce(counter uint64, final bool) []byte {
	nonce := make([]byte, 12)
	binary.BigEndian.PutUint64(nonce[3:11], counter)
	if final {
		nonce[11] = 1
	}
	return nonce
}

// EncryptedSize is the exact size Encrypt produces for plainSize bytes of
// input — letting a backup be streamed to the WebDAV server with a real
// Content-Length (some servers reject chunked PUTs) without first writing
// the ciphertext to disk.
func EncryptedSize(plainSize int64) int64 {
	chunks := plainSize / chunkSize
	if plainSize%chunkSize != 0 || plainSize == 0 {
		chunks++
	}
	return headerSize + plainSize + chunks*tagSize
}

// Encrypt reads all of src and writes it to dst in the format above.
func Encrypt(dst io.Writer, src io.Reader, key Key) error {
	header := make([]byte, headerSize)
	copy(header, fileMagic)
	header[8] = formatVersion
	fp := key.fingerprintBytes()
	copy(header[12:saltOffset], fp[:])
	if _, err := rand.Read(header[saltOffset:]); err != nil {
		return fmt.Errorf("generating salt: %w", err)
	}
	aead, err := newStreamAEAD(key, header[saltOffset:])
	if err != nil {
		return err
	}
	if _, err := dst.Write(header); err != nil {
		return err
	}

	in := bufio.NewReaderSize(src, chunkSize)
	buf := make([]byte, chunkSize, chunkSize+tagSize)
	for counter := uint64(0); ; counter++ {
		n, err := io.ReadFull(in, buf)
		final := false
		switch {
		case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF):
			// Short (or, for empty input, zero-length) chunk: always the last.
			final = true
		case err != nil:
			return err
		default:
			// A full chunk is the last one only if nothing follows it.
			if _, perr := in.Peek(1); errors.Is(perr, io.EOF) {
				final = true
			} else if perr != nil {
				return perr
			}
		}
		sealed := aead.Seal(buf[:0], chunkNonce(counter, final), buf[:n], header)
		if _, err := dst.Write(sealed); err != nil {
			return err
		}
		if final {
			return nil
		}
		buf = buf[:chunkSize]
	}
}

// Decrypt verifies and decrypts a backup from src into dst. Plaintext is
// written chunk by chunk as each one authenticates, so on error dst may
// hold a partial result that must be discarded — only a nil return means
// the whole file was intact.
func Decrypt(dst io.Writer, src io.Reader, key Key) error {
	header := make([]byte, headerSize)
	if _, err := io.ReadFull(src, header); err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return ErrNotBackup
		}
		return err
	}
	if string(header[:8]) != fileMagic {
		return ErrNotBackup
	}
	if header[8] != formatVersion {
		return fmt.Errorf("%w: %d", ErrUnsupportedVersion, header[8])
	}
	var fileFP [8]byte
	copy(fileFP[:], header[12:saltOffset])
	if fileFP != key.fingerprintBytes() {
		return &WrongKeyError{BackupFingerprint: formatFingerprint(fileFP), SuppliedFingerprint: key.Fingerprint()}
	}
	aead, err := newStreamAEAD(key, header[saltOffset:])
	if err != nil {
		return err
	}

	in := bufio.NewReaderSize(src, chunkSize+tagSize)
	buf := make([]byte, chunkSize+tagSize)
	for counter := uint64(0); ; counter++ {
		n, err := io.ReadFull(in, buf)
		final := false
		switch {
		case errors.Is(err, io.EOF):
			// Ran out of input without ever seeing the final chunk.
			return ErrCorrupt
		case errors.Is(err, io.ErrUnexpectedEOF):
			final = true
		case err != nil:
			return err
		default:
			if _, perr := in.Peek(1); errors.Is(perr, io.EOF) {
				final = true
			} else if perr != nil {
				return perr
			}
		}
		if n < tagSize {
			return ErrCorrupt
		}
		plain, err := aead.Open(buf[:0], chunkNonce(counter, final), buf[:n], header)
		if err != nil {
			return ErrCorrupt
		}
		if _, err := dst.Write(plain); err != nil {
			return err
		}
		if final {
			return nil
		}
	}
}

// sealedSecretPrefix versions sealSecret's output format.
const sealedSecretPrefix = "v1:"

// sealSecret encrypts a small secret (the WebDAV password) under a subkey
// of the backup key before it is written to system_settings, so the
// database file on its own — including the plaintext pre-migration and
// pre-restore copies kept under /data/backups/ — never contains the
// credentials to the off-site copies of itself.
func sealSecret(key Key, plaintext string) (string, error) {
	aead, err := secretAEAD(key)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("generating nonce: %w", err)
	}
	sealed := aead.Seal(nonce, nonce, []byte(plaintext), nil)
	return sealedSecretPrefix + base64.RawStdEncoding.EncodeToString(sealed), nil
}

// ErrSecretUnreadable means a stored secret can't be decrypted with the
// current key file — it was sealed under a key that has since been
// replaced or lost, and must simply be re-entered.
var ErrSecretUnreadable = errors.New("the stored WebDAV password can't be decrypted with the current backup key; enter it again")

func openSecret(key Key, sealed string) (string, error) {
	encoded, ok := strings.CutPrefix(sealed, sealedSecretPrefix)
	if !ok {
		return "", ErrSecretUnreadable
	}
	raw, err := base64.RawStdEncoding.DecodeString(encoded)
	if err != nil {
		return "", ErrSecretUnreadable
	}
	aead, err := secretAEAD(key)
	if err != nil {
		return "", err
	}
	if len(raw) < aead.NonceSize() {
		return "", ErrSecretUnreadable
	}
	plain, err := aead.Open(nil, raw[:aead.NonceSize()], raw[aead.NonceSize():], nil)
	if err != nil {
		return "", ErrSecretUnreadable
	}
	return string(plain), nil
}

func secretAEAD(key Key) (cipher.AEAD, error) {
	subkey, err := hkdf.Key(sha256.New, key[:], nil, "trakka-backup-v1 credential", KeySize)
	if err != nil {
		return nil, fmt.Errorf("deriving credential key: %w", err)
	}
	block, err := aes.NewCipher(subkey)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}
