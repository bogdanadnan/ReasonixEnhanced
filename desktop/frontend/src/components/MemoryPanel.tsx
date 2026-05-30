import { useState } from "react";
import type { MemoryView } from "../lib/types";

// MemoryPanel is the desktop memory manager: a right-side drawer over the loaded
// REASONIX.md hierarchy and saved auto-memories. Unlike Claude Code's /memory
// (which shells out to $EDITOR) it edits docs in place, and unlike Codex (no UI
// at all) it shows the saved facts. Docs are editable; facts are read-only
// (the model owns them via the `remember` tool). Quick-add mirrors the "#"
// shortcut with an explicit scope selector.
export function MemoryPanel({
  view,
  onClose,
  onRemember,
  onSaveDoc,
}: {
  view: MemoryView | null;
  onClose: () => void;
  onRemember: (scope: string, note: string) => Promise<void> | void;
  onSaveDoc: (path: string, body: string) => Promise<void> | void;
}) {
  const [note, setNote] = useState("");
  const [scope, setScope] = useState("");
  const [editingPath, setEditingPath] = useState<string | null>(null);
  const [draft, setDraft] = useState("");
  const [busy, setBusy] = useState(false);

  const scopes = view?.scopes ?? [];
  // Default the scope selector to "project" when present, else the first option.
  const activeScope =
    scope || scopes.find((s) => s.scope === "project")?.scope || scopes[0]?.scope || "project";

  const submitNote = async () => {
    const trimmed = note.trim();
    if (!trimmed || busy) return;
    setBusy(true);
    try {
      await onRemember(activeScope, trimmed);
      setNote("");
    } finally {
      setBusy(false);
    }
  };

  const startEdit = (path: string, body: string) => {
    setEditingPath(path);
    setDraft(body);
  };

  const saveEdit = async () => {
    if (editingPath === null || busy) return;
    setBusy(true);
    try {
      await onSaveDoc(editingPath, draft);
      setEditingPath(null);
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="drawer-backdrop" onClick={onClose}>
      <aside className="drawer" onClick={(e) => e.stopPropagation()}>
        <header className="drawer__head">
          <div className="drawer__title">Memory</div>
          <button className="chip" onClick={onClose} title="Close">
            ✕
          </button>
        </header>

        {!view?.available ? (
          <div className="empty">Memory unavailable.</div>
        ) : (
          <div className="drawer__body">
            {/* Quick-add: scope selector + note, mirroring the "#" shortcut. */}
            <section className="mem-section">
              <div className="mem-section__title">Quick add</div>
              <div className="mem-add">
                <select
                  className="mem-select"
                  value={activeScope}
                  onChange={(e) => setScope(e.target.value)}
                  title="Where to save"
                >
                  {scopes.map((s) => (
                    <option key={s.scope} value={s.scope}>
                      {s.scope}
                    </option>
                  ))}
                </select>
                <input
                  className="mem-input"
                  placeholder="Remember that…"
                  value={note}
                  onChange={(e) => setNote(e.target.value)}
                  onKeyDown={(e) => {
                    if (e.key === "Enter") void submitNote();
                  }}
                />
                <button
                  className="btn btn--primary btn--small"
                  onClick={() => void submitNote()}
                  disabled={busy || !note.trim()}
                >
                  Remember
                </button>
              </div>
              <div className="mem-hint">
                {scopes.find((s) => s.scope === activeScope)?.path}
              </div>
            </section>

            {/* Doc files — editable in place. */}
            <section className="mem-section">
              <div className="mem-section__title">Instruction files</div>
              {view.docs.length === 0 && (
                <div className="mem-empty">No REASONIX.md found. Quick-add one above.</div>
              )}
              {view.docs.map((d) => {
                const editing = editingPath === d.path;
                return (
                  <div className="mem-doc" key={d.path}>
                    <div className="mem-doc__head">
                      <span className={`badge badge--${d.scope}`}>{d.scope}</span>
                      <span className="mem-doc__path" title={d.path}>
                        {d.path}
                      </span>
                      {!editing && (
                        <button
                          className="btn btn--small"
                          onClick={() => startEdit(d.path, d.body)}
                        >
                          Edit
                        </button>
                      )}
                    </div>
                    {editing ? (
                      <div className="mem-doc__edit">
                        <textarea
                          className="mem-textarea"
                          value={draft}
                          onChange={(e) => setDraft(e.target.value)}
                          spellCheck={false}
                        />
                        <div className="mem-doc__actions">
                          <button
                            className="btn btn--small"
                            onClick={() => setEditingPath(null)}
                            disabled={busy}
                          >
                            Cancel
                          </button>
                          <button
                            className="btn btn--primary btn--small"
                            onClick={() => void saveEdit()}
                            disabled={busy}
                          >
                            Save
                          </button>
                        </div>
                      </div>
                    ) : (
                      <pre className="mem-doc__body">{d.body}</pre>
                    )}
                  </div>
                );
              })}
            </section>

            {/* Saved auto-memories — read-only; the model owns these. */}
            <section className="mem-section">
              <div className="mem-section__title">Saved memories</div>
              {view.facts.length === 0 ? (
                <div className="mem-empty">
                  Nothing saved yet. The agent writes these with the remember tool.
                </div>
              ) : (
                view.facts.map((f) => (
                  <div className="mem-fact" key={f.name} title={f.body}>
                    <span className={`badge badge--${f.type}`}>{f.type}</span>
                    <div className="mem-fact__text">
                      <div className="mem-fact__name">{f.name}</div>
                      <div className="mem-fact__desc">{f.description}</div>
                    </div>
                  </div>
                ))
              )}
              {view.storeDir && (
                <div className="mem-hint" title={view.storeDir}>
                  stored under {view.storeDir}
                </div>
              )}
            </section>
          </div>
        )}
      </aside>
    </div>
  );
}
