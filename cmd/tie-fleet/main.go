// tie-fleet administers the runner fleet: status, rollout, job-type
// specs, service autostart, restarts. Admin-only tool: rollout/restart need
// SSH + sudoers rights on the runner hosts. All logic lives in
// github.com/uidbz/tie/fleetcmd; this main pins the tie brand.
package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/uidbz/tie/fleetcmd"
)

func main() {
	if err := fleetcmd.Run(fleetcmd.TieBrand, os.Args[1:]); err != nil {
		if errors.Is(err, fleetcmd.ErrUsage) {
			os.Exit(2)
		}
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}
