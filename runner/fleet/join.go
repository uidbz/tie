package fleet

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/uidbz/tie/runner/api"
)

// ErrJoinPending is returned by Join when the wait budget expired and
// the host is still waiting for admin approval. The HostID travels with
// it so the operator can report what to approve.
var ErrJoinPending = errors.New("still pending approval")

// Join posts the registration handshake to the seed and re-posts every
// poll interval until the host is approved (AddPending is idempotent, so
// each post just re-reads the entry). The returned reply carries the
// seed-generated config as raw JSON; writing it is the caller's choice
// (config schemas belong to the runner binary).
func Join(ctx context.Context, seed string, req api.RegisterRequest, poll, maxWait time.Duration, client *http.Client) (*api.RegisterReply, error) {
	if client == nil {
		client = http.DefaultClient
	}
	seed = strings.TrimRight(seed, "/")
	payload, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(maxWait)
	for attempt := 0; ; attempt++ {
		if attempt > 0 {
			if time.Now().After(deadline) {
				return nil, ErrJoinPending
			}
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(poll):
			}
		}
		httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, seed+"/api/fleet/register", bytes.NewReader(payload))
		if err != nil {
			return nil, err
		}
		httpReq.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(httpReq)
		if err != nil {
			return nil, fmt.Errorf("cannot reach seed %s: %w", seed, err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		switch resp.StatusCode {
		case http.StatusUnauthorized:
			return nil, errors.New("join token rejected by seed")
		case http.StatusOK:
		default:
			return nil, fmt.Errorf("seed error (%d): %s", resp.StatusCode, strings.TrimSpace(string(body)))
		}
		var rep api.RegisterReply
		if err := json.Unmarshal(body, &rep); err != nil {
			return nil, fmt.Errorf("bad seed reply: %w", err)
		}
		if rep.Status == api.HostActive && len(rep.Config) > 0 {
			return &rep, nil
		}
	}
}
