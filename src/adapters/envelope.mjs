export function buildAdapterEnvelope(input, ctx) {
  const envelope = {
    input,
    run_id: ctx.runId,
    metadata: ctx.metadata ?? {},
    a2a: ctx.a2a ?? {},
  };
  if (ctx.helper) {
    envelope.agent_node = {
      helper: ctx.helper,
    };
  }
  return envelope;
}

export function helperEnv(ctx) {
  if (!ctx.helper) return {};
  return {
    OPENLINKER_AGENT_NODE_RUN_ID: ctx.runId,
    OPENLINKER_AGENT_NODE_HELPER_URL: ctx.helper.base_url,
    OPENLINKER_AGENT_NODE_HELPER_TOKEN: ctx.helper.token,
    OPENLINKER_AGENT_NODE_HELPER_CALL_AGENT_URL: ctx.helper.endpoints.call_agent,
    OPENLINKER_AGENT_NODE_HELPER_EVENTS_URL: ctx.helper.endpoints.events,
  };
}
