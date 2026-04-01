"use client";

import styles from "./ScriptEditor.module.css";

interface ScriptEditorProps {
  value: string;
  onChange: (value: string) => void;
  onClear: () => void;
}

export function ScriptEditor({ value, onChange, onClear }: ScriptEditorProps) {
  const handleDrop = (e: React.DragEvent) => {
    e.preventDefault();
    const file = e.dataTransfer.files?.[0];
    if (!file) return;
    const reader = new FileReader();
    reader.onload = (ev) => onChange(ev.target?.result as string);
    reader.readAsText(file);
  };

  const lineCount = value.split("\n").length;

  return (
    <div className={styles.container}>
      <div className={styles.toolbar}>
        <div className={styles.toolbarLeft}>
          <span className={styles.label}>script</span>
        </div>
        <div className={styles.toolbarRight}>
          <span className={styles.lineCount}>{lineCount} line{lineCount !== 1 ? "s" : ""}</span>
          {value && (
            <button className={styles.iconBtn} onClick={onClear} title="Clear editor">
              <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
                <line x1="18" y1="6" x2="6" y2="18" />
                <line x1="6" y1="6" x2="18" y2="18" />
              </svg>
              clear
            </button>
          )}
        </div>
      </div>

      <div className={styles.editorWrap} onDrop={handleDrop} onDragOver={(e) => e.preventDefault()}>
        <div className={styles.gutter}>
          {Array.from({ length: lineCount }, (_, i) => (
            <span key={i} className={styles.lineNum}>{i + 1}</span>
          ))}
        </div>
        <textarea
          className={styles.textarea}
          value={value}
          onChange={(e) => onChange(e.target.value)}
          placeholder={"#!/bin/sh\necho {{API_KEY}}"}
          spellCheck={false}
          autoComplete="off"
          autoCorrect="off"
        />
      </div>
    </div>
  );
}
