package tie

import (
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"os/user"
	"strconv"
	"strings"

	"path/filepath"

	"gopkg.in/resty.v1"

	"git.sr.ht/~uid/putlib"
	"git.sr.ht/~uid/tie/metadata"
	"git.sr.ht/~uid/tie/request"
)

var (
	CurrentState State
	Config       string
	ConfigDir    string
	ConfigPath   string
)

const (
	tieKey = "A00102030405060708090A0B0C0D0E0FF0E0D0C0B0A090807060504030201000"
)

func InitKey() []byte {
	k, err := hex.DecodeString(tieKey)
	if err != nil {
		fmt.Printf("Cannot decode hex key: %v", err) // add error handling
		return nil
	}

	return k
}

func InitConfig() {
	usr, err := user.Current()
	if err != nil {
		log.Fatal(err)
	}
	ConfigDir = filepath.Join(usr.HomeDir, ".config", "tie")
	ConfigPath = ConfigDir + "/" + Config + ".json"

	if _, err := os.Stat(ConfigPath); os.IsNotExist(err) {
		if _, err2 := os.Stat(ConfigDir); os.IsNotExist(err2) {
			if os.Mkdir(ConfigDir, 0777) != nil {
				panic("Can't create " + ConfigDir + "\nExiting...")
			}
		}
		// Default values
		s := State{
			Webservice:    "https://localhost:1161",
			Namespace:     "Collections",
			Collection:    "Main",
			ServeUrl:      "http://localhost:1162",
			DataHost:      "/data",
			ThumbnailHost: "/mnt/thumbnails",
		}
		CurrentState = s

		SaveJSON(ConfigPath, CurrentState)
		PrintState()
	} else {
		LoadJSON(ConfigPath, &CurrentState)
		CurrentState.Key = InitKey()
		PrintState()
	}
}

func TieAdd(Entry1, Relation, Entry2 string, addHandler func(json.RawMessage)) {
	// type Association struct {
	// 	Key    string
	// 	Value1 string
	// 	Value2 string
	// }
	a := request.Add{
		Key:    Entry1,
		Value1: Relation,
		Value2: Entry2,
	}
	b, _ := json.Marshal(a)
	SendToWebservice("Add", b, addHandler)
}

func Tag(path string, tags []string, options TagOptions, addHandler func(json.RawMessage)) {
	putlib.JsonOutput = true
	putlib.ForceGenerateThumbnails = options.PutlibForceGenerateThumbnails
	output, _ := putlib.Upload(CurrentState.ServeUrl+"/upload", CurrentState.Key, path)
	if output != "" {
		info := metadata.Info{}
		if err := json.Unmarshal([]byte(output), &info); err != nil {
			fmt.Println("Unmashal error:", err.Error())
		} else {
			// uid := metalib.HashFunction + "/" + info.MediaType + "/" + info.Hash
			base := filepath.Base(path)
			TieAdd("file", "highway-hash", info.Hash, addHandler)
			TieAdd(info.Hash, "filename", base, addHandler)
			TieAdd(info.Hash, "media-type", info.MediaType, addHandler)
			if options.AddOriginalPath {
				TieAdd(info.Hash, "original-path", path, addHandler)
			}
			for _, x := range tags {
				TieAdd(info.Hash, "tag", x, addHandler)
			}
			if len(tags) != 0 {
				fmt.Println(info.Hash, "tag", tags)
			} else {
				fmt.Println(info.Hash)
			}
			p := strings.Split(info.MediaType, "/")
			if len(p) == 2 {
				switch p[0] {
				case "video":

				case "image":

				case "audio":
					audio := metadata.Audio{}
					if err := json.Unmarshal([]byte(output), &audio); err == nil {
						if audio.Album != "" {
							TieAdd(audio.Hash, "album", audio.Album, addHandler)
						}
						if audio.Artist != "" {
							TieAdd(audio.Hash, "artist", audio.Artist, addHandler)
						}
						if audio.Title != "" {
							TieAdd(audio.Hash, "title", audio.Title, addHandler)
						}
						if audio.Track != 0 {
							TieAdd(audio.Hash, "track", strconv.Itoa(audio.Track), addHandler)
						}
						if audio.Year != 0 {
							TieAdd(audio.Hash, "year", strconv.Itoa(audio.Year), addHandler)
						}
					}
				}
			}
		}
	}
}

func printOutput(resp *resty.Response, err error) {
	if CurrentState.Verbose {
		fmt.Println("Response from Web Service:")
		fmt.Println(string(resp.Body()))
	}
	if err != nil {
		fmt.Println("Error communicating with web service:", resp, err)
	}
}

func SendToWebservice(command string, body json.RawMessage, handler func(json.RawMessage)) {
	if len(CurrentState.Webservice) < 5 {
		return
	}
	var r *resty.Client
	if CurrentState.Webservice[0:5] == "https" {
		t := tls.Config{}
		t.InsecureSkipVerify = true // Not so good. Temp hack for self-signed certificates.
		r = resty.SetTLSClientConfig(&t)
	} else {
		r = resty.New()
	}
	resp, err := r.R().
		SetHeader("Content-Type", "application/json").
		SetBody(body).
		SetResult(AuthSuccess{}).
		Post(CurrentState.Webservice + "/" + CurrentState.Namespace + "/" + CurrentState.Collection + "/" + command)

	printOutput(resp, err)
	handler(resp.Body())
}

func LoadJSON(inputFile string, dest interface{}) bool {
	file, err := os.Open(inputFile)

	if err != nil {
		log.Fatal(err)
		return false
	} else {
		input, err2 := io.ReadAll(file)
		json.Unmarshal(input, &dest)
		if err2 != nil {
			log.Fatal(err2)
			return false
		} else {
			if CurrentState.Verbose {
				log.Println("Loading succeeded: " + inputFile)
			}
			return true
		}
	}
}

func SaveJSON(outputFile string, source interface{}) bool {
	b, _ := json.Marshal(source)
	err := os.WriteFile(outputFile, b, 0766)
	if err != nil {
		log.Fatal(err)
		return false
	} else {
		return true
	}
}
