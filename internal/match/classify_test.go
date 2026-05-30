package match

import (
	"net/url"
	"os"
	"path/filepath"
	"testing"
)

func mustParse(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse %q: %v", raw, err)
	}
	return u
}

func TestClassify(t *testing.T) {
	rs, err := LoadDefaults(true, true)
	if err != nil {
		t.Fatalf("LoadDefaults: %v", err)
	}
	cases := []struct {
		name      string
		raw       string
		wantKind  Kind
		wantTitle string
		wantPart  int
	}{
		// PS4
		{
			name:      "ps4 base chunk 0",
			raw:       "http://gs2.ww.prod.dl.playstation.net/gs2/appkgo/UP1234-CUSA12345_00-FAKEBASE0000000/8/f_60d5/UP1234-CUSA12345_00-FAKEBASEPKG-A0100-V0100_0.pkg?downloadId=abc&du=1",
			wantKind:  KindPKGBase,
			wantTitle: "UP1234-CUSA12345",
			wantPart:  0,
		},
		{
			name:      "ps4 base chunk 3",
			raw:       "http://gs2.ww.prod.dl.playstation.net/gs2/appkgo/UP1234-CUSA12345_00-FAKEBASE0000000/8/f_60d5/UP1234-CUSA12345_00-FAKEBASEPKG-A0100-V0100_3.pkg",
			wantKind:  KindPKGBase,
			wantTitle: "UP1234-CUSA12345",
			wantPart:  3,
		},
		{
			name:      "ps4 patch",
			raw:       "http://gs2.ww.prod.dl.playstation.net/gs2/ppkgo/UP1234-CUSA12345_00-FAKEPATCH000000/8/f_abc/UP1234-CUSA12345_00-FAKEPATCHPKG-A0101-V0101_0.pkg",
			wantKind:  KindPKGPatch,
			wantTitle: "UP1234-CUSA12345",
			wantPart:  0,
		},
		{
			name:      "ps4 delta under ppkgo",
			raw:       "http://gs2.ww.prod.dl.playstation.net/gs2/ppkgo/UP1234-CUSA12345_00-FAKEPATCH000000/8/f_abc/UP1234-CUSA12345_00-FAKEPATCHPKG-A0101-V0101-DP.pkg",
			wantKind:  KindPKGDelta,
			wantTitle: "UP1234-CUSA12345",
			wantPart:  -1,
		},
		{
			name:      "ps4 manifest json under appkgo",
			raw:       "http://gs2.ww.prod.dl.playstation.net/gs2/appkgo/UP1234-CUSA12345_00-FAKEBASE0000000/8/f_60d5/manifest.json",
			wantKind:  KindManifestJSON,
			wantTitle: "UP1234-CUSA12345",
			wantPart:  -1,
		},
		{
			name:      "ps4 manifest json on secured host",
			raw:       "http://gs2-sec.ww.prod.dl.playstation.net/gs2/ppkgo/UP1234-CUSA12345_00-FAKEPATCH000000/8/f_abc/manifest.json",
			wantKind:  KindManifestJSON,
			wantTitle: "UP1234-CUSA12345",
			wantPart:  -1,
		},

		// PS5
		{
			name:      "ps5 app pkg chunk 0",
			raw:       "http://gst.prod.dl.playstation.net/gst/prod/00/PPSA01234_00-FAKEBASE000000_0/app/pkg/PPSA01234_00-FAKEBASEPKG-A0100_0.pkg?q=abc",
			wantKind:  KindPKGApp,
			wantTitle: "PPSA01234",
			wantPart:  0,
		},
		{
			name:      "ps5 app pkg chunk 22",
			raw:       "http://gst.prod.dl.playstation.net/gst/prod/00/PPSA01234_00-FAKEBASE000000_0/app/pkg/PPSA01234_00-FAKEBASEPKG-A0100_22.pkg",
			wantKind:  KindPKGApp,
			wantTitle: "PPSA01234",
			wantPart:  22,
		},
		{
			name:      "ps5 sc pkg",
			raw:       "http://gst.prod.dl.playstation.net/gst/prod/00/PPSA01234_00-FAKEBASE000000_0/app/pkg/PPSA01234_00-FAKEBASEPKG_sc.pkg",
			wantKind:  KindPKGSC,
			wantTitle: "PPSA01234",
			wantPart:  -1,
		},
		{
			name:      "ps5 delta DP pkg",
			raw:       "http://gst.prod.dl.playstation.net/gst/prod/00/PPSA01234_00-FAKEPATCH00000_0/app/pkg/PPSA01234_00-FAKEPATCHPKG-A0101-DP.pkg",
			wantKind:  KindPKGDelta,
			wantTitle: "PPSA01234",
			wantPart:  -1,
		},
		{
			name:      "ps5 crc sidecar",
			raw:       "http://gst.prod.dl.playstation.net/gst/prod/00/PPSA01234_00-FAKEBASE000000_0/app/pkg/PPSA01234_00-FAKEBASEPKG-A0100_0.crc",
			wantKind:  KindCRC,
			wantTitle: "PPSA01234",
			wantPart:  0,
		},
		{
			name:      "ps5 version xml",
			raw:       "https://sgst.prod.dl.playstation.net/sgst/prod/np/PPSA01234_00/A0100-version.xml",
			wantKind:  KindManifestXML,
			wantTitle: "PPSA01234",
			wantPart:  -1,
		},
		{
			name:      "ps5 app info json",
			raw:       "https://sgst.prod.dl.playstation.net/sgst/prod/00/np/PPSA01234_00/app/info/PPSA01234_00-FAKE.json",
			wantKind:  KindManifestJSON,
			wantTitle: "PPSA01234",
			wantPart:  -1,
		},

		// Negative / unknown
		{
			name:      "unrelated host returns unknown",
			raw:       "http://example.com/some/file.pkg",
			wantKind:  KindUnknown,
			wantTitle: "",
			wantPart:  -1,
		},
		{
			name:      "playstation host but unknown path",
			raw:       "http://gs2.ww.prod.dl.playstation.net/random/path/foo.bin",
			wantKind:  KindUnknown,
			wantTitle: "",
			wantPart:  -1,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			u := mustParse(t, tc.raw)
			gotKind, gotHint := rs.Classify(u)
			if gotKind != tc.wantKind {
				t.Errorf("kind = %q, want %q", gotKind, tc.wantKind)
			}
			if gotHint.TitleHint != tc.wantTitle {
				t.Errorf("title hint = %q, want %q", gotHint.TitleHint, tc.wantTitle)
			}
			if gotHint.PartIndex != tc.wantPart {
				t.Errorf("part index = %d, want %d", gotHint.PartIndex, tc.wantPart)
			}
		})
	}
}

