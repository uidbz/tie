package proxy

import (
	"bufio"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestIsWebSocketRequest(t *testing.T) {
	cases := []struct {
		name       string
		connection []string
		upgrade    []string
		want       bool
	}{
		{"chrome", []string{"Upgrade"}, []string{"websocket"}, true},
		{"firefox token list", []string{"keep-alive, Upgrade"}, []string{"websocket"}, true},
		{"repeated header lines", []string{"keep-alive", "upgrade"}, []string{"WebSocket"}, true},
		{"upgrade token list", []string{"Upgrade"}, []string{"h2c, websocket"}, true},
		{"plain keep-alive", []string{"keep-alive"}, nil, false},
		{"non-websocket upgrade", []string{"Upgrade"}, []string{"h2c"}, false},
		{"missing connection token", []string{"keep-alive"}, []string{"websocket"}, false},
		{"substring is not a token", []string{"upgrade-insecure"}, []string{"websocket"}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/service/x:latest/ws", nil)
			for _, v := range c.connection {
				r.Header.Add("Connection", v)
			}
			for _, v := range c.upgrade {
				r.Header.Add("Upgrade", v)
			}
			if got := isWebSocketRequest(r); got != c.want {
				t.Errorf("isWebSocketRequest = %v, want %v", got, c.want)
			}
		})
	}
}

