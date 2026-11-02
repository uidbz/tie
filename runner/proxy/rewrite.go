package proxy

import (
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"regexp"
	"strings"
)

// Body rewrite scopes: how far the reverse proxy reaches into a service's
// response bodies. The scope is reported per service by Config.RewriteScope.
const (
	// ScopeAll rewrites HTML, XML, CSS and JS bodies (default).
	// Right for apps that emit root-absolute URLs everywhere and know nothing
	// about being mounted under /service/{name}.
	ScopeAll = "all"
	// ScopeHTML rewrites only HTML/XML bodies. Use it for prefix-aware
	// backends that build their own /service/{name} URLs inside their JS and
	// CSS bundles: prefixing those literals a second time yields a doubled
	// prefix at runtime (route tables, asset preload helpers, API base paths),
	// and the join happens in the browser where no server-side collapse pass
	// can repair it.
	ScopeHTML = "html"
	// ScopeNone leaves response bodies alone. Location headers and
	// Set-Cookie Path attributes are still rewritten.
	ScopeNone = "none"
)

// scopeFor resolves the body rewrite scope configured for a service.
// Unset means the default (all); an unknown value warns and also falls
// back to all, so a typo cannot silently disable rewriting.
func (h *Handler) scopeFor(service string) string {
	raw := ""
	if h.cfg.RewriteScope != nil {
		raw = h.cfg.RewriteScope(service)
	}
	switch scope := strings.ToLower(strings.TrimSpace(raw)); scope {
	case "", ScopeAll:
		return ScopeAll
	case ScopeHTML, ScopeNone:
		return scope
	default:
		slog.Warn("Unknown rewrite scope value, using default",
			"service", service, "value", raw, "using", ScopeAll)
		return ScopeAll
	}
}

// bodyRewriteNeeded reports whether a response of this content type gets its
// body read and rewritten under the given scope.
func bodyRewriteNeeded(scope, contentType string) bool {
	isHTML := strings.Contains(contentType, "text/html")
	isXML := strings.Contains(contentType, "text/xml") || strings.Contains(contentType, "application/xml")
	if isHTML || isXML {
		return scope != ScopeNone
	}
	isCSS := strings.Contains(contentType, "text/css")
	isJS := strings.Contains(contentType, "javascript") // text/javascript, application/x-javascript, ...
	return (isCSS || isJS) && scope == ScopeAll
}

// rewriteSetCookiePath prefixes a Set-Cookie Path attribute with the service
// prefix so backend cookies (e.g. Path=/app) are visible to the browser
// under /service/{name}/.... Without this, app session cookies are never
// returned and stateful server-rendered apps silently lose their session
// once session-ID URL rewriting stops.
func rewriteSetCookiePath(cookie, prefix string) string {
	i := strings.Index(strings.ToLower(cookie), "path=")
	if i < 0 {
		// no Path attribute: browser defaults to the request directory, already under the prefix
		return cookie
	}
	valStart := i + len("path=")
	valEnd := strings.Index(cookie[valStart:], ";")
	if valEnd < 0 {
		valEnd = len(cookie) - valStart
	}
	path := cookie[valStart : valStart+valEnd]
	if path == "" || strings.HasPrefix(path, "/service/") {
		// already namespaced (idempotency / other-service cookies): leave alone
		return cookie
	}
	var newPath string
	if path == "/" {
		newPath = prefix
	} else {
		newPath = prefix + path
	}
	return cookie[:valStart] + newPath + cookie[valStart+valEnd:]
}

