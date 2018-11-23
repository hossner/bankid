package config

import (
	"encoding/json"
	"errors"
	"io/ioutil"
	"os"
	"path/filepath"
)

const (
	defaultConfigFileName = "config.json"
	minPollDelay          = 2000
)

// Config holds all config parameters from the config file
type Config struct {
	AppDir    string
	CertStore struct {
		CertStorePath          string `json:"certStorePath"`
		UserPrivateKeyPassword string `json:"userPrivateKeyPassword"`
		CACertFileName         string `json:"caCertFileName"`
		UserCertFileName       string `json:"userCertFileName"`
		UserPrivateKeyFileName string `json:"userPrivateKeyFileName"`
		UserP12FileName        string `json:"userP12FileName"`
	} `json:"certStore"`
	HTTPClientConfig struct {
		RequestHeader struct {
			Host        string `json:"Host"`
			ContentType string `json:"Content-type"`
		} `json:"requestHeader"`
	} `json:"httpClientConfig"`
	ServiceURL    string `json:"serviceUrl"`
	PollDelay     int    `json:"pollDelay"`
	LogFileName   string `json:"logFile"`
	EnableLogging bool   `json:"enableLogging"`
}

// GetConfig is used to retrieve config from file.
// Returns a pointer to a new instance of a Confog struct
func GetConfig(cfgFileName string) (*Config, error) {
	myDir, _ := filepath.Abs(filepath.Dir(os.Args[0]))
	if cfgFileName == "" {
		cfgFileName = myDir + "/" + defaultConfigFileName
	}
	raw, err := ioutil.ReadFile(cfgFileName)
	if err != nil {
		return nil, err
	}
	var s Config
	if err = json.Unmarshal(raw, &s); err != nil {
		return nil, err
	}
	s.AppDir = myDir
	if err := s.validate(); err != nil {
		return nil, err
	}
	return &s, nil
}

// GetFilePath is used to get the absolute path to the specified item
func (c *Config) GetFilePath(name string) string {
	// Todo: If a absolute path is provided in the config file, then that should be returned. Otherwise
	// the path should be relative to the location of the executable
	switch name {
	case "caCertFileName":
		return c.AppDir + "/" + c.CertStore.CertStorePath + "/" + c.CertStore.CACertFileName
	case "userCertFileName":
		return c.AppDir + "/" + c.CertStore.CertStorePath + "/" + c.CertStore.UserCertFileName
	case "userPrivateKeyFileName":
		return c.AppDir + "/" + c.CertStore.CertStorePath + "/" + c.CertStore.UserPrivateKeyFileName
	case "userP12FileName":
		return c.AppDir + "/" + c.CertStore.CertStorePath + "/" + c.CertStore.UserP12FileName
	case "logFile":
		return c.AppDir + "/" + c.LogFileName
	default:
		return ""
	}
}

func (c *Config) validate() error {
	if c.PollDelay < minPollDelay {
		return errors.New("pollDelay in config too low (needs to be at least " + string(minPollDelay) + " ms)")
	}
	return nil
}
