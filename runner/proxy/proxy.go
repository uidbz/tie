// Package proxy is the runner's data plane: /service/{name} reverse-proxies
// to a running service container, rewriting paths, redirects, cookies, and
// (within a configured scope) response bodies so the container never sees
// the external prefix and the browser never leaves it.
//
// The handler owns no state of its own. Two callbacks connect it to the
// runner: ExternalPort resolves a service name to its backend port, and
// RewriteScope optionally reports how deeply response bodies get rewritten
// for that service. Everything else — prefix stripping, X-Forwarded-*
// headers, WebSocket tunneling, base-tag injection, Location and Set-Cookie
// rewriting — is self-contained.
package proxy

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/uidbz/tie/runner/api"
)

// Config wires the proxy to the rest of the runner.
type Config struct {
	// ExternalPort resolves a running service to the localhost port its
	// container listens on.
	ExternalPort func(service string) (int, bool)
	// RewriteScope optionally reports the body rewrite scope for a
	// service (ScopeAll, ScopeHTML, ScopeNone; "" means ScopeAll). It is
	// consulted per request, so configuration changes take effect without
	// rebuilding anything.
	RewriteScope func(service string) string
}

// Handler serves proxied service traffic. The route pattern stays with the
// server; the handler takes the service name from the path value.
type Handler struct {
	cfg Config
}

func New(cfg Config) *Handler {
	return &Handler{cfg: cfg}
}

// ServeService proxies one request to the named service's backend.
func (h *Handler) ServeService(w http.ResponseWriter, r *http.Request, service string) {
	slog.Debug("Service request", "service", service, "path", r.URL.Path, "method", r.Method)

	port, exists := h.cfg.ExternalPort(service)
	if !exists {
		// Wire-compatible with the API endpoints' JSON error body.
		b, err := json.Marshal(&api.JobStatus{ErrorMsg: "Service not running: " + service})
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(b)
		return
	}
	target := mustParseURL("http://localhost:" + strconv.Itoa(port))

	// Check if this is a WebSocket upgrade request
	if isWebSocketRequest(r) {
		h.handleWebSocket(w, r, target, service)
		return
	}

	h.buildReverseProxy(r, target, service).ServeHTTP(w, r)
}

// ─────────────────────────────────────────────
// WebSocket Support
// ─────────────────────────────────────────────

// Connection and Upgrade are token lists (Firefox sends "keep-alive, Upgrade"),
// possibly split across repeated header lines.
func isWebSocketRequest(r *http.Request) bool {
	return headerHasToken(r.Header, "Connection", "upgrade") &&
		headerHasToken(r.Header, "Upgrade", "websocket")
}

func headerHasToken(h http.Header, name, token string) bool {
	for _, v := range h.Values(name) {
		for _, t := range strings.Split(v, ",") {
			if strings.EqualFold(strings.TrimSpace(t), token) {
				return true
			}
		}
	}
	return false
}

func (h *Handler) handleWebSocket(w http.ResponseWriter, r *http.Request, target *url.URL, service string) {
	// Extract path for backend
	incomingPath := r.URL.Path
	prefix := servicePrefix(service)
	backendPath := strings.TrimPrefix(incomingPath, prefix)
	if backendPath == "" {
		backendPath = "/"
	}
	if !strings.HasPrefix(backendPath, "/") {
		backendPath = "/" + backendPath
	}

	slog.Info("WebSocket upgrade request",
		"service", service,
		"original_path", r.URL.Path,
		"backend_path", backendPath,
		"backend_host", target.Host,
	)

	// Dial the backend WebSocket
	backendConn, err := net.DialTimeout("tcp", target.Host, 10*time.Second)
	if err != nil {
		slog.Error("Failed to dial backend for WebSocket", "service", service, "error", err)
		http.Error(w, "Failed to connect to backend", http.StatusBadGateway)
		return
	}

	// Hijack the client connection
	hj, ok := w.(http.Hijacker)
	if !ok {
		backendConn.Close()
		http.Error(w, "Hijacking not supported", http.StatusInternalServerError)
		return
	}

	clientConn, clientBuf, err := hj.Hijack()
	if err != nil {
		backendConn.Close()
		slog.Error("Failed to hijack client connection", "error", err)
		return
	}

	// Rewrite the request path for the backend
	r.URL.Path = singleJoiningSlash(target.Path, backendPath)
	r.URL.RawPath = ""
	r.Host = target.Host
	r.RequestURI = "" // Must be cleared for client requests

	// Forward the modified upgrade request to the backend
	err = r.Write(backendConn)
	if err != nil {
		clientConn.Close()
		backendConn.Close()
		slog.Error("Failed to write upgrade request to backend", "error", err)
		return
	}

	// Bidirectional copy between client and backend
	errc := make(chan error, 2)
	cp := func(dst io.Writer, src io.Reader, name string) {
		_, err := io.Copy(dst, src)
		if err != nil && err != io.EOF {
			slog.Debug("WebSocket copy error", "direction", name, "error", err)
		}
		errc <- err
	}

	// Read via the hijack buffer: it may already hold client bytes past the request.
	go cp(backendConn, clientBuf.Reader, "client->backend")
	go cp(clientConn, backendConn, "backend->client")

	// Wait for one direction to close
	<-errc

	// Close both connections
	clientConn.Close()
	backendConn.Close()

	slog.Info("WebSocket connection closed", "service", service, "path", r.URL.Path)
}

