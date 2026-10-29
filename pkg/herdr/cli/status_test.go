package cli

import (
	"strings"
	"testing"
)

const statusJSON = `{"client":{"version":"0.9.5","channel":"stable","protocol":22,` +
	`"endpoint_protocol_generation":1,"endpoint_capabilities":["surface_interest",` +
	`"presentation_effects_fence","health_check"],"remote_host_bridge":true,` +
	`"remote_bridge_idle_timeout":true,"binary":"/u/.local/bin/herdr","session":"work"},` +
	`"server":{"status":"running","running":true,"version":"0.9.5","protocol":22,` +
	`"capabilities":{"live_handoff":true,"detached_server_daemon":false,` +
	`"endpoint_protocol_generation":1,"surface_interest":true,"health_check":true,` +
	`"ssh_agent_registration":true},"compatible":true,"endpoint_compatible":false,` +
	`"socket":"/tmp/x/sessions/work/herdr.sock","session":"work","restart_needed":false,` +
	`"server_binary_stale":true},"update":{"restart_needed":false,"server_binary_stale":true}}`

const notRunningJSON = `{"client":{"version":"0.9.5","channel":"stable","protocol":22,` +
	`"endpoint_protocol_generation":1,"endpoint_capabilities":[],"remote_host_bridge":true,` +
	`"remote_bridge_idle_timeout":true,"binary":"/u/.local/bin/herdr","session":null},` +
	`"server":{"status":"not_running","running":false,"version":null,"protocol":null,` +
	`"capabilities":null,"compatible":null,"endpoint_compatible":null,` +
	`"socket":"/tmp/x/herdr.sock","session":null,"restart_needed":false,` +
	`"server_binary_stale":false},"update":{"restart_needed":false,"server_binary_stale":false}}`

func TestStatus(t *testing.T) {
	f := newFake(t)
	c := f.cli()
	f.answer("status --json", statusJSON+"\n", "", 0)
	s, err := c.Status(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	cl, sv := s.Client, s.Server
	if cl.Version != "0.9.5" || cl.Channel != "stable" || cl.Protocol != 22 ||
		cl.EndpointProtocolGeneration != 1 || strings.Join(cl.EndpointCapabilities, ",") !=
		"surface_interest,presentation_effects_fence,health_check" || !cl.RemoteHostBridge ||
		!cl.RemoteBridgeIdleTimeout || cl.Binary != "/u/.local/bin/herdr" || cl.Session != "work" {
		t.Errorf("client = %+v", cl)
	}
	if sv.Status != "running" || !sv.Running || sv.Version != "0.9.5" || sv.Protocol != 22 ||
		sv.Socket != "/tmp/x/sessions/work/herdr.sock" || sv.Session != "work" ||
		!is(sv.Compatible, true) || !is(sv.EndpointCompatible, false) ||
		!is(sv.RestartNeeded, false) || !is(sv.ServerBinaryStale, true) {
		t.Errorf("server = %+v", sv)
	}
	want := ServerCapabilities{
		LiveHandoff: true, EndpointProtocolGeneration: 1,
		SurfaceInterest: true, HealthCheck: true, SSHAgentRegistration: true,
	}
	if sv.Capabilities == nil || *sv.Capabilities != want {
		t.Errorf("capabilities = %+v", sv.Capabilities)
	}
	if !is(s.Update.RestartNeeded, false) || !is(s.Update.ServerBinaryStale, true) {
		t.Errorf("update = %+v", s.Update)
	}

	f.answer("status --json", notRunningJSON+"\n", "", 0)
	s, err = c.Status(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	sv = s.Server
	if sv.Running || sv.Status != "not_running" || sv.Version != "" || sv.Protocol != 0 ||
		sv.Capabilities != nil || sv.Compatible != nil || sv.EndpointCompatible != nil ||
		s.Client.Session != "" {
		t.Errorf("not running: %+v", s)
	}

	f.answer("status --json", "", "Error: Custom { kind: Other }\n", 1)
	if _, err := c.Status(t.Context()); err == nil {
		t.Error("exit 1: want an error")
	}
}

func is(b *bool, want bool) bool { return b != nil && *b == want }
