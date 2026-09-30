package config

type ServerConfig struct {
	Port               int
	DisableTLS         bool
	TLSCertFile        string
	TLSCertKeyFile     string
	DisableTelemetry   bool
	TelemetryCollector string
	LogLevel           string
	Environment        string
}

type Configuration struct {
	Server ServerConfig
}
