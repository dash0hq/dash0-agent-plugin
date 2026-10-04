export type CheckStatus = 'critical' | 'degraded' | 'resolved' | 'unknown'

export type FailedCheck = {
  id: string
  status: CheckStatus
  summary: string
  rule: string
  startedAt?: number
  /** Absent while the check is still failing ("ongoing"). */
  endedAt?: number
  services: string[]
  owner?: string
  priority?: string
}

export type Agent0Thread = { id: string; name: string; updatedAt?: number }

export type Snapshot = {
  checks: FailedCheck[]
  threads: Agent0Thread[]
  /** The "View in Dash0" link the failed-checks tool returns; carries org + dataset. */
  checksUrl?: string
  fetchedAt: number
  /** Set when a source failed; the bar shows it instead of pretending to be live. */
  error?: string
}

export type View = 'overview' | 'checks' | 'agent0'

declare module 'claude-code' {
  interface PluginState {
    'dash0-live': { snapshot: Snapshot | null; view: View; isBandHidden: boolean }
  }
}
