package handoff

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAria2AddURI(t *testing.T) {
	var gotMethod string
	var gotParams []json.RawMessage
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Method string            `json:"method"`
			Params []json.RawMessage `json:"params"`
		}
		_ = json.Unmarshal(body, &req)
		gotMethod = req.Method
		gotParams = req.Params
		_, _ = io.WriteString(w, `{"jsonrpc":"2.0","id":"psxdh","result":"abcd1234"}`)
	}))
	defer srv.Close()

	c := NewAria2(srv.URL, "s3cr3t", nil)
	gid, err := c.AddURI(context.Background(), "http://cdn.example/path/GAME_0.pkg?sig=x", "/lib")
	if err != nil {
		t.Fatal(err)
	}
	if gid != "abcd1234" {
		t.Errorf("gid = %q, want abcd1234", gid)
	}
	if gotMethod != "aria2.addUri" {
		t.Errorf("method = %q", gotMethod)
	}
	// params: ["token:s3cr3t", ["url"], {out,dir}]
	if len(gotParams) != 3 {
		t.Fatalf("expected 3 params, got %d: %v", len(gotParams), gotParams)
	}
	var token string
	_ = json.Unmarshal(gotParams[0], &token)
	if token != "token:s3cr3t" {
		t.Errorf("token param = %q", token)
	}
	var opts map[string]string
	_ = json.Unmarshal(gotParams[2], &opts)
	if opts["out"] != "GAME_0.pkg" {
		t.Errorf("out option = %q, want GAME_0.pkg", opts["out"])
	}
	if opts["dir"] != "/lib" {
		t.Errorf("dir option = %q, want /lib", opts["dir"])
	}
}

func TestAria2RPCError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"jsonrpc":"2.0","id":"psxdh","error":{"code":1,"message":"unauthorised"}}`)
	}))
	defer srv.Close()

	c := NewAria2(srv.URL, "", nil)
	if _, err := c.AddURI(context.Background(), "http://x/y.pkg", ""); err == nil {
		t.Fatal("expected an rpc error")
	}
}

func TestAria2NoSecretOmitsToken(t *testing.T) {
	var paramCount int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Params []json.RawMessage `json:"params"`
		}
		_ = json.Unmarshal(body, &req)
		paramCount = len(req.Params)
		_, _ = io.WriteString(w, `{"result":"gid"}`)
	}))
	defer srv.Close()

	c := NewAria2(srv.URL, "", nil)
	if _, err := c.AddURI(context.Background(), "http://x/y.pkg", ""); err != nil {
		t.Fatal(err)
	}
	// Without a secret and without a dir: [["url"], {out}] → 2 params.
	if paramCount != 2 {
		t.Errorf("expected 2 params without secret, got %d", paramCount)
	}
}

func TestAria2TellStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"result":{"status":"active","completedLength":"500","totalLength":"1000","downloadSpeed":"250","errorMessage":""}}`)
	}))
	defer srv.Close()

	c := NewAria2(srv.URL, "", nil)
	st, err := c.TellStatus(context.Background(), "gid1")
	if err != nil {
		t.Fatal(err)
	}
	if st.Status != "active" || st.Completed != 500 || st.Total != 1000 || st.SpeedBPS != 250 {
		t.Errorf("unexpected status: %+v", st)
	}
}
