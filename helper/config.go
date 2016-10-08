package helper

import (
	"encoding/json"
	"os"
)

type Config struct {
	S3Domain         string // Domain name of YIG
	Region           string // Region name this instance belongs to, e.g cn-bj-1
	IamEndpoint      string // le IAM endpoint address
	IamKey           string
	IamSecret        string
	LogPath          string
	PanicLogPath     string
	PidFile          string
	BindApiAddress   string
	BindAdminAddress string
	SSLKeyPath       string
	SSLCertPath      string
	ZookeeperAddress string
}

var CONFIG Config

func SetupConfig() {
	f, err := os.Open("/etc/yig/yig.json")
	if err != nil {
		panic("Cannot open yig.json")
	}
	defer f.Close()

	err = json.NewDecoder(f).Decode(&CONFIG)
	if err != nil {
		panic("Failed to parse yig.json: " + err.Error())
	}
}
