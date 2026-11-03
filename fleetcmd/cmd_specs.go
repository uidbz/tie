// specs and autostart subcommands. Config edits are textual (configedit.go),
// idempotent, and always back up the file before writing. With --host the
// remote runner config (remote_config key) is fetched, edited locally, diffed, and
// pushed back; without it, a local FILE is edited.
package fleetcmd

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

type stringList []string

func (s *stringList) String() string     { return strings.Join(*s, ",") }
func (s *stringList) Set(v string) error { *s = append(*s, v); return nil }

func runSpecs(args []string) error {
	if len(args) < 1 {
		return errors.New(bt("usage: tie-fleet specs show | specs edit FILE [--set name:cpu:rammb:gpu]... [--drop name]... [--add-cap name] [--host H] [--dry-run]"))
	}
	switch args[0] {
	case "show":
		return specsShow(args[1:])
	case "edit":
		return specsEdit(args[1:])
	default:
		return fmt.Errorf("unknown specs subcommand %q (want show|edit)", args[0])
	}
}

func specsShow(args []string) error {
	var cf commonFlags
	fs := flag.NewFlagSet("specs show", flag.ExitOnError)
	cf.register(fs)
	fs.Parse(args)
	cfg, err := loadFleetConfig(cf.conf)
	if err != nil {
		return err
	}
	cl, err := newAPI(cf.ca, cfg.AuthUser)
	if err != nil {
		return err
	}
	for _, h := range cfg.Hosts {
		caps, err := cl.capabilities(h.API)
		if err != nil {
			fmt.Printf("%-10s DOWN (%v)\n", h.Name, err)
			continue
		}
		fmt.Printf("%-10s %s\n", h.Name, strings.Join(caps.Capabilities, " "))
	}
	return nil
}

func specsEdit(args []string) error {
	var cf commonFlags
	var sets, drops, addCaps stringList
	var host string
	var dryRun bool
	fs := flag.NewFlagSet("specs edit", flag.ExitOnError)
	cf.register(fs)
	fs.Var(&sets, "set", "add/update spec as name:cpu:rammb:gpu (repeatable)")
	fs.Var(&drops, "drop", "remove [JobTypeSpecs.name] and its capability (repeatable)")
	fs.Var(&addCaps, "add-cap", "append capability (repeatable)")
	fs.StringVar(&host, "host", "", "edit this fleet host's runner config (remote_config key) over SSH")
	fs.BoolVar(&dryRun, "dry-run", false, "print the diff, write nothing")
	fs.Parse(args)

	if len(sets)+len(drops)+len(addCaps) == 0 {
		return fmt.Errorf("nothing to do: pass --set/--drop/--add-cap")
	}
	if len(addCaps) > 1 {
		return fmt.Errorf("--add-cap takes one capability")
	}
	specEdits := make([]SpecEdit, 0, len(sets))
	for _, s := range sets {
		e, err := parseSpec(s)
		if err != nil {
			return err
		}
		specEdits = append(specEdits, e)
	}
	addCap := ""
	if len(addCaps) == 1 {
		addCap = addCaps[0]
	}

	if host != "" {
		return specsEditRemote(host, cf, specEdits, []string(drops), addCap, dryRun)
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: specs edit FILE [flags] (or --host H)")
	}
	path := fs.Arg(0)
	src, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	out, changes, err := EditConfig(string(src), specEdits, []string(drops), addCap)
	if err != nil {
		return err
	}
	for _, c := range changes {
		fmt.Println("  " + c)
	}
	if out == string(src) {
		fmt.Println("no changes")
		return nil
	}
	if dryRun {
		printDiff(path, string(src), out)
		return nil
	}
	if err := backupAndWrite(path, out); err != nil {
		return err
	}
	fmt.Printf("wrote %s (runner restart or reload required to take effect)\n", path)
	return nil
}

func specsEditRemote(host string, cf commonFlags, sets []SpecEdit, drops []string, addCap string, dryRun bool) error {
	cfg, err := loadFleetConfig(cf.conf)
	if err != nil {
		return err
	}
	found := filterHosts(cfg.Hosts, host)
	if len(found) == 0 {
		return fmt.Errorf("no fleet host named %q", host)
	}
	ssh := sshRunner{user: cf.sshUser}
	tmp, err := os.CreateTemp("", "runner-config-*.toml")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	tmp.Close()
	// scp remote paths do NOT expand ~, so resolve the home dir over ssh first.
	home, err := ssh.run(found[0].Name, "echo $HOME")
	if err != nil {
		return err
	}
	cfgPath := strings.TrimSpace(home) + "/" + cfg.RemoteConfig
	if err := ssh.copyFrom(found[0].Name, cfgPath, tmp.Name()); err != nil {
		return fmt.Errorf("fetch config from %s: %w", host, err)
	}
	src, err := os.ReadFile(tmp.Name())
	if err != nil {
		return err
	}
	out, changes, err := EditConfig(string(src), sets, drops, addCap)
	for _, c := range changes {
		fmt.Println("  " + c)
	}
	if err != nil {
		return err
	}
	if out == string(src) {
		fmt.Println("no changes")
		return nil
	}
	if dryRun {
		printDiff(host, string(src), out)
		return nil
	}
	if err := os.WriteFile(tmp.Name(), []byte(out), 0o600); err != nil {
		return err
	}
	if _, err := ssh.run(found[0].Name, "cp -a "+cfgPath+" "+cfgPath+".fleet-bak"); err != nil {
		return fmt.Errorf("remote backup: %w", err)
	}
	if err := ssh.copyTo(tmp.Name(), found[0].Name, cfgPath); err != nil {
		return err
	}
	fmt.Printf("wrote %s:%s (backup: %s.fleet-bak; restart or reload required)\n", host, cfgPath, cfgPath)
	return nil
}

