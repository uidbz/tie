package tie

import (
	"fmt"
)

func PrintState() {
	if CurrentState.Verbose {
		fmt.Println("Using host:", CurrentState.Webservice)
		fmt.Println("Current namespace/collection is " + CurrentState.Namespace + "/" + CurrentState.Collection)
	}
}
