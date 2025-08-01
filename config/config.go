package config

type ServerConfig struct {
	Port               int
	DisableTLS         bool
	TLSCertFile        string
	TLSCertKeyFile     string
	DisableTelemetry   bool
	TelemetryCollector string
}

type Configuration struct {
	Server ServerConfig
}