// ─────────────────────────────────────────────
// Reverse proxy builder
// ─────────────────────────────────────────────

// servicePrefix is the external URL prefix a service is mounted under.
func servicePrefix(service string) string {
	return "/service/" + service
}

// buildReverseProxy creates a reverse proxy for a backend service with path
// rewriting.
//
// Path Rewriting:
// The proxy strips the /service/{name} prefix from incoming requests before
// forwarding to the backend. The backend service never sees the external
// prefix.
//
// Examples:
//   - External: /service/tool:latest → Backend: /
//   - External: /service/tool:latest/dir/ → Backend: /dir/
//   - External: /service/tool:latest/cgi-bin/index.cgi → Backend: /cgi-bin/index.cgi
//
// HTML Rewriting:
// For HTML responses, the proxy injects a <base> tag to ensure relative URLs
// resolve correctly with the external prefix. This handles:
//   - Apache directory listings with relative links
//   - CGI scripts referencing CSS/JS with relative paths
//   - Any HTML content with relative resource references
//
// The base tag is calculated intelligently:
//   - Files with extensions (*.cgi, *.html): base is the containing directory
//   - Paths without extensions: treated as directories, trailing slash added
//
// Content Encoding:
// Automatically decompresses gzipped responses before HTML modification,
// then sends uncompressed to avoid re-compression overhead for small responses.
func (h *Handler) buildReverseProxy(r *http.Request, target *url.URL, service string) *httputil.ReverseProxy {
	origHost := r.Host
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	// Check X-Forwarded-Proto for scheme when behind another proxy (like Caddy)
	if proto := r.Header.Get("X-Forwarded-Proto"); proto != "" {
		scheme = proto
	}

	// Use X-Forwarded-Host if available (for proper URL generation behind proxies)
	if fwdHost := r.Header.Get("X-Forwarded-Host"); fwdHost != "" {
		origHost = fwdHost
	}

	// Detect if accessed through a proxy that wants clean URLs:
	// an X-Forwarded-Host that differs from our own server name means the
	// caller mounted this service under a clean hostname, so responses must
	// not carry the /service/{name} prefix.
	cleanURLs := origHost != r.Host && origHost != ""

	incomingPath := r.URL.Path
	endsWithSlash := strings.HasSuffix(incomingPath, "/")
	prefix := servicePrefix(service)

	// How broadly this service's response bodies get rewritten. Resolved
	// per request, so it follows the live configuration the moment the
	// entry is updated.
	rewriteScope := h.scopeFor(service)

	slog.Debug("Building reverse proxy",
		"service", service,
		"prefix", prefix,
		"incoming_path", incomingPath,
		"target", target.String(),
	)

	proxy := &httputil.ReverseProxy{Transport: newProxyTransport()}

	// Captured by the rewrite hook and used by ModifyResponse to resolve
	// relative redirects against the external URL space.
	var externalBaseDir string

	proxy.Rewrite = func(pr *httputil.ProxyRequest) {
		// Route to the backend host...
		pr.SetURL(target)

		// ...but replace the path entirely: strip the external prefix
		// (/service/{service}) from the incoming *external* path.
		// Example: /service/tool:latest/dir/file.cgi → /dir/file.cgi
		p := strings.TrimPrefix(incomingPath, prefix)
		if p == "" {
			p = "/"
		}
		if !strings.HasPrefix(p, "/") {
			p = "/" + p
		}
		if endsWithSlash && !strings.HasSuffix(p, "/") {
			p += "/"
		}

		// Set the backend request path (without the /service/{service} prefix)
		pr.Out.URL.Path = singleJoiningSlash(target.Path, p)
		pr.Out.URL.RawPath = ""
		pr.Out.URL.RawQuery = r.URL.RawQuery

		// Ensure vhost routing on the backend is correct
		pr.Out.Host = target.Host

		// X-Forwarded-* headers. The proxy strips client-supplied ones
		// before this hook, so the chain below is authoritative.
		clientIP := clientIPFrom(r)
		if prior := r.Header.Get("X-Forwarded-For"); prior != "" && clientIP != "" {
			pr.Out.Header.Set("X-Forwarded-For", prior+", "+clientIP)
		} else if clientIP != "" {
			pr.Out.Header.Set("X-Forwarded-For", clientIP)
		}
		pr.Out.Header.Set("X-Forwarded-Host", origHost)
		pr.Out.Header.Set("X-Forwarded-Proto", scheme)
		pr.Out.Header.Set("X-Forwarded-Uri", r.URL.RequestURI())

		// Optional: empty UA
		if _, ok := pr.Out.Header["User-Agent"]; !ok {
			pr.Out.Header.Set("User-Agent", "")
		}

		// Compute the *external* request base dir (for resolving relative redirects)
		externalBaseDir = r.URL.Path
		if !strings.HasSuffix(externalBaseDir, "/") {
			if idx := strings.LastIndex(externalBaseDir, "/"); idx >= 0 {
				externalBaseDir = externalBaseDir[:idx+1]
			} else {
				externalBaseDir = "/"
			}
		}
	}

	// Helper to map backend path back to external prefix (strip target.Path if present)
	rewriteToExternal := func(backendPath string) string {
		outPath := backendPath
		if target.Path != "" && strings.HasPrefix(outPath, target.Path) {
			outPath = strings.TrimPrefix(outPath, target.Path)
			if !strings.HasPrefix(outPath, "/") {
				outPath = "/" + outPath
			}
		}

		var result string
		if cleanURLs {
			// Clean URLs mode: don't add service prefix, just return the path
			result = outPath
		} else {
			// Normal mode: add service prefix
			result = singleJoiningSlash(prefix, outPath)
		}

		slog.Debug("Rewriting to external path",
			"backend_path", backendPath,
			"prefix", prefix,
			"clean_urls", cleanURLs,
			"result", result,
		)
		return result
	}

	// Correctly rewrite Location headers and inject base tags for HTML
	proxy.ModifyResponse = func(resp *http.Response) error {
		// Inject <base> tag for HTML responses (fixes relative links in Apache directory listings)
		contentType := resp.Header.Get("Content-Type")

		slog.Debug("ModifyResponse called",
			"service", service,
			"path", r.URL.Path,
			"content_type", contentType,
			"status", resp.StatusCode,
		)

		// Warn if Content-Type is missing (backend should set this)
		if contentType == "" && resp.StatusCode == 200 {
			slog.Warn("Backend returned 200 OK without Content-Type header",
				"service", service,
				"path", r.URL.Path,
			)
		}

		if bodyRewriteNeeded(rewriteScope, contentType) {
			// Read the response body
			bodyReader := resp.Body
			isGzipped := strings.Contains(resp.Header.Get("Content-Encoding"), "gzip")

			// Decompress if gzipped
			if isGzipped {
				gzReader, err := gzip.NewReader(resp.Body)
				if err != nil {
					resp.Body.Close()
					return err
				}
				bodyReader = gzReader
			}

			body, err := io.ReadAll(bodyReader)
			resp.Body.Close()
			if err != nil {
				return err
			}

			// Construct the base URL (the directory containing the current resource)
			basePath := r.URL.Path
			// If the path doesn't end with /, we need to determine if it's a file or directory
			// Only strip the last segment if it looks like a file (has an extension)
			if !strings.HasSuffix(basePath, "/") {
				lastSegment := basePath
				if idx := strings.LastIndex(basePath, "/"); idx >= 0 {
					lastSegment = basePath[idx+1:]
				}
				// If it has an extension (like .cgi, .html, .php), treat as file and use directory
				if strings.Contains(lastSegment, ".") {
					basePath = basePath[:strings.LastIndex(basePath, "/")+1]
				} else {
					// No extension, likely a directory without trailing slash - add one
					basePath += "/"
				}
			}

			// For clean URLs, strip the service prefix from basePath
			if cleanURLs && strings.HasPrefix(basePath, prefix+"/") {
				basePath = strings.TrimPrefix(basePath, prefix)
			}

			baseURL := scheme + "://" + origHost + basePath

			// Rewrite HTML paths (both base tag injection and absolute path rewriting).
			// XML responses (e.g. JSF/Mojarra partial ajax) get path rewriting but
			// never a <base> tag — a stray <base> would invalidate the XML.
			// CSS/JS bodies (rewrite scope "all" only) get the same path rewriting
			// (their url("/...") and '/...' literals otherwise resolve outside the
			// /service/ prefix) and never a <base> tag.
			isHTML := strings.Contains(contentType, "text/html")
			isCSS := strings.Contains(contentType, "text/css")
			isJS := strings.Contains(contentType, "javascript")
			modifiedBody := rewriteHTMLPaths(body, baseURL, prefix, cleanURLs, isHTML, isCSS || isJS)

			// Don't re-gzip, just remove the encoding header and send uncompressed
			// (simpler and the content is small)
			if isGzipped {
				resp.Header.Del("Content-Encoding")
			}

			// Replace response body
			resp.Body = io.NopCloser(bytes.NewReader(modifiedBody))
			resp.Header.Set("Content-Length", strconv.Itoa(len(modifiedBody)))
		}

		// Handle Location header redirects
		loc := resp.Header.Get("Location")

		// Rewrite Set-Cookie Path attributes so backend cookies are visible
		// through the /service/{name} prefix (e.g. Path=/app from the
		// backend is invisible to the browser at /service/{name}/app,
		// which silently breaks app sessions once session-ID URL rewriting
		// stops).
		if cookies := resp.Header["Set-Cookie"]; len(cookies) > 0 {
			rewritten := make([]string, 0, len(cookies))
			for _, c := range cookies {
				rewritten = append(rewritten, rewriteSetCookiePath(c, prefix))
			}
			resp.Header["Set-Cookie"] = rewritten
		}

		if loc == "" {
			return nil
		}

		slog.Debug("Processing Location header",
			"service", service,
			"original_location", loc,
			"external_base_dir", externalBaseDir,
		)

		u, err := url.Parse(loc)
		if err != nil {
			return nil // don't fail the response
		}

		// Helper to check if a host is localhost/internal
		isLocalHost := func(host string) bool {
			// Remove port for comparison
			h, _, _ := net.SplitHostPort(host)
			if h == "" {
				h = host // no port
			}
			return h == "localhost" || h == "127.0.0.1" || h == "::1" || h == ""
		}

		switch {
		case u.IsAbs():
			// Absolute URL. Rewrite if it points to localhost/backend or matches target host
			shouldRewrite := u.Host == target.Host || isLocalHost(u.Host)
			if !shouldRewrite {
				return nil // leave external URLs as-is
			}
			newURL := &url.URL{
				Scheme:   scheme,
				Host:     origHost,
				Path:     rewriteToExternal(u.EscapedPath()),
				RawQuery: u.RawQuery,
				Fragment: u.Fragment,
			}
			resp.Header.Set("Location", newURL.String())
			return nil

		default:
			// Not absolute. Could be absolute-path (starts with "/") or relative ("result.cgi", "./x", "../x").
			if strings.HasPrefix(u.Path, "/") {
				newURL := &url.URL{
					Scheme:   scheme,
					Host:     origHost,
					Path:     rewriteToExternal(u.Path),
					RawQuery: u.RawQuery,
					Fragment: u.Fragment,
				}
				resp.Header.Set("Location", newURL.String())
				return nil
			}

			// Pure relative → resolve against the EXTERNAL base directory
			base := &url.URL{
				Scheme: scheme,
				Host:   origHost,
				Path:   externalBaseDir, // must be a directory path with trailing slash
			}
			resolved := base.ResolveReference(u)
			slog.Debug("Resolved relative redirect",
				"original", loc,
				"base", base.String(),
				"resolved", resolved.String(),
			)
			// Ensure path normalization is preserved and we don't accidentally drop the host/scheme
			resp.Header.Set("Location", resolved.String())
			return nil
		}
	}

	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		slog.Error("Proxy error", "service", service, "target", target.String(), "error", err)
		http.Error(w, "Bad Gateway (upstream unavailable)", http.StatusBadGateway)
	}

	return proxy
}

// newProxyTransport builds the backend transport with sane timeouts.
func newProxyTransport() *http.Transport {
	return &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   100,
		IdleConnTimeout:       90 * time.Second,
		DisableCompression:    false,
		ExpectContinueTimeout: 1 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		DialContext: (&net.Dialer{
			Timeout:   10 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
	}
}

func clientIPFrom(req *http.Request) string {
	host, _, err := net.SplitHostPort(req.RemoteAddr)
	if err == nil && host != "" {
		return host
	}
	return ""
}

// singleJoiningSlash joins two URL path segments with exactly one slash.
func singleJoiningSlash(a, b string) string {
	aslash := strings.HasSuffix(a, "/")
	bslash := strings.HasPrefix(b, "/")
	switch {
	case aslash && bslash:
		return a + b[1:]
	case !aslash && !bslash:
		return a + "/" + b
	default:
		return a + b
	}
}

func mustParseURL(raw string) *url.URL {
	u, err := url.Parse(raw)
	if err != nil {
		slog.Error("Invalid backend URL", "url", raw, "error", err)
		panic(err)
	}
	return u
}
