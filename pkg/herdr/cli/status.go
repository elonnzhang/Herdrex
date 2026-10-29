package cli

import "context"

// Status is the output of herdr status --json (src/cli/status.rs).
type Status struct {
	Client ClientStatus `json:"client"`
	Server ServerStatus `json:"server"`
	Update UpdateStatus `json:"update"`
}

// ClientStatus describes the herdr binary that ran the command.
type ClientStatus struct {
	Version                    string   `json:"version"`
	Channel                    string   `json:"channel"`  // "stable" or "preview"
	Protocol                   uint32   `json:"protocol"` // direct terminal protocol
	EndpointProtocolGeneration uint32   `json:"endpoint_protocol_generation"`
	EndpointCapabilities       []string `json:"endpoint_capabilities"`
	RemoteHostBridge           bool     `json:"remote_host_bridge"`
	RemoteBridgeIdleTimeout    bool     `json:"remote_bridge_idle_timeout"`
	Binary                     string   `json:"binary"`
	Session                    string   `json:"session"` // empty for the default session
}

// ServerStatus describes the server at the resolved API socket. Version
// through EndpointCompatible are empty or nil when it is not running.
type ServerStatus struct {
	Status             string              `json:"status"` // "running" or "not_running"
	Running            bool                `json:"running"`
	Version            string              `json:"version"`
	Protocol           uint32              `json:"protocol"` // 0 when unknown
	Capabilities       *ServerCapabilities `json:"capabilities"`
	Compatible         *bool               `json:"compatible"` // Protocol matches the client's
	EndpointCompatible *bool               `json:"endpoint_compatible"`
	Socket             string              `json:"socket"`
	Session            string              `json:"session"` // empty for the default session
	RestartNeeded      *bool               `json:"restart_needed"`
	ServerBinaryStale  *bool               `json:"server_binary_stale"`
}

// ServerCapabilities are the capabilities the server reports in its ping.
type ServerCapabilities struct {
	LiveHandoff                bool   `json:"live_handoff"`
	DetachedServerDaemon       bool   `json:"detached_server_daemon"`
	EndpointProtocolGeneration uint32 `json:"endpoint_protocol_generation"` // 0 when unknown
	SurfaceInterest            bool   `json:"surface_interest"`
	HealthCheck                bool   `json:"health_check"`
	SSHAgentRegistration       bool   `json:"ssh_agent_registration"`
}

// UpdateStatus says whether the running server should be restarted; nil when
// unknown.
type UpdateStatus struct {
	RestartNeeded     *bool `json:"restart_needed"`
	ServerBinaryStale *bool `json:"server_binary_stale"`
}

// Status runs herdr status --json. A server that is not running is not an
// error: Status.Server.Running is false.
func (c *CLI) Status(ctx context.Context) (*Status, error) {
	var s Status
	if err := c.RunJSON(ctx, &s, "status", "--json"); err != nil {
		return nil, err
	}
	return &s, nil
}
