// useController is the frontend's state machine over the agent's event stream. It
// reduces the flat WireEvent flow (text/reasoning deltas, tool dispatch/result,
// notices, approvals, usage) into a structured transcript the components render,
// and exposes the command surface (send/cancel/approve/…) that calls back into
// the kernel via the bridge. This is the desktop analogue of the chat TUI's
// update loop — same controller, different renderer.

import { useCallback, useEffect, useReducer } from "react";
import { app, onEvent } from "./bridge";
import type {
  ContextInfo,
  HistoryMessage,
  MemoryView,
  Meta,
  WireApproval,
  WireEvent,
  WireUsage,
} from "./types";

export type ToolStatus = "running" | "done" | "error";

export type Item =
  | { kind: "user"; id: string; text: string }
  | { kind: "assistant"; id: string; text: string; reasoning: string; streaming: boolean }
  | { kind: "phase"; id: string; text: string }
  | { kind: "notice"; id: string; level: "info" | "warn"; text: string }
  | {
      kind: "tool";
      id: string;
      name: string;
      args: string;
      readOnly: boolean;
      status: ToolStatus;
      output?: string;
      error?: string;
      truncated?: boolean;
    };

interface State {
  items: Item[];
  running: boolean;
  approval?: WireApproval;
  usage?: WireUsage;
  context: ContextInfo;
  meta?: Meta;
  // currentAssistant tracks the in-flight assistant item that text/reasoning
  // deltas accumulate into; cleared at turn boundaries.
  currentAssistant?: string;
  // seq is a monotonic id source so React keys stay stable across re-renders.
  seq: number;
}

const initialState: State = {
  items: [],
  running: false,
  context: { used: 0, window: 0 },
  seq: 0,
};

type Action =
  | { type: "event"; e: WireEvent }
  | { type: "user"; text: string }
  | { type: "meta"; meta: Meta }
  | { type: "context"; context: ContextInfo }
  | { type: "history"; messages: HistoryMessage[] }
  | { type: "clearApproval" }
  | { type: "reset" };

// ensureAssistant returns the items array containing the active assistant item
// (creating one if the turn hasn't produced text yet), its id, and the next seq.
function ensureAssistant(s: State): { items: Item[]; id: string; seq: number } {
  if (s.currentAssistant) {
    const exists = s.items.some((it) => it.id === s.currentAssistant && it.kind === "assistant");
    if (exists) return { items: s.items, id: s.currentAssistant, seq: s.seq };
  }
  const id = `a${s.seq}`;
  const item: Item = { kind: "assistant", id, text: "", reasoning: "", streaming: true };
  return { items: [...s.items, item], id, seq: s.seq + 1 };
}

function applyEvent(s: State, e: WireEvent): State {
  switch (e.kind) {
    case "turn_started":
      return { ...s, running: true, currentAssistant: undefined };

    case "text":
    case "reasoning": {
      const { items, id, seq } = ensureAssistant(s);
      const delta = e.text ?? e.reasoning ?? "";
      const next = items.map((it) =>
        it.kind === "assistant" && it.id === id
          ? e.kind === "text"
            ? { ...it, text: it.text + delta }
            : { ...it, reasoning: it.reasoning + delta }
          : it,
      );
      return { ...s, items: next, currentAssistant: id, seq };
    }

    case "message": {
      const { items, id, seq } = ensureAssistant(s);
      const next = items.map((it) =>
        it.kind === "assistant" && it.id === id
          ? { ...it, text: e.text ?? it.text, reasoning: e.reasoning ?? it.reasoning, streaming: false }
          : it,
      );
      return { ...s, items: next, currentAssistant: undefined, seq };
    }

    case "tool_dispatch": {
      const t = e.tool;
      if (!t) return s;
      const id = t.id || `tool${s.seq}`;
      const item: Item = {
        kind: "tool",
        id,
        name: t.name,
        args: t.args ?? "",
        readOnly: t.readOnly,
        status: "running",
      };
      return { ...s, seq: s.seq + 1, items: [...s.items, item] };
    }

    case "tool_result": {
      const t = e.tool;
      if (!t) return s;
      const next = [...s.items];
      // Match the dispatched card by id; if the kernel omitted one, fall back to
      // the most recent still-running tool.
      let idx = t.id ? next.findIndex((it) => it.kind === "tool" && it.id === t.id) : -1;
      if (idx < 0) {
        for (let i = next.length - 1; i >= 0; i--) {
          const cand = next[i];
          if (cand.kind === "tool" && cand.status === "running") {
            idx = i;
            break;
          }
        }
      }
      if (idx >= 0) {
        const it = next[idx];
        if (it.kind === "tool") {
          next[idx] = {
            ...it,
            status: t.err ? "error" : "done",
            output: t.output,
            error: t.err,
            truncated: t.truncated,
          };
        }
      }
      return { ...s, items: next };
    }

    case "usage": {
      const used = e.usage && s.context.window ? e.usage.promptTokens : s.context.used;
      return { ...s, usage: e.usage, context: { ...s.context, used } };
    }

    case "notice":
      return {
        ...s,
        seq: s.seq + 1,
        items: [...s.items, { kind: "notice", id: `n${s.seq}`, level: e.level ?? "info", text: e.text ?? "" }],
      };

    case "phase":
      return {
        ...s,
        seq: s.seq + 1,
        items: [...s.items, { kind: "phase", id: `p${s.seq}`, text: e.text ?? "" }],
      };

    case "approval_request":
      return { ...s, approval: e.approval };

    case "turn_done": {
      const finalized = s.items.map((it) =>
        it.kind === "assistant" && it.streaming ? { ...it, streaming: false } : it,
      );
      const items: Item[] = e.err
        ? [...finalized, { kind: "notice", id: `e${s.seq}`, level: "warn", text: e.err }]
        : finalized;
      return { ...s, items, running: false, currentAssistant: undefined, approval: undefined, seq: s.seq + 1 };
    }
  }
}

