# HTTP API

The backend listens on `LISTEN_ADDR` (default `:8090`). Every route except `/healthz` needs
`Authorization: Bearer <token>`, where the token is an Oakestra access token or refresh token.
The dashboard UI reaches these routes as `/addon-assets/oakestra-mcp/api/<route>`; the UI
container's nginx strips the `/api` prefix.

## `GET /healthz`

`200 {"status":"ok"}`. No auth.

## `POST /mcp`

MCP Streamable HTTP endpoint (stateless, JSON responses).

## `GET /info`

```json
{
  "name": "oakestra-mcp",
  "version": "0.1.0",
  "mcp_path": "/mcp",
  "public_url": "http://root.example:8090/mcp",
  "writes_enabled": false,
  "user": {"username": "Admin", "roles": ["Admin"], "organization": "..."},
  "tools": [
    {"name": "list_clusters", "title": "List clusters", "summary": "...", "description": "...", "kind": "read"}
  ]
}
```

`public_url` comes from `PUBLIC_URL` and is empty when unset. `kind` is `read` or `write`.
`summary` is a one-line description for people; `description` is the text the agent gets in
`tools/list` and is deliberately detailed.
Write tools (`prepare_deploy_application`, `prepare_scale_service`, `prepare_undeploy_service`,
`prepare_delete_application`, `confirm_action`) are listed even when writes are disabled, so the
UI can show them as unavailable; `writes_enabled` says whether `/mcp` serves them right now (see
`/settings`). The `prepare_*`
tools count as `write` because they belong to the write workflow, although they change nothing in
Oakestra on their own.

## `GET /settings`, `PUT /settings`

The settings an Admin can change at runtime. Any authenticated user may read them; only `Admin`
may write them (otherwise `403`).

```json
{"writes_enabled": true}
```

`PUT` takes the same object; `writes_enabled` is required and unknown fields are rejected with
`400`. The response is the stored settings. A change applies to the next `/mcp` request without a
restart and is kept in `$DATA_DIR/settings.json`. Until that file exists, `MCP_ENABLE_WRITES`
provides the initial value.

## `GET /activity`

Query parameters, all optional: `limit` (default 50, max 200), `before` (an `id`, for paging),
`tool`, `user` (only honoured for Admins), `outcome` (`ok`, `error`, `denied`).

Admins see every user's calls; everyone else sees only their own.

```json
{
  "items": [
    {
      "id": 42,
      "ts": "2026-11-10T12:00:00Z",
      "user": "Admin",
      "organization": "...",
      "client_name": "claude-code",
      "tool": "dry_run_placement",
      "kind": "read",
      "args": {"sla": {"...": "..."}},
      "outcome": "ok",
      "error": "",
      "duration_ms": 120,
      "action_id": ""
    }
  ],
  "next_before": 41
}
```

`args` is already redacted (secrets replaced with `"***"`, including `confirmation_token`).
`next_before` is null on the last page. `action_id` links a `prepare_*` call to its
`confirm_action` call; a confirm that fails before its token is verified has no `action_id`.

Errors on every route use `{"error": "<message>"}` with a 4xx/5xx status.
