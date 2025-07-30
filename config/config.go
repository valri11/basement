package config

type ServerConfig struct {
	Port               int
	DisableTLS         bool
	TLSCertFile        string
	TLSCertKeyFile     string
	EnableTelemetry    bool
	TelemetryCollector string
}

type Configuration struct {
	Server ServerConfig
}
