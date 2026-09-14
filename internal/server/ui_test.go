package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/scottlaird/drivelist/internal/store"
)

func uiServer(t *testing.T, viewer string) *httptest.Server {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	srv, err := New(st, Config{AgentToken: "a", OperatorToken: "o", ViewerToken: viewer, Interval: time.Minute}, nil)
	if err != nil {
		t.Fatal(err)
	}
	hs := httptest.NewServer(srv.Handler())
	t.Cleanup(hs.Close)
	return hs
}

// TestUIServed: / redirects into the interface, which is served with a
// policy that admits no inline script and nothing from elsewhere.
func TestUIServed(t *testing.T) {
	hs := uiServer(t, "v")
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, err := client.Get(hs.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusFound || res.Header.Get("Location") != "/ui/" {
		t.Errorf("/ -> %d %s", res.StatusCode, res.Header.Get("Location"))
	}
	for _, path := range []string{"/ui/", "/ui/app.js", "/ui/style.css"} {
		res, err := http.Get(hs.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if res.StatusCode != 200 || len(body) == 0 {
			t.Errorf("%s: %d, %d bytes", path, res.StatusCode, len(body))
		}
		csp := res.Header.Get("Content-Security-Policy")
		if !strings.Contains(csp, "default-src 'none'") || !strings.Contains(csp, "script-src 'self' "+mermaidSource+";") || strings.Contains(csp, "unsafe-eval") {
			t.Errorf("%s: policy %q", path, csp)
		}
		if res.Header.Get("X-Content-Type-Options") != "nosniff" {
			t.Errorf("%s: no nosniff", path)
		}
	}
	// The page carries no inline script or style, which the policy would
	// block anyway; better that it never relies on them.
	res, _ = http.Get(hs.URL + "/ui/")
	page, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if strings.Contains(string(page), "<script>") || strings.Contains(string(page), "onclick") || strings.Contains(string(page), "style=") {
		t.Errorf("index.html has inline script or style")
	}
}

// TestUINeverBuildsHTMLFromData: the script builds every node with
// createElement and textContent. Any of these would let a value from the
// database become markup, so their absence is checked here rather than
// remembered.
func TestUINeverBuildsHTMLFromData(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("ui", "app.js"))
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"innerHTML", "outerHTML", "insertAdjacentHTML", "document.write", "createContextualFragment", "DOMParser", "eval(", "new Function", "srcdoc"} {
		if strings.Contains(string(src), bad) {
			t.Errorf("app.js uses %s", bad)
		}
	}
	// Links are only ever built from hash routes with encoded parameters.
	if m := regexp.MustCompile(`href[^\n]*javascript:`).Find(src); m != nil {
		t.Errorf("app.js builds a javascript: URL: %s", m)
	}
	// The one script it loads from elsewhere is the Mermaid file the policy
	// names, so the pin and the policy cannot drift apart.
	urls := regexp.MustCompile(`https://[^'"\s]+`).FindAll(src, -1)
	if len(urls) == 0 {
		t.Errorf("app.js names no Mermaid URL")
	}
	for _, u := range urls {
		if !strings.HasPrefix(string(u), mermaidSource) || !strings.HasSuffix(string(u), ".js") {
			t.Errorf("app.js loads %s, outside the policy's %s", u, mermaidSource)
		}
	}
}

// TestViewerToken: the viewer token reads and cannot change anything; the
// browser's JSON calls work the way the page makes them.
func TestViewerToken(t *testing.T) {
	hs := uiServer(t, "v")
	call := func(token, proc, body string) (int, string) {
		req, _ := http.NewRequest("POST", hs.URL+"/drivelist.v1.Query/"+proc, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+token)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(b)
	}
	if code, body := call("v", "ListHosts", "{}"); code != 200 || !strings.HasPrefix(body, "{") {
		t.Errorf("viewer ListHosts: %d %s", code, body)
	}
	if code, body := call("v", "Annotate", `{"ref":"x","status":"bad"}`); code != http.StatusForbidden || !strings.Contains(body, "viewer token") {
		t.Errorf("viewer Annotate: %d %s", code, body)
	}
	if code, _ := call("wrong", "ListHosts", "{}"); code != http.StatusUnauthorized {
		t.Errorf("wrong token: %d", code)
	}
	if code, _ := call("o", "ListHosts", "{}"); code != 200 {
		t.Errorf("operator ListHosts: %d", code)
	}
	// Without a viewer token configured, nothing but the operator token reads.
	hs2 := uiServer(t, "")
	req, _ := http.NewRequest("POST", hs2.URL+"/drivelist.v1.Query/ListHosts", strings.NewReader("{}"))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer ")
	res, _ := http.DefaultClient.Do(req)
	res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Errorf("empty token with no viewer configured: %d", res.StatusCode)
	}
}
