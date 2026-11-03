// Fleet-registry subcommands: `tie-fleet hosts list|approve|remove` talk to
// the seed runner's /api/fleet/* endpoints (AdminToken auth), so new runners
// that joined via the runner's own register mode can be managed without SSH.
// The seed URL comes from the `seed` key in tie-fleet.toml.
package fleetcmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"text/tabwriter"

	runnerapi "github.com/uidbz/tie/runner/api"
)

// fleetAdminToken resolves the seed's AdminToken: --token flag >
// TIE_FLEET_ADMIN_TOKEN env > the seed host's [[Hosts]] Token key (matched
// by API prefix against the seed URL).
func fleetAdminToken(token string, hosts []Host, seed string) (string, error) {
	if token == "" {
		token = os.Getenv(adminTokenEnv())
	}
	if token == "" {
		for _, h := range hosts {
			if h.API == seed || strings.HasPrefix(seed, h.API) {
				token = h.Token
				break
			}
		}
	}
	if token == "" {
		return "", fmt.Errorf(bt("no AdminToken for seed %s: pass --token, set TIE_FLEET_ADMIN_TOKEN, or add Token to its [[Hosts]] entry"), seed)
	}
	return token, nil
}

// fleetHosts GETs the seed's registry list.
func (cl *api) fleetHosts(seed, token string) ([]runnerapi.FleetHost, error) {
	req, err := http.NewRequest(http.MethodGet, seed+"/api/fleet/hosts", nil)
	if err != nil {
		return nil, err
	}
	req.SetBasicAuth(cl.user, token)
	resp, err := cl.c.Do(req)
	if err != nil {
		return nil, fmt.Errorf("seed %s: %w", seed, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("seed %s: HTTP %d: %s", seed, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var hosts []runnerapi.FleetHost
	if err := json.Unmarshal(body, &hosts); err != nil {
		return nil, fmt.Errorf("decode seed reply: %w", err)
	}
	return hosts, nil
}

// fleetApprove / fleetRemove POST {"id":...} to the seed.
func (cl *api) fleetApprove(seed, token, id string) error {
	return cl.fleetPost(seed, "/api/fleet/approve", token, id)
}

func (cl *api) fleetRemove(seed, token, id string) error {
	return cl.fleetPost(seed, "/api/fleet/remove", token, id)
}

func (cl *api) fleetPost(seed, path, token, id string) error {
	body, _ := json.Marshal(map[string]string{"id": id})
	req, err := http.NewRequest(http.MethodPost, seed+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.SetBasicAuth(cl.user, token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := cl.c.Do(req)
	if err != nil {
		return fmt.Errorf("seed %s: %w", seed, err)
	}
	defer resp.Body.Close()
	reply, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("seed %s: HTTP %d: %s", seed, resp.StatusCode, strings.TrimSpace(string(reply)))
	}
	fmt.Println(strings.TrimSpace(string(reply)))
	return nil
}

func formatHostsTable(hosts []runnerapi.FleetHost) string {
	var buf bytes.Buffer
	w := tabwriter.NewWriter(&buf, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tNAME\tURL\tSTATUS\tCAPS")
	for _, h := range hosts {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", h.ID, h.Name, h.URL, h.Status, strings.Join(h.Capabilities, ","))
	}
	w.Flush()
	return buf.String()
}

func runHosts(args []string) error {
	if len(args) < 1 {
		return errors.New(bt("usage: tie-fleet hosts list | approve <id> | remove <id>"))
	}
	var cf commonFlags
	var token string
	fs := flag.NewFlagSet("hosts", flag.ExitOnError)
	cf.register(fs)
	fs.StringVar(&token, "token", "", bt("seed AdminToken (else TIE_FLEET_ADMIN_TOKEN env, else the seed [[Hosts]] Token key)"))
	sub := args[0]
	rest := args[1:]
	// Allow flags before or after the subcommand.
	if err := fs.Parse(rest); err != nil {
		return err
	}
	rest = fs.Args()

	cfg, err := loadFleetConfig(cf.conf)
	if err != nil {
		return err
	}
	if cfg.Seed == "" {
		return errors.New(bt("no 'seed' key in the fleet config: set seed = \"https://<seed-runner>\" in tie-fleet.toml"))
	}
	tok, err := fleetAdminToken(token, cfg.Hosts, cfg.Seed)
	if err != nil {
		return err
	}
	cl, err := newAPI(cf.ca, cfg.AuthUser)
	if err != nil {
		return err
	}

	switch sub {
	case "list":
		hosts, err := cl.fleetHosts(cfg.Seed, tok)
		if err != nil {
			return err
		}
		fmt.Print(formatHostsTable(hosts))
	case "approve":
		if len(rest) != 1 {
			return errors.New(bt("usage: tie-fleet hosts approve <id>"))
		}
		return cl.fleetApprove(cfg.Seed, tok, rest[0])
	case "remove":
		if len(rest) != 1 {
			return errors.New(bt("usage: tie-fleet hosts remove <id>"))
		}
		return cl.fleetRemove(cfg.Seed, tok, rest[0])
	default:
		return errors.New(bt("usage: tie-fleet hosts list | approve <id> | remove <id>"))
	}
	return nil
}
