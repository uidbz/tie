// tie-smb serves the tie live tag-derived filesystem (the same tree
// `tie mount --db` mounts: /query, /files, /tags) over SMB2/3, pure Go, no
// Samba. One share (default name "tie") mirrors the mount root; accounts come
// from the config's [[Users]] table with read/write roles like the other tie
// servers. See docs/smb.md for layout, name escaping, and client recipes.
package main

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"strings"

	"github.com/go-filesystems/smb"
	"github.com/uidbz/tie/auth"
	"github.com/uidbz/tie/client"
	"github.com/uidbz/tie/internal/daemon"
	"github.com/uidbz/tie/io/smbfs"
	"github.com/uidbz/tie/io/tiefs"
)

// User is a single account entry in the tie-smb config. Passwords are stored
// in plaintext, so the config file must be tightly permissioned (SMB's
// NTLMv2 needs the password itself, or its NT hash, on the server).
type User struct {
	Username string
	Password string
	// Role is "read" (browse and download) or "write" (full modify). Empty
	// defaults to "write", matching tie-filehost.
	Role string
}

// SMBConfig is the full tie-smb configuration, loaded from TOML.
type SMBConfig struct {
	// ListenOn is the SMB transport address. Port 445 is the standard port
	// and needs root or AmbientCapabilities=CAP_NET_BIND_SERVICE (see
	// contrib/systemd/tie-smb.service); any high port works for testing
	// (mount -t cifs -o port=...).
	ListenOn string
	// ServerName is the name the server reports in NTLM challenges
	// (cosmetic; clients display it).
	ServerName string
	// ShareName is the single exported share, mirroring the mount root.
	ShareName string
	// TieConfig is the tie client config (same file `tie -C` takes): the
	// triplestore URL, credentials, namespace, collections and filehosts.
	// A bare name searches the usual config dirs; a path loads it directly.
	TieConfig string
	// Collection selects the entry in the tie config's [Collections]; empty
	// uses the config's default.
	Collection string
	// TripleStoreFile, when non-empty, overrides the tie config's
	// TripleStoreFile: the .tie file is opened in-process instead of using the
	// triplestore URL. Relative paths follow the process working directory.
	TripleStoreFile string
	// ReadOnly opens the database read-only. Every SMB user is then read-only,
	// because write requests would be refused by the database. A ReadOnly
	// already set in the tie config has the same effect: the read-only state
	// that matters is the EFFECTIVE one, tie-smb's flag OR the tie config's
	// (#153).
	ReadOnly bool
	// Host names the filehost (from the tie config's FileHosts) that serves
	// blob reads and receives uploads. Empty selects the collection's first.
	Host string
	// CacheSizeGB is the on-disk blob cache budget in gibibytes.
	CacheSizeGB int
	// Verify hash-checks downloaded bytes against their content address
	// (off for a trusted filehost).
	Verify bool
	// LogFile is the path structured JSON logs are appended to. Empty logs
	// only to stderr (pretty).
	LogFile string
	// LogLevel is the minimum level emitted: debug, info, warn, error.
	LogLevel string
	// Users are the accounts allowed to mount the share. SMB has no
	// anonymous mode here: an empty list refuses every login, so the config
	// must define at least one user.
	Users []User
}

func defaultConfig() SMBConfig {
	return SMBConfig{
		ListenOn:    ":445",
		ServerName:  "tie",
		ShareName:   "tie",
		TieConfig:   "config.toml",
		CacheSizeGB: 1,
	}
}

// applyDirectConfig folds tie-smb's direct-database overrides into the loaded
// tie client config. They win over the tie config's own TripleStoreFile, so a
// service unit can point at its database without editing the shared file.
// After folding, tieCfg.ReadOnly IS the effective read-only state: a ReadOnly
// from the tie config alone survives, and tie-smb's flag can only add to it.
func applyDirectConfig(tieCfg *client.Config, cfg SMBConfig) {
	if cfg.TripleStoreFile != "" {
		tieCfg.TripleStoreFile = cfg.TripleStoreFile
	}
	if cfg.ReadOnly {
		tieCfg.ReadOnly = true
	}
}

// effectiveRole folds the read-only database state into a configured role: a
// "write" session against a read-only database only ever fails at the
// database layer, so the role is downgraded and reported honestly instead
// (#153).
func effectiveRole(role auth.Role, readOnly bool) auth.Role {
	if readOnly && role == auth.RoleWrite {
		return auth.RoleRead
	}
	return role
}

// shareOptionsFor picks the share's writer mask. WriteUsers() with no names
// does NOT mean "nobody may write" — smb's access.go reads an empty writer
// list as "every connection may write" — so a service whose effective state
// has no writers must export the share itself read-only, or the share
// advertises write and clients learn the refusal per operation (#153). The
// second return value describes the choice for the log.
func shareOptionsFor(writeUsers []string) (opts []smb.ShareOption, mode string) {
	if len(writeUsers) == 0 {
		return []smb.ShareOption{smb.ReadOnly()}, "read-only"
	}
	return []smb.ShareOption{smb.WriteUsers(writeUsers...)}, strings.Join(writeUsers, ",")
}