// rewriteHTMLPaths rewrites HTML content to fix both relative and absolute URLs
// by injecting a <base> tag for relative URLs and rewriting absolute paths
// If cleanURLs is true, strips service prefix from generated URLs (for proxy access)
// injectBase adds the <base> tag only for HTML; XML (JSF partial responses) gets
// path rewriting only.
// scanQuotedLiterals enables the quoted root-relative literal rule ('/path',
// "/path", url("/path")) for CSS/JS bodies. It stays off for HTML/XML: an HTML
// body's inline <script> builds URLs at runtime (fetch(prefix + '/login')), so
// prefixing that literal cannot be repaired by the collapse below — the join
// happens in the browser and the doubled prefix only shows up as a failed
// request. Standalone CSS/JS bodies are scanned instead, which is where the
// url("/path") style references actually live.
func rewriteHTMLPaths(body []byte, baseURL, servicePrefix string, cleanURLs bool, injectBase, scanQuotedLiterals bool) []byte {
	// Use simple string replacement to avoid regex backtracking issues
	bodyStr := string(body)

	// Extract the site's own domain from baseURL
	var siteDomain, siteHost string
	if parsedBase, err := url.Parse(baseURL); err == nil {
		siteDomain = parsedBase.Scheme + "://" + parsedBase.Host + "/"
		siteHost = parsedBase.Hostname()
	}

	if !cleanURLs {
		// Add service prefix for direct access (not through proxy)
		// Rewrite absolute URLs pointing at THIS site: https://own.host/path → /service/{name}/path
		// Foreign hosts (e.g. xmlns="http://www.w3.org/1999/xhtml") must be left
		// alone — mangling them corrupts XML namespaces and outbound links.
		urlPattern := regexp.MustCompile(`(https?://[^/]+)/`)
		bodyStr = urlPattern.ReplaceAllStringFunc(bodyStr, func(match string) string {
			if strings.Contains(match, "/service/") {
				return match
			}
			host := match
			if i := strings.Index(host, "://"); i >= 0 {
				host = host[i+3:]
			}
			host = strings.TrimSuffix(host, "/")
			if h, _, err := net.SplitHostPort(host); err == nil {
				host = h
			}
			if host != siteHost && host != "localhost" && host != "127.0.0.1" {
				return match
			}
			return servicePrefix + "/"
		})

		// Rewrite root-relative paths: href="/path" → href="/service/{name}/path"
		bodyStr = strings.ReplaceAll(bodyStr, `="/`, `="`+servicePrefix+`/`)
		bodyStr = strings.ReplaceAll(bodyStr, `='/`, `='`+servicePrefix+`/`)

		// Spaced attribute assignments: href = "/path" (some server-rendered
		// app frameworks build anchors with spaces around '=', which defeats
		// the strict ="/ match).
		spacedAssign := regexp.MustCompile(`=\s+(['"])/([A-Za-z0-9_~.+-])`)
		bodyStr = spacedAssign.ReplaceAllString(bodyStr, `=$1`+servicePrefix+`/$2`)

		// Quoted root-relative path literals in CSS/JS bodies:
		// url("/path"), ('/path'), url: '/path'. A path character must follow
		// the slash, so separator strings like '/' and protocol-relative
		// '//' are left alone. Already-prefixed matches double the prefix,
		// which the collapse below then repairs.
		if scanQuotedLiterals {
			quotedRoot := regexp.MustCompile(`(['"])/([A-Za-z0-9_~.+-])`)
			bodyStr = quotedRoot.ReplaceAllString(bodyStr, "$1"+servicePrefix+"/$2")
		}

		// Unquoted CSS url(/path) form.
		bodyStr = strings.ReplaceAll(bodyStr, `url(/`, `url(`+servicePrefix+`/`)

		// Fix any double-rewrites (stacked rules can nest the prefix more
		// than once, so collapse until stable).
		for strings.Contains(bodyStr, servicePrefix+servicePrefix+"/") {
			bodyStr = strings.ReplaceAll(bodyStr, servicePrefix+servicePrefix+"/", servicePrefix+"/")
		}
	} else {
		// Clean URLs mode: only strip the site's OWN domain, keep external links intact
		// Replace https://own.host/path → /path
		// But keep https://other.host/path → https://other.host/path
		if siteDomain != "" {
			// Simple string replacement for the site's domain only
			bodyStr = strings.ReplaceAll(bodyStr, siteDomain, "/")
		}
		// Root-relative paths are already clean, no rewriting needed
	}

	body = []byte(bodyStr)

	// Inject base tag AFTER rewriting to avoid rewriting the base tag itself
	baseTag := fmt.Sprintf(`<base href="%s">`, baseURL)

	if injectBase {
		// Try to inject after <head> tag
		headRegex := regexp.MustCompile(`(?i)(<head[^>]*>)`)
		if headRegex.Match(body) {
			replacement := fmt.Sprintf(`$1%s`, baseTag)
			body = headRegex.ReplaceAll(body, []byte(replacement))
		} else {
			// If no <head> tag, try after <html> tag
			htmlRegex := regexp.MustCompile(`(?i)(<html[^>]*>)`)
			if htmlRegex.Match(body) {
				replacement := fmt.Sprintf(`$1%s`, baseTag)
				body = htmlRegex.ReplaceAll(body, []byte(replacement))
			} else {
				// If neither, prepend to the beginning of the document
				body = append([]byte(baseTag), body...)
			}
		}
	}

	return body
}
