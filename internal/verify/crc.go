// Package verify provides integrity verification for files in the
// psxdh library. The current Phase-1 scope is the framework: a
// Verifier interface, library-watcher integration, and CRC parser
// stubs that fail-safe ("unknown format; skipping") until Phase 0
// captures real PS5 .crc samples and we lock down the schema.
//
// The Sony PS5 CDN serves a sidecar .crc file alongside each chunk;
// the format is documented neither in psdevwiki nor in any of the
// FOSS forks we surveyed (see docs/research.md). We refuse to invent
// it from thin air — a wrong parser would either fail every install
// or silently pass corrupt downloads. Instead, Verify hooks into the
// library watcher so that when a real .crc appears next to a PKG,
// the framework is ready to fire as soon as ParseCRC learns the
// format.
package verify

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// ErrUnknownFormat is returned by ParseCRC when the sidecar's contents
// don't match any algorithm we recognise. Watcher integration treats
// this as a warning, not an install-blocker.
var ErrUnknownFormat = errors.New("verify: unknown .crc format")

// Algorithm names the integrity scheme.
type Algorithm string

const (
	AlgCRC32  Algorithm = "crc32"
	AlgSHA256 Algorithm = "sha256"
)

// CRCFile is the parsed shape of a .crc sidecar.
type CRCFile struct {
	// For is the basename of the PKG this CRC covers, or empty if the
	// sidecar's name carries the association implicitly (e.g. foo.pkg
	// + foo.pkg.crc).
	For string
	// Algorithm names the digest scheme.
	Algorithm Algorithm
	// Expected is the hex-encoded digest the PKG should produce.
	Expected string
}

// Verifier checks a PKG file against a parsed CRC sidecar.
type Verifier interface {
	Verify(pkgPath string, crc CRCFile) (ok bool, err error)
}

// DefaultVerifier returns a Verifier that supports CRC32 (IEEE) and
// SHA-256. It reads the PKG file once, streaming through the chosen
// hash.
func DefaultVerifier() Verifier { return defaultVerifier{} }

type defaultVerifier struct{}

func (defaultVerifier) Verify(pkgPath string, crc CRCFile) (bool, error) {
	f, err := os.Open(pkgPath)
	if err != nil {
		return false, fmt.Errorf("verify: open %s: %w", pkgPath, err)
	}
	defer f.Close()

	switch crc.Algorithm {
	case AlgCRC32:
		h := crc32.NewIEEE()
		if _, err := io.Copy(h, f); err != nil {
			return false, err
		}
		got := fmt.Sprintf("%08x", h.Sum32())
		return strings.EqualFold(got, crc.Expected), nil
	case AlgSHA256:
		h := sha256.New()
		if _, err := io.Copy(h, f); err != nil {
			return false, err
		}
		got := hex.EncodeToString(h.Sum(nil))
		return strings.EqualFold(got, crc.Expected), nil
	default:
		return false, fmt.Errorf("verify: unsupported algorithm %q", crc.Algorithm)
	}
}

// ParseCRC reads a .crc sidecar from disk. The implementation
// currently recognises two community-described shapes:
//
//   - Plain hex (8 chars → CRC32, 64 chars → SHA-256). This is the
//     format psxdh's own `psxdh verify --crc32 …` will emit when we
//     enable it, and the simplest thing some PS4 dump tools also
//     produce.
//   - "<alg> <hex>" on a single line (e.g. "sha256 0123…").
//
// Anything else returns ErrUnknownFormat. Once Phase 0 captures a
// real PS5 sidecar this function can be extended; callers must
// treat ErrUnknownFormat as a non-fatal signal to skip verification.
func ParseCRC(path string) (CRCFile, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return CRCFile{}, fmt.Errorf("verify: read %s: %w", path, err)
	}
	s := strings.TrimSpace(string(body))
	if s == "" {
		return CRCFile{}, ErrUnknownFormat
	}
	target := pkgNeighbour(path)

	// "<alg> <hex>" form.
	if fields := strings.Fields(s); len(fields) == 2 {
		alg := strings.ToLower(fields[0])
		digest := strings.TrimSpace(fields[1])
		switch alg {
		case "crc32":
			if isHex(digest, 8) {
				return CRCFile{For: target, Algorithm: AlgCRC32, Expected: digest}, nil
			}
		case "sha256", "sha-256":
			if isHex(digest, 64) {
				return CRCFile{For: target, Algorithm: AlgSHA256, Expected: digest}, nil
			}
		}
	}

	// Bare hex form.
	digest := strings.TrimPrefix(strings.ToLower(s), "0x")
	digest = strings.TrimSpace(digest)
	switch len(digest) {
	case 8:
		if isHex(digest, 8) {
			return CRCFile{For: target, Algorithm: AlgCRC32, Expected: digest}, nil
		}
	case 64:
		if isHex(digest, 64) {
			return CRCFile{For: target, Algorithm: AlgSHA256, Expected: digest}, nil
		}
	}
	return CRCFile{}, ErrUnknownFormat
}

// pkgNeighbour returns the basename of the PKG that a "foo.pkg.crc"
// sidecar is presumed to cover ("foo.pkg"). Returns the empty string
// if the input doesn't end in ".crc".
func pkgNeighbour(crcPath string) string {
	base := filepath.Base(crcPath)
	if !strings.HasSuffix(strings.ToLower(base), ".crc") {
		return ""
	}
	return strings.TrimSuffix(base, filepath.Ext(base))
}

func isHex(s string, n int) bool {
	if len(s) != n {
		return false
	}
	for _, c := range s {
		switch {
		case c >= '0' && c <= '9',
			c >= 'a' && c <= 'f',
			c >= 'A' && c <= 'F':
		default:
			return false
		}
	}
	return true
}