func runAutostart(args []string) error {
	if len(args) < 1 {
		return errors.New(bt("usage: tie-fleet autostart list [--host H] | autostart set NAME... [--host H]"))
	}
	var cf commonFlags
	var host string
	fs := flag.NewFlagSet("autostart", flag.ExitOnError)
	cf.register(fs)
	fs.StringVar(&host, "host", "", "fleet host whose config to read/edit (default: local FILE)")
	fs.Parse(args[1:])

	var src string
	var localPath string // set when editing a local FILE
	if host != "" {
		cfg, err := loadFleetConfig(cf.conf)
		if err != nil {
			return err
		}
		found := filterHosts(cfg.Hosts, host)
		if len(found) == 0 {
			return fmt.Errorf("no fleet host named %q", host)
		}
		ssh := sshRunner{user: cf.sshUser}
		out, err := ssh.run(found[0].Name, "cat ~/"+cfg.RemoteConfig)
		if err != nil {
			return err
		}
		src = out
	} else {
		if fs.NArg() < 1 {
			return fmt.Errorf("no FILE given and no --host")
		}
		localPath = fs.Arg(0)
		b, err := os.ReadFile(localPath)
		if err != nil {
			return err
		}
		src = string(b)
	}

	switch args[0] {
	case "list":
		for _, n := range NoAutostart(src) {
			fmt.Println(n)
		}
		return nil
	case "set":
		names := fs.Args()
		if host == "" {
			names = names[1:] // Arg(0) was the FILE
		}
		if len(names) == 0 {
			return fmt.Errorf("autostart set needs service NAMEs")
		}
		out, report, missing := SetAutostart(src, names)
		for _, r := range report {
			fmt.Println("  " + r)
		}
		if len(missing) > 0 {
			return fmt.Errorf("not found in config: %s", strings.Join(missing, ", "))
		}
		if out == src {
			fmt.Println("no changes")
			return nil
		}
		if host != "" {
			cfg, err := loadFleetConfig(cf.conf)
			if err != nil {
				return err
			}
			ssh := sshRunner{user: cf.sshUser}
			tmp, err := os.CreateTemp("", "autostart-*.toml")
			if err != nil {
				return err
			}
			defer os.Remove(tmp.Name())
			if err := os.WriteFile(tmp.Name(), []byte(out), 0o600); err != nil {
				return err
			}
			if _, err := ssh.run(host, "cp -a ~/"+cfg.RemoteConfig+" ~/"+cfg.RemoteConfig+".fleet-bak"); err != nil {
				return err
			}
			if err := ssh.copyTo(tmp.Name(), host, "~/"+cfg.RemoteConfig); err != nil {
				return err
			}
			fmt.Printf("wrote %s:~/%s (takes effect at next restart)\n", host, cfg.RemoteConfig)
			return nil
		}
		return backupAndWrite(localPath, out)
	default:
		return fmt.Errorf("unknown autostart subcommand %q (want list|set)", args[0])
	}
}

func parseSpec(s string) (SpecEdit, error) {
	parts := strings.Split(s, ":")
	if len(parts) != 4 {
		return SpecEdit{}, fmt.Errorf("bad --set %q: want name:cpu:rammb:gpu", s)
	}
	e := SpecEdit{Name: parts[0]}
	if _, err := fmt.Sscanf(parts[1]+":"+parts[2]+":"+parts[3], "%d:%d:%d", &e.CPU, &e.RAMMB, &e.GPU); err != nil {
		return SpecEdit{}, fmt.Errorf("bad --set %q: %w", s, err)
	}
	return e, nil
}

func backupAndWrite(path, content string) error {
	src, err := os.ReadFile(path)
	if err == nil {
		if err := os.WriteFile(path+".bak", src, 0o600); err != nil {
			return fmt.Errorf("backup: %w", err)
		}
	}
	return os.WriteFile(path, []byte(content), 0o600)
}

func printDiff(label, before, after string) {
	f, err := os.CreateTemp("", "diff-*.txt")
	if err != nil {
		return
	}
	defer os.Remove(f.Name())
	f.WriteString(before)
	f.Close()
	cmd := exec.Command("diff", "-u", f.Name(), "-")
	cmd.Stdin = strings.NewReader(after)
	cmd.Stdout = os.Stdout
	fmt.Printf("--- %s\n", label)
	_ = cmd.Run() // diff exits 1 when files differ; that is not an error here
}
