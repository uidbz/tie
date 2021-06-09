package tie

import (
	"fmt"
	"os/exec"
	"strings"
)

func PrintState() {
	if CurrentState.Verbose {
		fmt.Println("Using host:", CurrentState.Webservice)
		fmt.Println("Current namespace/collection is " + CurrentState.Namespace + "/" + CurrentState.Collection)
	}
}

func GetVideoHeight(source string) string {
	//ffprobe -v error -select_streams v:0 -show_entries stream=height -of default=nw=1:nk=1
	args := []string{
		"-v", "error",
		"-select_streams", "v:0",
		"-show_entries", "stream=height",
		"-of", "default=noprint_wrappers=1:nokey=1",
		source,
	}
	cmd := exec.Command("ffprobe", args...)
	if out, err := cmd.Output(); err != nil {
		fmt.Println(err)
		return ""
	} else {
		return strings.TrimSpace(string(out)) + "p"
	}
}
