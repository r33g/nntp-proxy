/*
 * Copyright (c) 2017. akarl
 *
 *
 */

package config

type Configuration struct {
	Frontend frontendConfig
	Backend  []backendConfig
	Users    []user
	SelectedBackend
}

type frontendConfig struct {
	FrontendAddr            string             `json:"frontendAddr"`
	FrontendPort            string             `json:"frontendPort"`
	FrontendTLS             bool               `json:"frontendTLS"`
	FrontendTLSCert         string             `json:"frontendTLSCert"`
	FrontendTLSKey          string             `json:"frontendTLSKey"`
	FrontendHTTPAddr        string             `json:"frontendHTTPAddr"`
	FrontendHTTPPort        string             `json:"frontendHTTPPort"`
	FrontendAllowedCommands []frontendCommands `json:"frontendAllowedCommands"`
}

type frontendCommands struct {
	FrontendCommand string `json:"frontendCommand"`
}

type backendConfig struct {
	BackendName  string `json:"backendName"`
	BackendAddr  string `json:"backendAddr"`
	BackendPort  string `json:"backendPort"`
	BackendTLS   bool   `json:"backendTLS"`
	BackendUser  string `json:"backendUser"`
	BackendPass  string `json:"backendPass"`
	BackendConns int    `json:"backendConns"`
}

type user struct {
	Username string `json:"Username"`
	Password string `json:"Password"`
}

type SelectedBackend struct {
	BackendName string
	BackendAddr string
	BackendPort string
	BackendTLS  bool
	BackendUser string
	BackendPass string
}
