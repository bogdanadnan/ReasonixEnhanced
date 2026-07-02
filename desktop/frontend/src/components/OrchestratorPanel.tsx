import { memo, useEffect, useState } from "react";
import { app } from "../lib/bridge";
import { useT } from "../lib/i18n";
import type { OrchState } from "../lib/types";
import { ChevronRight, CheckCircle, RotateCcw, Gauge, FileText } from "lucide-react";

export const OrchestratorPanel = memo(function OrchestratorPanel({ active }: { active: boolean }) {
  const t = useT();
  const [state, setState] = useState<OrchState | null>(null);
  const [planContent, setPlanContent] = useState("");
  const [expanded, setExpanded] = useState(false);
  const [reviewIndex, setReviewIndex] = useState(0);

  useEffect(() => {
    if (!active) return;
    const poll = setInterval(async () => {
      try {
        const s = await app.OrchState();
        setState(s ?? null);
        if (s && !planContent) {
          const plan = await app.OrchPlanContent();
          setPlanContent(plan);
        }
      } catch { /* bridge not ready */ }
    }, 2000);
    return () => clearInterval(poll);
  }, [active, planContent]);

  if (!state) return null;

  const currentPhase = state.phases?.[state.phase - 1];
  const taskName = (currentPhase?.tasks?.[state.task - 1] ?? "").replace(/\*\*/g, "");
  const totalTasks = state.phases?.reduce((sum, p) => sum + (p?.tasks?.length ?? 0), 0) ?? 0;

  const roleLabel = () => {
    const pct = progress > 0 ? " · " + progress + "%" : "";
    switch (state.status) {
      case "planning": return t("orchestrator.planning") + " · " + (state.plannerLabel || "planner") + pct;
      case "developing": return t("orchestrator.developing") + " · " + (state.developerLabel || "dev") + pct;
      case "reviewing": return t("orchestrator.reviewing") + " · " + (state.reviewerLabel || "reviewer") + pct;
      case "reviewing2": return t("orchestrator.reviewing") + " · " + (state.reviewer2Label || "reviewer2") + pct;
      case "done": return t("orchestrator.done") + " · " + progress + "%";
      default: return state.status;
    }
  };
  const doneTasks = state.phases?.reduce((sum, p) => sum + (p?.done?.length ?? 0), 0) ?? 0;
  const progress = totalTasks > 0 ? Math.round((doneTasks / totalTasks) * 100) : 0;


  return (
    <div className="orchestrator-panel">
      <div className="orchestrator-panel__head">
        <Gauge size={14} />
        <span className="orchestrator-panel__title">
          {currentPhase ? `${currentPhase.name}` : t("orchestrator.title")}
        </span>
        {' · '}
        <span className="orchestrator-panel__status">{roleLabel()}</span>
        <button
          className="orchestrator-panel__toggle"
          onClick={() => setExpanded(!expanded)}
        >
          <ChevronRight size={12} className={expanded ? "orchestrator-panel__chevron--open" : ""} />
        </button>
      </div>

      <div className="orchestrator-panel__progress-bar">
        <div className="orchestrator-panel__progress-fill" style={{ width: `${progress}%` }} />
      </div>

      {taskName && (
        <div className="orchestrator-panel__current">
          {t("orchestrator.currentTask")}: <strong>{taskName}</strong>
        </div>
      )}

      {expanded && (
        <div className="orchestrator-panel__body">
          {/* Plan tree */}
          <details className="orchestrator-panel__section" open>
            <summary>
              <FileText size={12} />
              {t("orchestrator.planTree")}
            </summary>
            <div className="orchestrator-panel__plan">
              {state.phases?.filter(Boolean).map((phase, pi) => (
                <div key={pi} className={`orchestrator-panel__phase ${pi + 1 === state.phase ? "orchestrator-panel__phase--active" : ""}`}>
                  <div className="orchestrator-panel__phase-name">{phase.name}</div>
                  {(phase.tasks || []).map((task, ti) => {
                    const done = (phase.done || []).includes(task);
                    return (
                      <div key={ti} className={`orchestrator-panel__task ${done ? "orchestrator-panel__task--done" : ""}`}>
                        {done ? <CheckCircle size={10} /> : <span className="orchestrator-panel__task-dot" />}
                        <span>{task}</span>
                      </div>
                    );
                  })}
                </div>
              ))}
            </div>
          </details>

          {/* Verdict history */}
          <details className="orchestrator-panel__section">
            <summary>
              <RotateCcw size={12} />
              {t("orchestrator.reviewHistory")}
            </summary>
            <div className="orchestrator-panel__reviews">
              {(() => {
                const items: React.ReactNode[] = [];
                for (let i = 1; i <= totalTasks; i++) {
                  items.push(
                    <div
                      key={i}
                      className={`orchestrator-panel__review ${i === reviewIndex ? "orchestrator-panel__review--selected" : ""}`}
                      onClick={() => setReviewIndex(i)}
                    >
                      {t("orchestrator.review")} #{i}
                    </div>
                  );
                }
                return items.length === 0 ? <div className="orchestrator-panel__empty">{t("orchestrator.noReviews")}</div> : items;
              })()}
            </div>
          </details>
        </div>
      )}
    </div>
  );
});