func main() {
	flags := daemon.ParseFlags("tie-smb", "tie-smb.toml", `SMB2/3 server for the tie tag-derived filesystem
--------------------------------------
Configuration (listen address, tie config, accounts) is read from a TOML
file. The share mirrors the "tie mount --db" layout: query, files, tags.
`)

	cfg := defaultConfig()
	if err := daemon.LoadConfig(flags.ConfigPath, &cfg); err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		os.Exit(1)
	}

	cleanup := daemon.SetupLogging("tie-smb", cfg.LogFile, cfg.LogLevel)
	defer cleanup()

	if len(cfg.Users) == 0 {
		slog.Error("please define at least one [[Users]] entry in the config file")
		os.Exit(1)
	}

	// Tie client: same config the tie CLI uses, so the triplestore URL,
	// credentials and filehosts are configured once.
	tieCfg, err := client.LoadConfig(cfg.TieConfig)
	if err != nil {
		slog.Error("reading tie config", "file", cfg.TieConfig, "err", err)
		os.Exit(1)
	}
	applyDirectConfig(&tieCfg, cfg)
	if tieCfg.ReadOnly && tieCfg.TripleStoreFile == "" {
		// The effective read-only state, not just tie-smb's own flag: a
		// ReadOnly from the tie config is equally unbackable without a file
		// (#153).
		slog.Error("ReadOnly requires TripleStoreFile (in this config or the tie config)")
		os.Exit(1)
	}
	tie := client.NewTieClientFor(tieCfg, cfg.Collection)
	if err := tie.Err(); err != nil {
		slog.Error("opening database", "file", tieCfg.TripleStoreFile, "err", err)
		_ = tie.Close()
		os.Exit(1)
	}

	// runSMB returns instead of calling os.Exit: everything it opens (the
	// blob cache, the SMB listener) is released by defers on the way out —
	// os.Exit used to skip the deferred cleanup entirely (#153).
	code := runSMB(cfg, tieCfg, tie)
	_ = tie.Close()
	os.Exit(code)
}

func runSMB(cfg SMBConfig, tieCfg client.Config, tie *client.TieClient) int {
	if _, err := tie.ServerVersion(); err != nil {
		slog.Error("triplestore not reachable", "target", tie.Target(), "err", err)
		return 1
	}
	if tieCfg.TripleStoreFile != "" {
		// A direct open is lazy about the collection, and ServerVersion never
		// touches one, so a database already locked by a running server used
		// to start happily and fail every SMB request. Load the collection
		// now and fail startup with the real reason instead (#153).
		if _, err := tie.Get("tie-smb-startup-load"); err != nil && !errors.Is(err, client.ErrNotFound) {
			slog.Error("loading collection", "file", tieCfg.TripleStoreFile, "err", err)
			return 1
		}
	}
	host, err := tie.ResolveHost(cfg.Host)
	if err != nil {
		slog.Error("resolving filehost", "host", cfg.Host, "err", err)
		return 1
	}
	if tieCfg.ReadOnly {
		slog.Warn("database opened read-only; every SMB user is read-only")
	}

	tree := tiefs.NewTree(tie, host, cfg.CacheSizeGB, cfg.Verify)
	defer tree.Close()
	share := smbfs.NewShare(tree)

	srv := smb.New()
	srv.SetName(cfg.ServerName)
	writeUsers := make([]string, 0, len(cfg.Users))
	for _, u := range cfg.Users {
		role, errRole := auth.ParseUserRole(u.Role)
		if errRole != nil {
			slog.Error("invalid user role in config", "user", u.Username, "err", errRole)
			return 1
		}
		role = effectiveRole(role, tieCfg.ReadOnly)
		srv.AddUser(u.Username, u.Password)
		if role == auth.RoleWrite {
			writeUsers = append(writeUsers, u.Username)
		}
		roleName := "read"
		if role == auth.RoleWrite {
			roleName = "write"
		}
		slog.Info("user", "name", u.Username, "role", roleName)
	}
	// Non-writers get a read-only connection: the server refuses their
	// writes before they reach the filesystem. An empty writer list exports
	// the whole share read-only (see shareOptionsFor).
	shareOpts, writerMode := shareOptionsFor(writeUsers)
	if err := srv.Share(cfg.ShareName, share, shareOpts...); err != nil {
		slog.Error("exporting share", "share", cfg.ShareName, "err", err)
		return 1
	}

	// Listen synchronously so a bind failure (port in use, missing
	// CAP_NET_BIND_SERVICE) exits with a clear error; serve in the
	// background so signals can shut us down gracefully.
	ln, err := net.Listen("tcp", cfg.ListenOn)
	if err != nil {
		slog.Error("listening", "addr", cfg.ListenOn, "err", err)
		return 1
	}
	go func() {
		if err := srv.Serve(ln); err != nil {
			slog.Error("serve", "err", err)
		}
	}()
	slog.Info("listening", "addr", cfg.ListenOn, "share", cfg.ShareName,
		"triplestore", tie.Target(), "filehost", host.URL,
		"writers", writerMode)

	slog.Info("shutting down", "signal", daemon.WaitForSignal())
	// srv.Close drops connections; the smb server closes every open handle
	// on connection teardown, and each handle's Close commits its staged
	// write. Then release the tree (blob cache + staging dir); the client is
	// closed by main.
	if err := srv.Close(); err != nil {
		slog.Error("closing server", "err", err)
	}
	slog.Info("bye")
	return 0
}
