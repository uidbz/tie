// Package imagecheck answers one question at submit time: can this
// runner still pull the job's image? Its only job is to turn "docker run
// fails seconds later with manifest unknown" into an actionable error at
// submission.
//
// It fails OPEN: only a definitive 404 from the registry counts as
// missing. An unset registry, transport error, timeout, 5xx, or any
// unexpected status passes — a broken check must never block real jobs.
package imagecheck

import (
	"fmt"
	"net/http"
	"strings"
	"time"
)

// Timeout bounds the registry manifest lookup. Small: this runs inline
// with submission and must never add real latency.
var Timeout = 5 * time.Second

// Check reports whether registry/handler can still be pulled, via the
// registry v2 manifest HEAD endpoint. registry is the configured
// DockerRegistry value (host or host:port, with or without scheme);
// handler the image name:tag as jobs carry it.
func Check(registry, handler string) error {
	return checkWith(http.DefaultClient, registry, handler)
}

func checkWith(client *http.Client, registry, handler string) error {
	if handler == "" {
		return nil // request validation already rejects empty handlers upstream
	}
	if registry == "" {
		return nil
	}
	base := registry
	if !strings.HasPrefix(base, "http://") && !strings.HasPrefix(base, "https://") {
		base = "https://" + base
	}
	base = strings.TrimSuffix(base, "/")

	name, tag := handler, "latest"
	if i := strings.LastIndex(handler, ":"); i > strings.LastIndex(handler, "/") {
		name, tag = handler[:i], handler[i+1:]
	}
	if name == "" || tag == "" {
		return nil
	}

	url := fmt.Sprintf("%s/v2/%s/manifests/%s", base, name, tag)
	req, err := http.NewRequest(http.MethodHead, url, nil)
	if err != nil {
		return nil
	}
	req.Header.Set("Accept", "application/vnd.docker.distribution.manifest.v2+json, "+
		"application/vnd.oci.image.manifest.v1+json, "+
		"application/vnd.oci.image.index.v1+json")
	resp, err := client.Do(req)
	if err != nil {
		return nil // unreachable/slow registry: fail open
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return fmt.Errorf("image '%s' not found in the registry - deploy it first", handler)
	}
	return nil
}
