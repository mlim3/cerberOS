/// <reference types="vite/client" />

interface ImportMetaEnv {
  readonly VITE_IO_API_BASE?: string
  readonly VITE_ORCHESTRATOR_SSE?: string
}

interface ImportMeta {
  readonly env: ImportMetaEnv
}
