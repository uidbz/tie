package main

import (
	"context"
	"errors"
	"testing"

	"github.com/urfave/cli/v3"
)

// TestBeforeIsTheOnlyConfigGuard: the root Before hook refuses every
// config-requiring command when the config failed to load, and stays out of the
// way of the ones that do not need it. Before this, 31 commands carried the
// guard themselves and several of them forgot it.
func TestBeforeIsTheOnlyConfigGuard(t *testing.T) {
	broken := &app{} // no config loaded: a.tie is nil

	run := func(args ...string) error {
		root := &cli.Command{
			Name:   "tie",
			Before: broken.before,
			Commands: []*cli.Command{
				{Name: "get", Action: func(context.Context, *cli.Command) error {
					t.Error("the action ran without a config")
					return nil
				}},
				{Name: "version", Action: func(context.Context, *cli.Command) error { return nil }},
			},
		}
		return root.Run(context.Background(), args)
	}

	err := run("tie", "get", "somekey")
	var ee exitError
	if err == nil {
		t.Fatal("`tie get` ran without a config")
	}
	if !errors.As(err, &ee) || ee.code != 2 {
		t.Errorf("`tie get`: err = %v, want an exit-2 error", err)
	}

	if err := run("tie", "version"); err != nil {
		t.Errorf("`tie version` must work without a config: %v", err)
	}
}

// TestConfigExemptNames: the exemption list is about commands, not flags or
// near-misses.
func TestConfigExemptNames(t *testing.T) {
	for _, name := range []string{"version", "conf", "help", "completion", ""} {
		if !configExempt(name) {
			t.Errorf("%q should be exempt", name)
		}
	}
	for _, name := range []string{"get", "import", "verify", "mount", "Get", "conf-x"} {
		if configExempt(name) {
			t.Errorf("%q should not be exempt", name)
		}
	}
}
