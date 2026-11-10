import { useAddonApi } from '@oakestra/dashboard-sdk';
import {
  Alert,
  AlertDescription,
  AlertTitle,
  Badge,
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
  PageHeader,
  QueryErrorAlert,
  Skeleton,
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

import {
  claudeCodeCommand,
  cursorConfig,
  genericCurl,
  loginCommand,
  proxiedMcpUrl,
  vscodeConfig,
} from '../lib/snippets';
import type { ServerInfo } from '../lib/types';
import { infoQuery } from '../queries';
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
        <ConnectContent info={info.data} />
      )}
    </div>
  );
}

function ConnectContent({ info }: { info: ServerInfo }) {
  const dashboardUrl = proxiedMcpUrl(window.location.origin, info.mcp_path);
  const urls = [
    ...(info.public_url ? [{ label: 'Public URL', url: info.public_url }] : []),
    { label: 'Through this dashboard', url: dashboardUrl },
  ];
  const [primary] = urls;
  const url = primary?.url ?? dashboardUrl;

  return (
    <>
      {!info.writes_enabled ? (
        <Alert>
          <AlertTitle>Read-only</AlertTitle>
          <AlertDescription>
            Write tools are disabled on this server (MCP_ENABLE_WRITES is not true). Agents can
            inspect the deployment but cannot change it.
          </AlertDescription>
        </Alert>
      ) : null}

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
            Replace <code>{'<refresh_token>'}</code> with the token from the previous step.
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
            {info.tools.length} tools. Write tools change the deployment and only work when writes
            are enabled.
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
                    {tool.description}
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

function KindBadge({ kind, writesEnabled }: { kind: 'read' | 'write'; writesEnabled: boolean }) {
  if (kind === 'read') return <Badge variant="neutral">read</Badge>;
  return <Badge variant="warning">{writesEnabled ? 'write' : 'write (disabled)'}</Badge>;
}
