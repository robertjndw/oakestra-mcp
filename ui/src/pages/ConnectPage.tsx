import { hasAnyRole, useAddonApi, useCurrentUser, useNotify } from '@oakestra/dashboard-sdk';
import {
  Badge,
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
  ConfirmDialog,
  Label,
  PageHeader,
  QueryErrorAlert,
  Skeleton,
  Switch,
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
  Tabs,
  TabsContent,
  TabsList,
  TabsTrigger,
} from '@oakestra/ui';
import { useQuery } from '@tanstack/react-query';
import { useState } from 'react';

import {
  claudeCodeCommand,
  cursorConfig,
  genericCurl,
  loginCommand,
  proxiedMcpUrl,
  TOKEN_PLACEHOLDER,
  vscodeConfig,
} from '../lib/snippets';
import type { ServerInfo, ToolKind } from '../lib/types';
import { infoQuery, useUpdateSettings } from '../queries';
import { CodeBlock } from './CodeBlock';

export default function ConnectPage() {
  const api = useAddonApi();
  const info = useQuery(infoQuery(api));

  return (
    <div className="mcp:space-y-6">
      <PageHeader
        title="MCP"
        description="Let AI agents inspect this Oakestra deployment and explain service placement through the Model Context Protocol."
      />
      {info.isPending ? (
        <Skeleton className="mcp:h-48 mcp:w-full" />
      ) : info.isError ? (
        <QueryErrorAlert
          title="Could not reach the MCP server"
          error={info.error}
          onRetry={() => void info.refetch()}
        />
      ) : (
        <ConnectContent info={info.data} apiBase={api.baseUrl} />
      )}
    </div>
  );
}

