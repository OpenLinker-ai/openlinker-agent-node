package agentnode

// AgentNodeVersion is the implementation identity reported to Core and by
// --version. Release builds inject the exact tag or commit using -ldflags -X.
// It is deliberately not configurable through the process environment.
// Changing an enrolled Node's version requires the supported Core upgrade
// procedure; a development build must never impersonate a released version.
var AgentNodeVersion = "openlinker-agent-node/dev"
