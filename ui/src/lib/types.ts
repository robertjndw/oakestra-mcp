// Mirrors docs/api.md in the backend repository (GET /info and GET /activity).

export type ToolKind = 'read' | 'write';
export type Outcome = 'ok' | 'error' | 'denied';

export interface ToolInfo {
  name: string;
  title: string;
  description: string;
  kind: ToolKind;
}

export interface ServerInfo {
  name: string;
  version: string;
  mcp_path: string;
  /** Empty when the server has no PUBLIC_URL configured. */
  public_url: string;
  writes_enabled: boolean;
  user: { username: string; roles: string[]; organization: string };
  tools: ToolInfo[];
}

export interface ActivityItem {
  id: number;
  ts: string;
  user: string;
  organization: string;
  client_name: string;
  tool: string;
  kind: ToolKind;
  /** Already redacted by the server. */
  args: unknown;
  outcome: Outcome;
  error: string;
  duration_ms: number;
  /** Links a prepare_* call to its confirm_action call. Empty for everything else. */
  action_id: string;
}

export interface ActivityPage {
  items: ActivityItem[];
  /** Null on the last page. */
  next_before: number | null;
}