function ConnectContent({ info, apiBase }: { info: ServerInfo; apiBase: string }) {
  const dashboardUrl = proxiedMcpUrl(window.location.origin, apiBase, info.mcp_path);
  const urls = [
    ...(info.public_url ? [{ label: 'Public URL', url: info.public_url }] : []),
    { label: 'Through this dashboard', url: dashboardUrl },
  ];
  const url = info.public_url || dashboardUrl;

  return (
    <>
      <WriteModeCard writesEnabled={info.writes_enabled} />

      <Card>
        <CardHeader>
          <CardTitle>Endpoint</CardTitle>
          <CardDescription>
            Streamable HTTP, version {info.version}. Every request needs an Oakestra token.
          </CardDescription>
        </CardHeader>
        <CardContent className="mcp:space-y-3">
          {urls.map((entry) => (
            <div key={entry.label} className="mcp:space-y-1">
              <p className="mcp:text-sm mcp:font-medium">{entry.label}</p>
              <CodeBlock value={entry.url} label={entry.label.toLowerCase()} />
            </div>
          ))}
          {!info.public_url ? (
            <p className="mcp:text-sm mcp:text-muted-foreground">
              No PUBLIC_URL is configured on the server. The dashboard URL works for any client
              that can reach this dashboard.
            </p>
          ) : null}
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle>Get a token</CardTitle>
          <CardDescription>
            Agents authenticate with their own Oakestra login. Access tokens expire after 10
            minutes, so use the <code>refresh_token</code> from the login response: the server
            accepts it and renews the access token itself.
          </CardDescription>
        </CardHeader>
        <CardContent>
          <CodeBlock value={loginCommand()} label="login command" />
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle>Client configuration</CardTitle>
          <CardDescription>
            Replace <code>{TOKEN_PLACEHOLDER}</code> with the token from the previous step.
          </CardDescription>
        </CardHeader>
        <CardContent>
          <Tabs defaultValue="claude">
            <TabsList>
              <TabsTrigger value="claude">Claude Code</TabsTrigger>
              <TabsTrigger value="cursor">Cursor</TabsTrigger>
              <TabsTrigger value="vscode">VS Code</TabsTrigger>
              <TabsTrigger value="generic">Generic</TabsTrigger>
            </TabsList>
            <TabsContent value="claude" className="mcp:space-y-2">
              <CodeBlock value={claudeCodeCommand(url)} label="Claude Code command" />
            </TabsContent>
            <TabsContent value="cursor" className="mcp:space-y-2">
              <p className="mcp:text-sm mcp:text-muted-foreground">
                Put this in <code>.cursor/mcp.json</code> or <code>~/.cursor/mcp.json</code>.
              </p>
              <CodeBlock value={cursorConfig(url)} label="Cursor config" />
            </TabsContent>
            <TabsContent value="vscode" className="mcp:space-y-2">
              <p className="mcp:text-sm mcp:text-muted-foreground">
                Put this in <code>.vscode/mcp.json</code>.
              </p>
              <CodeBlock value={vscodeConfig(url)} label="VS Code config" />
            </TabsContent>
            <TabsContent value="generic" className="mcp:space-y-2">
              <p className="mcp:text-sm mcp:text-muted-foreground">
                Any client that speaks Streamable HTTP needs the URL and an{' '}
                <code>Authorization: Bearer</code> header. This call lists the tools:
              </p>
              <CodeBlock value={genericCurl(url)} label="curl command" />
            </TabsContent>
          </Tabs>
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle>Tools</CardTitle>
          <CardDescription>
            {info.tools.length} tools. Write tools change the deployment and are only served while
            write mode is on.
          </CardDescription>
        </CardHeader>
        <CardContent>
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Name</TableHead>
                <TableHead>Title</TableHead>
                <TableHead>Description</TableHead>
                <TableHead>Access</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {info.tools.map((tool) => (
                <TableRow key={tool.name}>
                  <TableCell className="mcp:font-mono mcp:text-xs">{tool.name}</TableCell>
                  <TableCell>{tool.title}</TableCell>
                  <TableCell className="mcp:max-w-xl mcp:whitespace-normal mcp:text-muted-foreground">
                    <ToolDescription summary={tool.summary} description={tool.description} />
                  </TableCell>
                  <TableCell>
                    <KindBadge kind={tool.kind} writesEnabled={info.writes_enabled} />
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </CardContent>
      </Card>
    </>
  );
}

function WriteModeCard({ writesEnabled }: { writesEnabled: boolean }) {
  const api = useAddonApi();
  const notify = useNotify();
  // The server enforces this too (PUT /settings answers 403); the check only avoids offering a
  // switch that cannot work.
  const isAdmin = hasAnyRole(useCurrentUser(), ['Admin']);
  const update = useUpdateSettings(api);
  const [confirmOpen, setConfirmOpen] = useState(false);

  const save = (next: boolean) =>
    update.mutate(
      { writes_enabled: next },
      {
        onSuccess: () => {
          setConfirmOpen(false);
          notify.success(next ? 'Write mode enabled' : 'Write mode disabled');
        },
        onError: (error) => notify.error('Could not change write mode', { description: error.message }),
      },
    );

  return (
    <Card>
      <CardHeader>
        <CardTitle>Write mode</CardTitle>
        <CardDescription>
          {writesEnabled
            ? 'Agents can deploy, scale, undeploy and delete through the prepare and confirm tools. Every change still needs the human to approve the plan, and only users with the Admin, Organization_Admin or Application_Provider role can make one.'
            : 'Agents can inspect the deployment but cannot change it.'}
        </CardDescription>
      </CardHeader>
      <CardContent>
        <div className="mcp:flex mcp:items-center mcp:gap-3">
          <Switch
            id="mcp-write-mode"
            checked={writesEnabled}
            disabled={!isAdmin || update.isPending}
            // Turning writes on widens what every agent can do, so it asks first; turning them off
            // only narrows it and applies at once.
            onCheckedChange={(next) => (next ? setConfirmOpen(true) : save(false))}
          />
          <Label htmlFor="mcp-write-mode">{writesEnabled ? 'Enabled' : 'Disabled'}</Label>
        </div>
        {!isAdmin ? (
          <p className="mcp:mt-2 mcp:text-sm mcp:text-muted-foreground">
            Only Admins can change this.
          </p>
        ) : null}
      </CardContent>
      <ConfirmDialog
        open={confirmOpen}
        onOpenChange={setConfirmOpen}
        title="Enable write mode?"
        description="Agents of users with a write role can then plan and, after the user approves, apply deployments, scaling, undeploys and deletions. The change applies to the next agent request."
        confirmLabel="Enable"
        pending={update.isPending}
        onConfirm={() => save(true)}
      />
    </Card>
  );
}

// The full description is written for the agent and runs to several sentences, so the table shows
// the summary and keeps the rest one click away.
function ToolDescription({ summary, description }: { summary: string; description: string }) {
  return (
    <details className="mcp:group">
      <summary className="mcp:cursor-pointer mcp:list-none mcp:[&::-webkit-details-marker]:hidden">
        {summary || description}{' '}
        <span className="mcp:text-xs mcp:underline mcp:underline-offset-2 mcp:group-open:hidden">
          Details
        </span>
        <span className="mcp:hidden mcp:text-xs mcp:underline mcp:underline-offset-2 mcp:group-open:inline">
          Hide
        </span>
      </summary>
      <p className="mcp:mt-2 mcp:text-xs">{description}</p>
    </details>
  );
}

function KindBadge({ kind, writesEnabled }: { kind: ToolKind; writesEnabled: boolean }) {
  if (kind === 'read') return <Badge variant="neutral">read</Badge>;
  return <Badge variant="warning">{writesEnabled ? 'write' : 'write (disabled)'}</Badge>;
}
