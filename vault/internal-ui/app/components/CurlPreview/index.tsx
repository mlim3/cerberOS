"use client";

import { useState, useCallback } from "react";
import styles from "./CurlPreview.module.css";

interface CurlPreviewProps {
  script: string;
  agent?: string;
  endpoint?: string;
}

export function CurlPreview({
  script,
  agent = "demo",
  endpoint = "http://localhost:8000/execute",
}: CurlPreviewProps) {
  const [copied, setCopied] = useState(false);

  const buildCurl = useCallback(() => {
    const payload = JSON.stringify({ agent, script });
    return `curl -X POST ${endpoint} \\\n  -H 'Content-Type: application/json' \\\n  -d '${payload}'`;
  }, [script, agent, endpoint]);

  const handleCopy = useCallback(() => {
    navigator.clipboard.writeText(buildCurl()).then(() => {
      setCopied(true);
      setTimeout(() => setCopied(false), 1500);
    });
  }, [buildCurl]);

  // Parse payload for syntax highlighting
  const payloadObj = { agent, script };
  const payloadStr = JSON.stringify(payloadObj);

  // Find where the script value starts and ends in the payload string
  const scriptKey = `"script":`;
  const scriptKeyIdx = payloadStr.indexOf(scriptKey);
  const scriptValueStart = scriptKeyIdx + scriptKey.length + 1; // +1 for opening quote
  const scriptValue = JSON.stringify(script); // includes surrounding quotes
  const scriptValueEnd = scriptValueStart + scriptValue.length;

  const payloadBefore = payloadStr.slice(0, scriptValueStart);
  const payloadScript = payloadStr.slice(scriptValueStart, scriptValueEnd);
  const payloadAfter = payloadStr.slice(scriptValueEnd);

  return (
    <div className={styles.container}>
      <div className={styles.toolbar}>
        <span className={styles.label}>curl preview</span>
        <button
          className={`${styles.copyBtn} ${copied ? styles.copied : ""}`}
          onClick={handleCopy}
          title="Copy curl command"
        >
          {copied ? (
            <>
              <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.5" strokeLinecap="round" strokeLinejoin="round">
                <polyline points="20 6 9 17 4 12" />
              </svg>
              copied
            </>
          ) : (
            <>
              <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
                <rect x="9" y="9" width="13" height="13" rx="2" ry="2" />
                <path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1" />
              </svg>
              copy
            </>
          )}
        </button>
      </div>

      <pre className={styles.code}>
        <span className={styles.keyword}>curl</span>
        {" "}
        <span className={styles.flag}>-X POST</span>
        {" "}
        <span className={styles.url}>{endpoint}</span>
        {" \\\n  "}
        <span className={styles.flag}>-H</span>
        {" "}
        <span className={styles.string}>&apos;Content-Type: application/json&apos;</span>
        {" \\\n  "}
        <span className={styles.flag}>-d</span>
        {" '"}
        <span className={styles.string}>{payloadBefore}</span>
        <span className={styles.scriptValue}>{payloadScript}</span>
        <span className={styles.string}>{payloadAfter}</span>
        {"'"}
      </pre>
    </div>
  );
}
