"use client";

import styles from "./ExecuteButton.module.css";
import { ExecutionStatus } from "../OutputPanel";

interface ExecuteButtonProps {
  status: ExecutionStatus;
  disabled: boolean;
  onClick: () => void;
}

export function ExecuteButton({ status, disabled, onClick }: ExecuteButtonProps) {
  const isRunning = status === "running";

  return (
    <button
      className={`${styles.btn} ${isRunning ? styles.running : ""}`}
      disabled={disabled || isRunning}
      onClick={onClick}
    >
      {isRunning ? (
        <>
          <span className={styles.spinner} />
          executing...
        </>
      ) : (
        <>
          <svg width="14" height="14" viewBox="0 0 24 24" fill="currentColor">
            <polygon points="5 3 19 12 5 21 5 3" />
          </svg>
          execute
        </>
      )}
    </button>
  );
}