// A Firefox-style upgrade with bytes pipelined after the request must reach the
// backend intact, path-rewritten, and the tunnel must carry data both ways.
func TestServeServiceWebSocketTunnel(t *testing.T) {
	backend, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()
	gotPath := make(chan string, 1)
	go func() {
		conn, err := backend.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		br := bufio.NewReader(conn)
		req, err := http.ReadRequest(br)
		if err != nil {
			gotPath <- "error: " + err.Error()
			return
		}
		gotPath <- req.URL.RequestURI()
		io.WriteString(conn, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\n")
		io.Copy(conn, br) // echo
	}()

	const service = "wstest:latest"
	h := New(Config{
		ExternalPort: func(s string) (int, bool) {
			if s != service {
				return 0, false
			}
			return backend.Addr().(*net.TCPAddr).Port, true
		},
	})

	mux := http.NewServeMux()
	mux.HandleFunc("/service/{service}/{params...}", func(w http.ResponseWriter, r *http.Request) {
		h.ServeService(w, r, r.PathValue("service"))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	conn, err := net.Dial("tcp", srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(5 * time.Second))

	io.WriteString(conn, "GET /service/"+service+"/api/ws?ticket=abc HTTP/1.1\r\n"+
		"Host: example\r\nConnection: keep-alive, Upgrade\r\nUpgrade: websocket\r\n"+
		"Sec-WebSocket-Version: 13\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\n\r\n"+
		"early")

	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("status = %d, want 101", resp.StatusCode)
	}
	if p := <-gotPath; p != "/api/ws?ticket=abc" {
		t.Fatalf("backend path = %q, want /api/ws?ticket=abc", p)
	}

	io.WriteString(conn, "-late")
	want := "early-late"
	buf := make([]byte, len(want))
	if _, err := io.ReadFull(br, buf); err != nil {
		t.Fatal(err)
	}
	if got := string(buf); !strings.EqualFold(got, want) {
		t.Fatalf("echo = %q, want %q", got, want)
	}
}

func TestServeServiceUnknownServiceJSONError(t *testing.T) {
	h := New(Config{ExternalPort: func(string) (int, bool) { return 0, false }})
	rec := httptest.NewRecorder()
	h.ServeService(rec, httptest.NewRequest(http.MethodGet, "/service/ghost:latest/", nil), "ghost:latest")
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	if !strings.Contains(rec.Body.String(), `"ErrorMsg":"Service not running: ghost:latest"`) {
		t.Errorf("body = %s", rec.Body.String())
	}
}

func TestRewriteHTMLPathsXMLNoBase(t *testing.T) {
	xml := `<?xml version='1.0'?>\n<partial-response><changes><update id="f:panel"><![CDATA[<img src="/app/resources/images/imgcenter.png"/>]]></update></changes></partial-response>`

	htmlOut := string(rewriteHTMLPaths([]byte(xml), "https://x.test/service/a:latest/p/", "/service/a:latest", false, true, false))
	xmlOut := string(rewriteHTMLPaths([]byte(xml), "https://x.test/service/a:latest/p/", "/service/a:latest", false, false, false))

	if !strings.Contains(htmlOut, "<base href=") {
		t.Errorf("HTML mode must inject base tag")
	}
	if strings.Contains(xmlOut, "<base href=") {
		t.Errorf("XML mode must NOT inject base tag (breaks XML): %s", xmlOut)
	}
	want := `src="/service/a:latest/app/resources/images/imgcenter.png"`
	if !strings.Contains(xmlOut, want) {
		t.Errorf("XML mode must rewrite root-relative paths; want %s in %s", want, xmlOut)
	}
}

func TestRewriteSetCookiePath(t *testing.T) {
	p := "/service/tool:latest"
	cases := []struct{ in, want string }{
		{"JSESSIONID=abc; Path=/app; HttpOnly", "JSESSIONID=abc; Path=/service/tool:latest/app; HttpOnly"},
		{"JSESSIONID=abc; path=/; HttpOnly", "JSESSIONID=abc; path=/service/tool:latest; HttpOnly"},
		{"SESSION=xyz; Path=/service/other/x", "SESSION=xyz; Path=/service/other/x"}, // other service prefix: leave
		{"JSESSIONID=abc; HttpOnly", "JSESSIONID=abc; HttpOnly"},                     // no Path: browser default already under prefix
		{"JSESSIONID=abc; Path=/app", "JSESSIONID=abc; Path=/service/tool:latest/app"},
	}
	for _, c := range cases {
		if got := rewriteSetCookiePath(c.in, p); got != c.want {
			t.Errorf("rewriteSetCookiePath(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestRewriteHTMLPathsKeepsForeignURLs(t *testing.T) {
	body := []byte(`<html xmlns="http://www.w3.org/1999/xhtml"><head></head>` +
		`<a href="http://own.host.example:2008/app/x">own</a>` +
		`<a href="https://example.com/go">ext</a></html>`)
	out := string(rewriteHTMLPaths(body, "https://own.host.example:443/service/tool:latest/app/", "/service/tool:latest", false, false, false))
	if !strings.Contains(out, `xmlns="http://www.w3.org/1999/xhtml"`) {
		t.Errorf("foreign w3.org URL must stay intact, got: %s", out)
	}
	if !strings.Contains(out, `href="/service/tool:latest/app/x"`) {
		t.Errorf("own-host URL must be rewritten, got: %s", out)
	}
	if !strings.Contains(out, `https://example.com/go`) {
		t.Errorf("external link must stay intact, got: %s", out)
	}
}

func TestRewriteCSSAndJSPaths(t *testing.T) {
	p := "/service/tool:latest"
	css := `.ui-icon{background-image:url("/app/static/ui-icons.png?ln=theme")}` +
		`.ui-tree .line{background:url(/app/static/tree/line.gif)}`
	out := string(rewriteHTMLPaths([]byte(css), "https://x.test/service/tool:latest/app/", p, false, false, true))
	if !strings.Contains(out, `url("`+p+`/app/static/ui-icons.png`) {
		t.Errorf("quoted url() must be prefixed, got: %s", out)
	}
	if !strings.Contains(out, `url(`+p+`/app/static/tree/line.gif`) {
		t.Errorf("unquoted url() must be prefixed, got: %s", out)
	}

	js := `$.ajax({url: '/app/faces/AjaxCall'}); var s = a.split('/'); var u = '//cdn.example/x'; export_image('/x');`
	out = string(rewriteHTMLPaths([]byte(js), "https://x.test/service/tool:latest/app/", p, false, false, true))
	if !strings.Contains(out, `url: '`+p+`/app/faces/AjaxCall'`) {
		t.Errorf("JS quoted root path must be prefixed, got: %s", out)
	}
	if !strings.Contains(out, `split('/')`) {
		t.Errorf("bare '/' separator must stay intact, got: %s", out)
	}
	if !strings.Contains(out, `'//cdn.example/x'`) {
		t.Errorf("protocol-relative URL must stay intact, got: %s", out)
	}

	// Some server-rendered frameworks emit 'href = "/..." ' with spaces
	// around '='; the strict ="/ rule misses it.
	link := `<b>Download: </b> <a target='_blank' href = "/app/users/u1/report.png"><b>x</b></a>`
	out = string(rewriteHTMLPaths([]byte(link), "https://x.test/service/tool:latest/app/", p, false, false, false))
	if !strings.Contains(out, `href ="`+p+`/app/users/u1/report.png"`) {
		t.Errorf("spaced href must be normalized and prefixed, got: %s", out)
	}

	// Already-prefixed strings (ajax XML, rewritten attributes) must stay single-prefixed.
	once := `<img src="/service/tool:latest/app/tmp/x.png"/>`
	out = string(rewriteHTMLPaths([]byte(once), "https://x.test/service/tool:latest/app/", p, false, false, false))
	if strings.Contains(out, p+p) {
		t.Errorf("no double prefix, got: %s", out)
	}
}

func TestScopeFor(t *testing.T) {
	configured := map[string]string{
		"unspecified:latest":  "",
		"explicit-all:latest": "all",
		"prefix-aware:latest": "html",
		"hands-off:latest":    " NONE ",
		"typo:latest":         "hmtl",
	}
	h := New(Config{RewriteScope: func(service string) string { return configured[service] }})

	cases := map[string]string{
		// Unset and unknown values both mean the default.
		"unspecified:latest":    ScopeAll,
		"not-configured:latest": ScopeAll,
		"explicit-all:latest":   ScopeAll,
		"typo:latest":           ScopeAll,
		"prefix-aware:latest":   ScopeHTML,
		"hands-off:latest":      ScopeNone,
	}
	for service, want := range cases {
		if got := h.scopeFor(service); got != want {
			t.Errorf("scope for %s = %q, want %q", service, got, want)
		}
	}

	// A nil resolver means the default for everything.
	bare := New(Config{})
	if got := bare.scopeFor("anything:latest"); got != ScopeAll {
		t.Errorf("nil resolver scope = %q, want %q", got, ScopeAll)
	}
}

func TestBodyRewriteNeeded(t *testing.T) {
	cases := []struct {
		scope       string
		contentType string
		want        bool
	}{
		{ScopeAll, "text/html; charset=utf-8", true},
		{ScopeAll, "application/xml", true},
		{ScopeAll, "text/css", true},
		{ScopeAll, "application/javascript", true},
		{ScopeAll, "application/json", false},
		{ScopeHTML, "text/html; charset=utf-8", true},
		{ScopeHTML, "application/xml", true},
		{ScopeHTML, "text/css", false},
		{ScopeHTML, "text/javascript", false},
		{ScopeNone, "text/html", false},
		{ScopeNone, "text/css", false},
	}
	for _, c := range cases {
		if got := bodyRewriteNeeded(c.scope, c.contentType); got != c.want {
			t.Errorf("bodyRewriteNeeded(%q, %q) = %v, want %v", c.scope, c.contentType, got, c.want)
		}
	}
}

// A login page that posts with fetch(prefix + '/auth/login'), where prefix
// comes from a runtime variable: prefixing that literal produces
// /service/x:latest/service/x:latest/auth/... in the browser, and the
// double-prefix collapse in rewriteHTMLPaths cannot see it (the join happens
// client-side). Inline HTML script literals are therefore left alone;
// standalone CSS/JS bodies, where the url("/path") references actually live,
// are still scanned.
func TestInlineHTMLScriptLiteralsAreNotRewritten(t *testing.T) {
	p := "/service/worker-agent:latest"
	page := `<script>fetch(prefix + '/auth/password-login');</script>`

	htmlOut := string(rewriteHTMLPaths([]byte(page), "https://x.test"+p+"/login/", p, false, false, false))
	if htmlOut != page {
		t.Errorf("HTML inline script must survive untouched, got: %s", htmlOut)
	}

	jsOut := string(rewriteHTMLPaths([]byte(page), "https://x.test"+p+"/login/", p, false, false, true))
	if !strings.Contains(jsOut, `'`+p+`/auth/password-login'`) {
		t.Errorf("JS bodies must still have quoted root literals prefixed, got: %s", jsOut)
	}
}

// End-to-end through ModifyResponse: a redirect Location from the backend
// must come back namespaced under the service prefix.
func TestLocationHeaderRewrite(t *testing.T) {
	backend := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			http.Redirect(w, r, "/login", http.StatusFound)
		case "/login":
			t.Error("followed redirect inside the test")
		default:
			t.Errorf("unexpected backend path %q", r.URL.Path)
		}
	})
	srv := httptest.NewServer(backend)
	defer srv.Close()
	_, portStr, _ := net.SplitHostPort(srv.Listener.Addr().String())
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatal(err)
	}

	h := New(Config{ExternalPort: func(string) (int, bool) { return port, true }})
	rec := httptest.NewRecorder()
	h.ServeService(rec, httptest.NewRequest(http.MethodGet, "/service/tool:latest/", nil), "tool:latest")

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302", rec.Code)
	}
	// The rewrite emits an absolute external URL (the old proxy does too;
	// browsers follow it identically, and it survives hop rewriting better).
	if loc := rec.Header().Get("Location"); loc != "http://example.com/service/tool:latest/login" {
		t.Errorf("Location = %q, want http://example.com/service/tool:latest/login", loc)
	}
}
