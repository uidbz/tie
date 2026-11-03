// cert status / cert push: fleet-wide TLS cert visibility and remote
// rotation. `cert status` reads CertNotAfter from each runner's
// /api/capabilities and flags expiries inside certWarnDays. `cert push`
// uploads a fresh PEM pair via POST /api/cert (AdminToken auth); the runner
// hot-swaps it without a restart, so service containers keep running.
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
	"time"
)

const certWarnDays = 30

// certStatusRow renders one host's cert line; near-expiry gets EXPIRES.
func certStatusRow(name, notAfter string) string {
	if notAfter == "" {
		return fmt.Sprintf("%-10s no TLS cert", name)
	}
	t, err := time.Parse(time.RFC3339, notAfter)
	if err != nil {
		return fmt.Sprintf("%-10s cert expiry unreported (%q)", name, notAfter)
	}
	days := int(time.Until(t).Hours() / 24)
	line := fmt.Sprintf("%-10s cert expires %s (%dd left)", name, t.Format("2006-01-02"), days)
	if days < certWarnDays {
		line += "  << EXPIRES SOON"
	}
	return line
}

// certPush uploads a PEM cert+key pair to one runner.
func (cl *api) certPush(apiBase, token, certPEM, keyPEM string) error {
	body, _ := json.Marshal(map[string]string{"cert": certPEM, "key": keyPEM})
	req, err := http.NewRequest(http.MethodPost, apiBase+"/api/cert", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.SetBasicAuth(cl.user, token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := cl.c.Do(req)
	if err != nil {
		return fmt.Errorf("%s: %w", apiBase, err)
	}
	defer resp.Body.Close()
	reply, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: HTTP %d: %s", apiBase, resp.StatusCode, strings.TrimSpace(string(reply)))
	}
	fmt.Println(strings.TrimSpace(string(reply)))
	return nil
}

func runCert(args []string) error {
	if len(args) < 1 {
		return errors.New(bt("usage: tie-fleet cert status | cert push --only H --crt FILE --key FILE"))
	}
	var cf commonFlags
	var only, crtFile, keyFile, token string
	fs := flag.NewFlagSet("cert", flag.ExitOnError)
	cf.register(fs)
	fs.StringVar(&only, "only", "", "restrict to one host")
	fs.StringVar(&crtFile, "crt", "", "PEM certificate file (push)")
	fs.StringVar(&keyFile, "key", "", "PEM key file (push)")
	fs.StringVar(&token, "token", "", bt("AdminToken (else TIE_FLEET_ADMIN_TOKEN env, else the [[Hosts]] token key)"))
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}

	cfg, err := loadFleetConfig(cf.conf)
	if err != nil {
		return err
	}
	hosts := filterHosts(cfg.Hosts, only)
	if len(hosts) == 0 {
		return fmt.Errorf("no fleet host matches --only %q", only)
	}
	cl, err := newAPI(cf.ca, cfg.AuthUser)
	if err != nil {
		return err
	}

	switch args[0] {
	case "status":
		for _, h := range hosts {
			caps, err := cl.capabilities(h.API)
			if err != nil {
				fmt.Printf("%-10s DOWN (%v)\n", h.Name, err)
				continue
			}
			fmt.Println(certStatusRow(h.Name, caps.CertNotAfter))
		}
		return nil
	case "push":
		if crtFile == "" || keyFile == "" {
			return fmt.Errorf("cert push needs --crt and --key")
		}
		certPEM, err := os.ReadFile(crtFile)
		if err != nil {
			return err
		}
		keyPEM, err := os.ReadFile(keyFile)
		if err != nil {
			return err
		}
		for _, h := range hosts {
			tok := token
			if tok == "" {
				tok = os.Getenv(adminTokenEnv())
			}
			if tok == "" {
				tok = h.Token
			}
			if tok == "" {
				return fmt.Errorf(bt("%s: no AdminToken (pass --token, set TIE_FLEET_ADMIN_TOKEN, or add Token to its [[Hosts]] entry)"), h.Name)
			}
			fmt.Printf("%s: pushing cert ...\n", h.Name)
			if err := cl.certPush(h.API, tok, string(certPEM), string(keyPEM)); err != nil {
				return err
			}
		}
		return nil
	default:
		return errors.New(bt("usage: tie-fleet cert status | cert push --only H --crt FILE --key FILE"))
	}
}
