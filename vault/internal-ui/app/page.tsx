"use client";

import { useState, useCallback } from "react";
import styles from "./page.module.css";
import {
  Header,
  ScriptEditor,
  OutputPanel,
  ExecuteButton,
  CurlPreview,
  ExecutionStatus,
} from "./components";

export default function Home() {
  const [script, setScript] = useState("");
  const [status, setStatus] = useState<ExecutionStatus>("idle");
  const [output, setOutput] = useState("");
  const [exitCode, setExitCode] = useState<number | null>(null);
  const [durationMs, setDurationMs] = useState<number | null>(null);

  const handleClearScript = useCallback(() => {
    setScript("");
  }, []);

  const handleClearOutput = useCallback(() => {
    setOutput("");
    setExitCode(null);
    setDurationMs(null);
    setStatus("idle");
  }, []);

  const execute = useCallback(async () => {
    if (!script.trim()) return;

    setStatus("running");
    setOutput("");
    setExitCode(null);
    setDurationMs(null);

    const startTime = Date.now();

    try {
      const res = await fetch("/execute", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ script }),
      });

      const elapsed = Date.now() - startTime;

      if (!res.ok) {
        const text = await res.text().catch(() => `HTTP ${res.status}`);
        setOutput(text);
        setStatus("error");
        setDurationMs(elapsed);
        return;
      }

      const data = await res.json();
      const result = data.response ?? data;
      setOutput(result.output ?? "");
      setExitCode(result.exit_code ?? null);
      setDurationMs(elapsed);
      setStatus(result.exit_code === 0 ? "success" : "error");
    } catch (err) {
      const elapsed = Date.now() - startTime;
      setOutput(err instanceof Error ? err.message : String(err));
      setStatus("error");
      setDurationMs(elapsed);
    }
  }, [script]);

  return (
    <div className={styles.page}>
      <Header engineStatus="idle" secretstoreStatus="idle" />

      <main className={styles.main}>
        <div className={styles.leftCol}>
          <ScriptEditor
            value={script}
            onChange={setScript}
            onClear={handleClearScript}
          />
          <CurlPreview script={script} />
          <div className={styles.actions}>
            <ExecuteButton
              status={status}
              disabled={!script.trim()}
              onClick={execute}
            />
          </div>
        </div>

        <div className={styles.rightCol}>
          <OutputPanel
            status={status}
            output={output}
            exitCode={exitCode}
            durationMs={durationMs}
            onClear={handleClearOutput}
          />
        </div>
      </main>
    </div>
  );
}
