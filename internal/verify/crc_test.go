package verify

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"hash/crc32"
	"os"
	"path/filepath"
	"testing"
)

func writeFile(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestParseCRCBareSHA256(t *testing.T) {
	dir := t.TempDir()
	expected := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	p := writeFile(t, dir, "foo.pkg.crc", expected+"\n")
	got, err := ParseCRC(p)
	if err != nil {
		t.Fatal(err)
	}
	if got.Algorithm != AlgSHA256 {
		t.Errorf("alg = %q, want sha256", got.Algorithm)
	}
	if got.Expected != expected {
		t.Errorf("expected = %q", got.Expected)
	}
	if got.For != "foo.pkg" {
		t.Errorf("for = %q, want foo.pkg", got.For)
	}
}

func TestParseCRCBareCRC32(t *testing.T) {
	dir := t.TempDir()
	p := writeFile(t, dir, "x.pkg.crc", "DEADBEEF\n")
	got, err := ParseCRC(p)
	if err != nil {
		t.Fatal(err)
	}
	if got.Algorithm != AlgCRC32 {
		t.Errorf("alg = %q, want crc32", got.Algorithm)
	}
}

func TestParseCRCPrefixedForm(t *testing.T) {
	dir := t.TempDir()
	p := writeFile(t, dir, "y.pkg.crc",
		"sha256  AABBCCDDEEFF00112233445566778899AABBCCDDEEFF00112233445566778899\n")
	got, err := ParseCRC(p)
	if err != nil {
		t.Fatal(err)
	}
	if got.Algorithm != AlgSHA256 {
		t.Errorf("alg = %q", got.Algorithm)
	}
}

func TestParseCRCUnknownFormatReturnsSentinel(t *testing.T) {
	dir := t.TempDir()
	p := writeFile(t, dir, "junk.crc", "this is not a valid checksum file\n")
	_, err := ParseCRC(p)
	if !errors.Is(err, ErrUnknownFormat) {
		t.Errorf("err = %v, want ErrUnknownFormat", err)
	}
}

func TestParseCRCEmptyReturnsSentinel(t *testing.T) {
	dir := t.TempDir()
	p := writeFile(t, dir, "empty.crc", "")
	_, err := ParseCRC(p)
	if !errors.Is(err, ErrUnknownFormat) {
		t.Errorf("err = %v, want ErrUnknownFormat", err)
	}
}

func TestVerifierCRC32(t *testing.T) {
	dir := t.TempDir()
	pkg := writeFile(t, dir, "data.pkg", "hello world")
	sum := crc32.ChecksumIEEE([]byte("hello world"))
	hexSum := func(u uint32) string {
		return string([]byte{
			hexNibble(u >> 28), hexNibble(u >> 24),
			hexNibble(u >> 20), hexNibble(u >> 16),
			hexNibble(u >> 12), hexNibble(u >> 8),
			hexNibble(u >> 4), hexNibble(u),
		})
	}(sum)
	ok, err := DefaultVerifier().Verify(pkg, CRCFile{Algorithm: AlgCRC32, Expected: hexSum})
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Error("crc32 mismatch")
	}
}

func TestVerifierSHA256(t *testing.T) {
	dir := t.TempDir()
	pkg := writeFile(t, dir, "data.pkg", "hello world")
	h := sha256.Sum256([]byte("hello world"))
	ok, err := DefaultVerifier().Verify(pkg, CRCFile{Algorithm: AlgSHA256, Expected: hex.EncodeToString(h[:])})
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Error("sha256 mismatch")
	}
}

func TestVerifierDetectsMismatch(t *testing.T) {
	dir := t.TempDir()
	pkg := writeFile(t, dir, "data.pkg", "hello world")
	ok, err := DefaultVerifier().Verify(pkg, CRCFile{Algorithm: AlgSHA256, Expected: "0000000000000000000000000000000000000000000000000000000000000000"})
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Error("verifier accepted obviously wrong digest")
	}
}

func TestVerifierUnknownAlgorithm(t *testing.T) {
	dir := t.TempDir()
	pkg := writeFile(t, dir, "data.pkg", "x")
	_, err := DefaultVerifier().Verify(pkg, CRCFile{Algorithm: "md5", Expected: "0"})
	if err == nil {
		t.Error("expected unsupported algorithm error")
	}
}

func hexNibble(b uint32) byte {
	b &= 0xf
	if b < 10 {
		return byte('0' + b)
	}
	return byte('a' + b - 10)
}
