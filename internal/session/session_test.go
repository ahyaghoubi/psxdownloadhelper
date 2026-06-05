package session

import (
	"net/url"
	"testing"
	"time"

	"github.com/ahyaghoubi/psxdownloadhelper/internal/capture"
	"github.com/ahyaghoubi/psxdownloadhelper/internal/library"
	"github.com/ahyaghoubi/psxdownloadhelper/internal/match"
)

// stubLib reports local/verified state for a fixed set of basenames.
type stubLib struct {
	local map[string]library.VerifyState
}

func (s stubLib) Resolve(u *url.URL) (string, bool) {
	base := u.Path[len(u.Path)-len(lastSeg(u.Path)):]
	if _, ok := s.local[lastSeg(u.Path)]; ok {
		return "/lib/" + base, true
	}
	return "", false
}
func (s stubLib) VerifyStateOf(path string) library.VerifyState {
	return s.local[lastSeg(path)]
}
func lastSeg(p string) string {
	for i := len(p) - 1; i >= 0; i-- {
		if p[i] == '/' {
			return p[i+1:]
		}
	}
	return p
}

func ev(rawURL, title string, part int) capture.Event {
	u, _ := url.Parse(rawURL)
	return capture.Event{URL: u, Method: "GET", Kind: match.KindPKGApp, Hint: match.Hint{TitleHint: title, PartIndex: part}, Time: time.Now()}
}

func TestSnapshotGroupsAndOrders(t *testing.T) {
	st := New(nil)
	st.Record(ev("http://cdn/CUSA1_1.pkg", "CUSA1", 1))
	st.Record(ev("http://cdn/CUSA1_0.pkg", "CUSA1", 0))
	st.Record(ev("http://cdn/CUSA2_0.pkg", "CUSA2", 0))

	snap := st.Snapshot()
	if len(snap) != 2 {
		t.Fatalf("expected 2 sessions, got %d", len(snap))
	}
	if snap[0].Title != "CUSA1" || snap[1].Title != "CUSA2" {
		t.Errorf("titles not sorted: %s, %s", snap[0].Title, snap[1].Title)
	}
	// Parts of CUSA1 sorted by index: _0 before _1.
	if snap[0].Parts[0].Basename != "CUSA1_0.pkg" {
		t.Errorf("parts not ordered by index: %+v", snap[0].Parts)
	}
}

func TestSnapshotReflectsLibraryState(t *testing.T) {
	lib := stubLib{local: map[string]library.VerifyState{
		"CUSA1_0.pkg": library.VerifyOK,
		"CUSA1_1.pkg": library.VerifyFailed,
	}}
	st := New(lib)
	st.Record(ev("http://cdn/CUSA1_0.pkg", "CUSA1", 0))
	st.Record(ev("http://cdn/CUSA1_1.pkg", "CUSA1", 1))
	st.Record(ev("http://cdn/CUSA1_2.pkg", "CUSA1", 2)) // not local

	snap := st.Snapshot()
	s := snap[0]
	if s.LocalCount != 2 || s.TotalCount != 3 {
		t.Errorf("counts: local=%d total=%d, want 2/3", s.LocalCount, s.TotalCount)
	}
	if s.Parts[0].Verified != "ok" {
		t.Errorf("part 0 verified = %q, want ok", s.Parts[0].Verified)
	}
	if s.Parts[1].Verified != "failed" {
		t.Errorf("part 1 verified = %q, want failed", s.Parts[1].Verified)
	}
	if s.Parts[2].Local {
		t.Errorf("part 2 should not be local")
	}
}
