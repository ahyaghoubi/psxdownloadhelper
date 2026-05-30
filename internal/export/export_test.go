package export

import (
	"bytes"
	"errors"
	"testing"
)

type errWriter struct{}

func (errWriter) Write(_ []byte) (int, error) { return 0, errors.New("forced") }

func TestWriteTxt_Basic(t *testing.T) {
	var buf bytes.Buffer
	urls := []string{
		"http://gs2.ww.prod.dl.playstation.net/gs2/appkgo/UP1234-CUSA12345_00/x/x_0.pkg?downloadId=abc",
		"http://gst.prod.dl.playstation.net/gst/prod/00/PPSA01234/app/pkg/x_0.pkg",
	}
	if err := WriteTxt(&buf, urls); err != nil {
		t.Fatalf("WriteTxt: %v", err)
	}
	want := urls[0] + "\n" + urls[1] + "\n"
	if buf.String() != want {
		t.Errorf("output =\n%q\nwant\n%q", buf.String(), want)
	}
}

func TestWriteTxt_EmptyList(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteTxt(&buf, nil); err != nil {
		t.Fatalf("WriteTxt: %v", err)
	}
	if buf.Len() != 0 {
		t.Errorf("empty list should produce zero bytes, got %d", buf.Len())
	}
}

func TestWriteTxt_SkipsBlankEntries(t *testing.T) {
	var buf bytes.Buffer
	urls := []string{
		"http://example.com/a.pkg",
		"",
		"   ",
		"http://example.com/b.pkg",
	}
	if err := WriteTxt(&buf, urls); err != nil {
		t.Fatalf("WriteTxt: %v", err)
	}
	want := "http://example.com/a.pkg\nhttp://example.com/b.pkg\n"
	if buf.String() != want {
		t.Errorf("output =\n%q\nwant\n%q", buf.String(), want)
	}
}

func TestWriteTxt_PreservesQueryString(t *testing.T) {
	var buf bytes.Buffer
	full := "http://gs2.ww.prod.dl.playstation.net/x/y_0.pkg?downloadId=DEAD-BEEF&du=1&q=z"
	if err := WriteTxt(&buf, []string{full}); err != nil {
		t.Fatalf("WriteTxt: %v", err)
	}
	if buf.String() != full+"\n" {
		t.Errorf("query string not preserved: %q", buf.String())
	}
}

func TestWriteTxt_NilWriter(t *testing.T) {
	if err := WriteTxt(nil, []string{"x"}); err == nil {
		t.Error("expected error for nil writer")
	}
}

func TestWriteTxt_WriterError(t *testing.T) {
	if err := WriteTxt(errWriter{}, []string{"http://example.com/a"}); err == nil {
		t.Error("expected propagated writer error")
	}
}

func TestWriteTxt_TrimsWhitespaceAroundURL(t *testing.T) {
	var buf bytes.Buffer
	urls := []string{"  http://example.com/a.pkg  ", "\thttp://example.com/b.pkg\n"}
	if err := WriteTxt(&buf, urls); err != nil {
		t.Fatalf("WriteTxt: %v", err)
	}
	want := "http://example.com/a.pkg\nhttp://example.com/b.pkg\n"
	if buf.String() != want {
		t.Errorf("output =\n%q\nwant\n%q", buf.String(), want)
	}
}