func TestClassifyNil(t *testing.T) {
	rs, err := LoadDefaults(true, true)
	if err != nil {
		t.Fatalf("LoadDefaults: %v", err)
	}
	kind, hint := rs.Classify(nil)
	if kind != KindUnknown {
		t.Errorf("nil URL should classify as unknown, got %q", kind)
	}
	if hint.PartIndex != -1 {
		t.Errorf("nil URL hint should have PartIndex -1, got %d", hint.PartIndex)
	}
}

func TestLoadDefaultsPS4Only(t *testing.T) {
	rs, err := LoadDefaults(true, false)
	if err != nil {
		t.Fatalf("LoadDefaults ps4-only: %v", err)
	}
	ps5URL := mustParse(t, "http://gst.prod.dl.playstation.net/gst/prod/00/PPSA01234_00-X_0/app/pkg/x_0.pkg")
	kind, _ := rs.Classify(ps5URL)
	if kind != KindUnknown {
		t.Errorf("ps5 URL with ps4-only rules should be unknown, got %q", kind)
	}
}

func TestLoadDefaultsPS5Only(t *testing.T) {
	rs, err := LoadDefaults(false, true)
	if err != nil {
		t.Fatalf("LoadDefaults ps5-only: %v", err)
	}
	ps4URL := mustParse(t, "http://gs2.ww.prod.dl.playstation.net/gs2/appkgo/UP1234-CUSA12345_00-X/8/f_x/x_0.pkg")
	kind, _ := rs.Classify(ps4URL)
	if kind != KindUnknown {
		t.Errorf("ps4 URL with ps5-only rules should be unknown, got %q", kind)
	}
}

func TestLoadDefaultsBothDisabled(t *testing.T) {
	rs, err := LoadDefaults(false, false)
	if err != nil {
		t.Fatalf("LoadDefaults both disabled: %v", err)
	}
	if rs.Len() != 0 {
		t.Errorf("Len = %d, want 0", rs.Len())
	}
}

func TestRuleCountSane(t *testing.T) {
	rs, err := LoadDefaults(true, true)
	if err != nil {
		t.Fatalf("LoadDefaults: %v", err)
	}
	if rs.Len() < 8 {
		t.Errorf("Len = %d; expected at least 8 default rules (5 PS4 + 6 PS5)", rs.Len())
	}
}

func TestLoadOverride(t *testing.T) {
	dir := t.TempDir()
	body := `platform: custom
rules:
  - kind: pkg-app
    host_suffix: example.com
    path_regex: /custom/.*\.pkg$
`
	if err := os.WriteFile(filepath.Join(dir, "custom.yaml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	rs, err := LoadOverride(dir)
	if err != nil {
		t.Fatalf("LoadOverride: %v", err)
	}
	if rs.Len() != 1 {
		t.Fatalf("Len = %d, want 1", rs.Len())
	}
	u := mustParse(t, "http://example.com/custom/foo.pkg")
	kind, _ := rs.Classify(u)
	if kind != KindPKGApp {
		t.Errorf("kind = %q, want pkg-app", kind)
	}
}

func TestLoadOverrideRejectsBadRegex(t *testing.T) {
	dir := t.TempDir()
	body := `platform: broken
rules:
  - kind: pkg-app
    host_suffix: example.com
    path_regex: "(unbalanced"
`
	if err := os.WriteFile(filepath.Join(dir, "bad.yaml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadOverride(dir); err == nil {
		t.Error("expected error for invalid regex")
	}
}

func TestHostSuffixSubdomainMatch(t *testing.T) {
	rs, err := LoadDefaults(false, true)
	if err != nil {
		t.Fatalf("LoadDefaults: %v", err)
	}
	// prod.dl.playstation.net is the suffix on the pkg-delta rule.
	u := mustParse(t, "http://anything.prod.dl.playstation.net/x/y-DP.pkg")
	kind, _ := rs.Classify(u)
	if kind != KindPKGDelta {
		t.Errorf("subdomain of suffix should match; got kind %q", kind)
	}
}

func TestHostSuffixNoCrossDomain(t *testing.T) {
	rs, err := LoadDefaults(false, true)
	if err != nil {
		t.Fatalf("LoadDefaults: %v", err)
	}
	// evil.com ends in nothing relevant; must not match.
	u := mustParse(t, "http://evil.com/x/y-DP.pkg")
	kind, _ := rs.Classify(u)
	if kind != KindUnknown {
		t.Errorf("unrelated host must not match; got kind %q", kind)
	}
}
