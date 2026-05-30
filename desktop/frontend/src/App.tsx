import { useCallback, useState } from "react";
import { SquarePen, Brain } from "lucide-react";
import { useController } from "./lib/useController";
import { Transcript } from "./components/Transcript";
import { Composer } from "./components/Composer";
import { ApprovalModal } from "./components/ApprovalModal";
import { StatusBar } from "./components/StatusBar";
import { MemoryPanel } from "./components/MemoryPanel";
import type { MemoryView } from "./lib/types";

export default function App() {
  const { state, send, cancel, approve, setPlan, newSession, fetchMemory, remember, saveDoc } =
    useController();
  const [plan, setPlanLocal] = useState(false);
  const [memView, setMemView] = useState<MemoryView | null>(null);

  const togglePlan = () => {
    const next = !plan;
    setPlanLocal(next);
    setPlan(next);
  };

  // Memory drawer: opening fetches a fresh snapshot; writes re-fetch so the
  // panel reflects what landed on disk.
  const openMemory = useCallback(async () => {
    setMemView(await fetchMemory());
  }, [fetchMemory]);

  const closeMemory = useCallback(() => setMemView(null), []);

  const onRemember = useCallback(
    async (scope: string, note: string) => {
      await remember(scope, note);
      setMemView(await fetchMemory());
    },
    [remember, fetchMemory],
  );

  const onSaveDoc = useCallback(
    async (path: string, body: string) => {
      await saveDoc(path, body);
      setMemView(await fetchMemory());
    },
    [saveDoc, fetchMemory],
  );

  return (
    <div className="app">
      <header className="topbar">
        <span className="topbar__model">{state.meta?.label ?? "…"}</span>
        <div className="topbar__spacer" />
        <button className="chip chip--icon" onClick={() => void openMemory()} title="Memory">
          <Brain size={13} />
        </button>
        <button
          className={`chip ${plan ? "chip--on" : ""}`}
          onClick={togglePlan}
          title="Plan mode — refuse all writes"
        >
          plan
        </button>
        <button className="chip chip--icon" onClick={newSession} title="New session">
          <SquarePen size={13} />
        </button>
      </header>

      {state.meta?.startupErr && (
        <div className="banner banner--error">startup error: {state.meta.startupErr}</div>
      )}

      <main className="main">
        <Transcript items={state.items} onPrompt={send} />
      </main>

      <footer className="footer">
        <Composer running={state.running} onSend={send} onCancel={cancel} />
        <StatusBar meta={state.meta} context={state.context} running={state.running} plan={plan} />
      </footer>

      {state.approval && (
        <ApprovalModal
          approval={state.approval}
          onAnswer={(allow, session) => approve(state.approval!.id, allow, session)}
        />
      )}

      {memView !== null && (
        <MemoryPanel
          view={memView}
          onClose={closeMemory}
          onRemember={onRemember}
          onSaveDoc={onSaveDoc}
        />
      )}
    </div>
  );
}
