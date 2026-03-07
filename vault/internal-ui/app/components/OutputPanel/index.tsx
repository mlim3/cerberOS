"use client";

import styles from "./OutputPanel.module.css";

export type ExecutionStatus = "idle" | "running" | "success" | "error";

interface OutputPanelProps {
  status: ExecutionStatus;
  output: string;
  exitCode: number | null;
  durationMs: number | null;
  onClear: () => void;
}

export function OutputPanel({ status, output, exitCode, durationMs, onClear }: OutputPanelProps) {
  const statusLabel: Record<ExecutionStatus, string> = {
    idle: "idle",
    running: "running",
    success: "success",
    error: "error",
  };

  return (
    <div className={styles.container}>
      <div className={styles.toolbar}>
        <div className={styles.toolbarLeft}>
          <span className={styles.label}>output</span>
          <span className={`${styles.statusBadge} ${styles[`status_${status}`]}`}>
            {status === "running" && <span className={styles.spinner} />}
            {statusLabel[status]}
          </span>
          {exitCode !== null && (
            <span className={`${styles.exitCode} ${exitCode === 0 ? styles.exitOk : styles.exitFail}`}>
              exit {exitCode}
            </span>
          )}
          {durationMs !== null && (
            <span className={styles.duration}>{durationMs}ms</span>
          )}
        </div>
        {output && (
          <button className={styles.iconBtn} onClick={onClear}>
            <svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
              <polyline points="3 6 5 6 21 6" />
              <path d="M19 6l-1 14a2 2 0 0 1-2 2H8a2 2 0 0 1-2-2L5 6" />
              <path d="M10 11v6M14 11v6" />
              <path d="M9 6V4h6v2" />
            </svg>
            clear
          </button>
        )}
      </div>

      <div className={styles.outputWrap}>
        {status === "idle" && !output ? (
          <div className={styles.emptyState}>
            <svg width="32" height="32" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1" strokeLinecap="round" strokeLinejoin="round" opacity="0.3">
              <polyline points="4 17 10 11 4 5" />
              <line x1="12" y1="19" x2="20" y2="19" />
            </svg>
            <span>output will appear here</span>
          </div>
        ) : (
          <pre className={styles.pre}>
            <code>{output || (status === "running" ? "booting VM..." : "")}</code>
          </pre>
        )}
      </div>
    </div>
  );
}
