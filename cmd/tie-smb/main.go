// tie-smb serves the tie live tag-derived filesystem (the same tree
// `tie mount --db` mounts: /query, /files, /tags) over SMB2/3, pure Go, no
// Samba. One share (default name "tie") mirrors the mount root; accounts come
// from the config's [[Users]] table with read/write roles like the other tie
// servers. See docs/smb.md for layout, name escaping, and client recipes.
package main

import (
	"fmt"
	"log/slog"
	"net"
	"os"

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
	tie := client.NewTieClientFor(tieCfg, cfg.Collection)
	if _, err := tie.ServerVersion(); err != nil {
		slog.Error("triplestore not reachable", "url", tieCfg.TripleStoreURL, "err", err)
		os.Exit(1)
	}
	host, err := tie.ResolveHost(cfg.Host)
	if err != nil {
		slog.Error("resolving filehost", "host", cfg.Host, "err", err)
		os.Exit(1)
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
			os.Exit(1)
		}
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
	// writes before they reach the filesystem.
	shareOpts := []smb.ShareOption{smb.WriteUsers(writeUsers...)}
	if err := srv.Share(cfg.ShareName, share, shareOpts...); err != nil {
		slog.Error("exporting share", "share", cfg.ShareName, "err", err)
		os.Exit(1)
	}

	// Listen synchronously so a bind failure (port in use, missing
	// CAP_NET_BIND_SERVICE) exits with a clear error; serve in the
	// background so signals can shut us down gracefully.
	ln, err := net.Listen("tcp", cfg.ListenOn)
	if err != nil {
		slog.Error("listening", "addr", cfg.ListenOn, "err", err)
		os.Exit(1)
	}
	go func() {
		if err := srv.Serve(ln); err != nil {
			slog.Error("serve", "err", err)
		}
	}()
	slog.Info("listening", "addr", cfg.ListenOn, "share", cfg.ShareName,
		"triplestore", tieCfg.TripleStoreURL, "filehost", host.URL,
		"writeUsers", len(writeUsers))

	slog.Info("shutting down", "signal", daemon.WaitForSignal())
	// srv.Close drops connections; the smb server closes every open handle
	// on connection teardown, and each handle's Close commits its staged
	// write. Then release the tree (blob cache + staging dir).
	if err := srv.Close(); err != nil {
		slog.Error("closing server", "err", err)
	}
	slog.Info("bye")
}
