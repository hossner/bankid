package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"os"
	"path"
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
	ServiceURL  string   `json:"serviceUrl"`
	PollDelay   int      `json:"pollDelay"`
	LogFileName string   `json:"logFile"`
	LogLevel    int      `json:"logLevel"`
	LogPrefixes []string `json:"logPrefixes"`
}

// New returns a pointer to a new instance of a Config struct, holding values from the config file cfgFileName
func New(cfgFileName string) (*Config, error) {
	myDir, err := filepath.Abs(filepath.Dir(os.Args[0]))
	if err != nil {
		return nil, fmt.Errorf("failed to retrieve working directory: %v", err)
	}
	if cfgFileName == "" {
		cfgFileName = path.Join(myDir, defaultConfigFileName)
	}
	raw, err := ioutil.ReadFile(cfgFileName)
	if err != nil {
		return nil, fmt.Errorf("could not read file %s: %v", cfgFileName, err)
	}
	var s Config
	if err = json.Unmarshal(raw, &s); err != nil {
		return nil, fmt.Errorf("could not unmarshal config file %s: %v", cfgFileName, err)
	}
	s.AppDir = myDir
	if err := s.validate(); err != nil {
		return nil, fmt.Errorf("invalid value in configuration file %s: %v", cfgFileName, err)
	}
	return &s, nil
}

// GetFilePath is used to get the absolute path to the specified item
func (c *Config) GetFilePath(name string) string {
	switch name {
	case "caCertFileName":
		return fixPath(c.AppDir, c.CertStore.CertStorePath, c.CertStore.CACertFileName)
	case "userCertFileName":
		return fixPath(c.AppDir, c.CertStore.CertStorePath, c.CertStore.UserCertFileName)
	case "userPrivateKeyFileName":
		return fixPath(c.AppDir, c.CertStore.CertStorePath, c.CertStore.UserPrivateKeyFileName)
	case "userP12FileName":
		return fixPath(c.AppDir, c.CertStore.CertStorePath, c.CertStore.UserP12FileName)
	case "logFile":
		return fixPath(c.AppDir, "", c.LogFileName)
	default:
		return ""
	}
}

func (c *Config) validate() error {
	if c.PollDelay < minPollDelay {
		return errors.New("pollDelay is too low (needs to be at least " + string(minPollDelay) + ")")
	}
	if c.CertStore.CACertFileName == "" {
		return errors.New("CACertFileName cannot be empty")
	}
	if c.CertStore.UserCertFileName == "" {
		return errors.New("UserCertFileName cannot be empty")
	}
	if c.LogLevel > 0 && c.LogFileName == "" {
		return errors.New("LogFileName cannot be empty if EnableLogging is true")
	}
	return nil
}

func fixPath(rd, d, f string) string {
	if path.IsAbs(f) {
		return f
	}
	if path.IsAbs(d) {
		return path.Join(d, f)
	}
	return path.Join(rd, d, f)
}
