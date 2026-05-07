"use client";

import styles from "./Header.module.css";

interface StatusDot {
  label: string;
  active: boolean;
}

interface HeaderProps {
  engineStatus: "idle" | "running" | "error";
  secretstoreStatus: "idle" | "running" | "error";
}

export function Header({ engineStatus, secretstoreStatus }: HeaderProps) {
  const statuses: StatusDot[] = [
    { label: "engine", active: engineStatus !== "error" },
    { label: "secretstore", active: secretstoreStatus !== "error" },
  ];

  return (
    <header className={styles.header}>
      <div className={styles.brand}>
        <div className={styles.logo}>
          <svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
            <rect x="3" y="11" width="18" height="11" rx="2" ry="2" />
            <path d="M7 11V7a5 5 0 0 1 10 0v4" />
          </svg>
        </div>
        <span className={styles.brandName}>cerberOS</span>
        <span className={styles.brandSub}>vault</span>
      </div>

<div className={styles.services}>
        {statuses.map(({ label, active }) => (
          <div key={label} className={styles.service}>
            <span className={`${styles.dot} ${active ? styles.dotActive : styles.dotError}`} />
            <span className={styles.serviceLabel}>{label}</span>
          </div>
        ))}
      </div>
    </header>
  );
}
