package agentnode

type AdapterEnvelope struct {
	Input        any                  `json:"input"`
	RunID        string               `json:"run_id"`
	Metadata     JSONMap              `json:"metadata"`
	A2A          JSONMap              `json:"a2a"`
	Conversation *ConversationContext `json:"conversation,omitempty"`
	AgentNode    *AgentNodeIO         `json:"agent_node,omitempty"`
}

type AgentNodeIO struct {
	Helper *HelperInfo `json:"helper,omitempty"`
}

func buildAdapterEnvelope(input any, runCtx RunContext) AdapterEnvelope {
	envelope := AdapterEnvelope{
		Input:        input,
		RunID:        runCtx.RunID,
		Metadata:     runCtx.Metadata,
		A2A:          runCtx.A2A,
		Conversation: runCtx.Conversation,
	}
	if runCtx.Helper != nil {
		envelope.AgentNode = &AgentNodeIO{Helper: runCtx.Helper}
	}
	return envelope
}

func helperEnv(runCtx RunContext) []string {
	if runCtx.Helper == nil {
		return nil
	}
	return []string{
		"OPENLINKER_AGENT_NODE_RUN_ID=" + runCtx.RunID,
		"OPENLINKER_AGENT_NODE_HELPER_URL=" + runCtx.Helper.BaseURL,
		"OPENLINKER_AGENT_NODE_HELPER_TOKEN=" + runCtx.Helper.Token,
		"OPENLINKER_AGENT_NODE_HELPER_CALL_AGENT_URL=" + runCtx.Helper.Endpoints.CallAgent,
		"OPENLINKER_AGENT_NODE_HELPER_EVENTS_URL=" + runCtx.Helper.Endpoints.Events,
	}
}