function reducer(s: State, a: Action): State {
  switch (a.type) {
    case "user":
      return {
        ...s,
        seq: s.seq + 1,
        running: true,
        items: [...s.items, { kind: "user", id: `u${s.seq}`, text: a.text }],
      };
    case "meta":
      return { ...s, meta: a.meta };
    case "context":
      return { ...s, context: a.context };
    case "history": {
      // Only user/assistant turns with visible text — never the system prompt or
      // tool-result messages, and not the empty content of a tool-call-only turn.
      const visible = a.messages.filter(
        (m) => (m.role === "user" || m.role === "assistant") && m.content.trim() !== "",
      );
      const items: Item[] = visible.map((m, i) =>
        m.role === "user"
          ? { kind: "user", id: `h${i}`, text: m.content }
          : { kind: "assistant", id: `h${i}`, text: m.content, reasoning: "", streaming: false },
      );
      return { ...s, items, seq: s.seq + visible.length };
    }
    case "clearApproval":
      return { ...s, approval: undefined };
    case "reset":
      return { ...initialState, meta: s.meta, context: { ...s.context, used: 0 } };
    case "event":
      return applyEvent(s, a.e);
  }
}

export function useController() {
  const [state, dispatch] = useReducer(reducer, initialState);

  useEffect(() => {
    const off = onEvent((e) => {
      dispatch({ type: "event", e });
      // The gauge's denominator (window) and post-turn prompt size come from the
      // kernel, not the stream — refresh once a turn settles.
      if (e.kind === "turn_done") {
        app
          .ContextUsage()
          .then((context) => dispatch({ type: "context", context }))
          .catch(() => {});
      }
    });

    void (async () => {
      try {
        dispatch({ type: "meta", meta: await app.Meta() });
        dispatch({ type: "context", context: await app.ContextUsage() });
        const history = await app.History();
        if (history && history.length) dispatch({ type: "history", messages: history });
      } catch {
        // Bound methods unavailable (pre-startup / build error) — ignore; Meta's
        // startupErr surfaces the reason once it's reachable.
      }
    })();

    return off;
  }, []);

  const send = useCallback((text: string) => {
    dispatch({ type: "user", text });
    app.Submit(text).catch(() => {});
  }, []);

  const cancel = useCallback(() => {
    app.Cancel().catch(() => {});
  }, []);

  const approve = useCallback((id: string, allow: boolean, session: boolean) => {
    dispatch({ type: "clearApproval" });
    app.Approve(id, allow, session).catch(() => {});
  }, []);

  const setPlan = useCallback((on: boolean) => {
    app.SetPlanMode(on).catch(() => {});
  }, []);

  const newSession = useCallback(async () => {
    await app.NewSession().catch(() => {});
    dispatch({ type: "reset" });
  }, []);

  const compact = useCallback(() => {
    app.Compact().catch(() => {});
  }, []);

  // Memory panel actions. fetchMemory re-reads the loaded snapshot; remember and
  // saveDoc mutate then return so the caller can re-fetch to reflect the change.
  const fetchMemory = useCallback((): Promise<MemoryView> => {
    return app.Memory().catch(
      () => ({ docs: [], facts: [], scopes: [], storeDir: "", available: false }),
    );
  }, []);

  const remember = useCallback(async (scope: string, note: string) => {
    await app.Remember(scope, note).catch(() => {});
  }, []);

  const saveDoc = useCallback(async (path: string, body: string) => {
    await app.SaveDoc(path, body).catch(() => {});
  }, []);

  return {
    state,
    send,
    cancel,
    approve,
    setPlan,
    newSession,
    compact,
    fetchMemory,
    remember,
    saveDoc,
  };
}
