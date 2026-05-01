package daemon

import (
	"github.com/cmdr-tool/cmdr/internal/agent"
	"github.com/cmdr-tool/cmdr/internal/agentoverride"
)

// loadOverrides delegates to the shared agentoverride package. Kept as a
// daemon-package symbol so daemon startup doesn't need to know about the
// underlying mechanism.
func loadOverrides() {
	agentoverride.Load()
}

// resolveAgent resolves the agent + system prompt + output format for a
// task type, falling back to the daemon's default agent (`agt`) when no
// override exists.
func resolveAgent(taskType string) (agent.Agent, string, string) {
	return agentoverride.Resolve(taskType, agt.Name())
}
