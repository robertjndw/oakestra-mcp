import { hasAnyRole, useAddonApi, useCurrentUser } from '@oakestra/dashboard-sdk';
import {
  Button,
  EmptyState,
  Input,
  PageHeader,
  QueryErrorAlert,
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
  Skeleton,
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@oakestra/ui';
import { useInfiniteQuery, useQuery } from '@tanstack/react-query';
import { useEffect, useMemo, useState } from 'react';

import type { ActivityFilters } from '../lib/activity-query';
import type { ActivityItem, Outcome } from '../lib/types';
import { activityQuery, infoQuery } from '../queries';
import { ActivityDetail } from './ActivityDetail';
import { OutcomeBadge } from './OutcomeBadge';

const ALL = 'all';
// Typing in the user filter would otherwise start one request per keystroke.
const USER_FILTER_DEBOUNCE_MS = 300;
const OUTCOMES: Outcome[] = ['ok', 'error', 'denied'];

export default function ActivityPage() {
  const api = useAddonApi();
  const user = useCurrentUser();
  // Only Admins see other users' calls, so only they get the user filter.
  const isAdmin = hasAnyRole(user, ['Admin']);

  const [tool, setTool] = useState('');
  const [outcome, setOutcome] = useState<Outcome | ''>('');
  const [userFilter, setUserFilter] = useState('');
  const [debouncedUser, setDebouncedUser] = useState('');
  const [selectedId, setSelectedId] = useState<number | null>(null);

  useEffect(() => {
    const timer = setTimeout(() => setDebouncedUser(userFilter), USER_FILTER_DEBOUNCE_MS);
    return () => clearTimeout(timer);
  }, [userFilter]);

  const filters: ActivityFilters = { tool, outcome, user: isAdmin ? debouncedUser : '' };
  const activity = useInfiniteQuery(activityQuery(api, filters));
  // Shares its cache entry with the Connect page; only used to fill the tool filter.
  const info = useQuery(infoQuery(api));

  const items = useMemo(() => activity.data?.pages.flatMap((page) => page.items) ?? [], [activity.data]);
  const selected = items.find((item) => item.id === selectedId) ?? null;

  return (
    <div className="mcp:space-y-4">
      <PageHeader
        title="Agent activity"
        description={
          isAdmin
            ? 'Every tool call agents made through the MCP server. Refreshes every 10 seconds.'
            : 'The tool calls made with your account. Refreshes every 10 seconds.'
        }
      />

      <div className="mcp:flex mcp:flex-wrap mcp:items-center mcp:gap-2">
        <Select value={tool || ALL} onValueChange={(v) => setTool(v === ALL ? '' : v)}>
          <SelectTrigger aria-label="Tool" className="mcp:w-56">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value={ALL}>All tools</SelectItem>
            {info.data?.tools.map((t) => (
              <SelectItem key={t.name} value={t.name}>
                {t.name}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        <Select
          value={outcome || ALL}
          onValueChange={(v) => setOutcome(v === ALL ? '' : (v as Outcome))}
        >
          <SelectTrigger aria-label="Outcome" className="mcp:w-40">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value={ALL}>All outcomes</SelectItem>
            {OUTCOMES.map((o) => (
              <SelectItem key={o} value={o}>
                {o}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        {isAdmin ? (
          <Input
            aria-label="User"
            placeholder="Filter by user"
            className="mcp:w-48"
            value={userFilter}
            onChange={(e) => setUserFilter(e.target.value)}
          />
        ) : null}
      </div>

      {activity.isPending ? (
        <Skeleton className="mcp:h-64 mcp:w-full" />
      ) : activity.isError ? (
        <QueryErrorAlert
          title="Could not load the activity log"
          error={activity.error}
          onRetry={() => void activity.refetch()}
        />
      ) : items.length === 0 ? (
        <EmptyState title="No activity yet" description="Calls agents make will show up here." />
      ) : (
        <>
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Time</TableHead>
                <TableHead>Tool</TableHead>
                {isAdmin ? <TableHead>User</TableHead> : null}
                <TableHead>Client</TableHead>
                <TableHead>Outcome</TableHead>
                <TableHead className="mcp:text-right">Duration</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {items.map((item) => (
                <ActivityRow
                  key={item.id}
                  item={item}
                  showUser={isAdmin}
                  onOpen={() => setSelectedId(item.id)}
                />
              ))}
            </TableBody>
          </Table>
          {activity.hasNextPage ? (
            <div className="mcp:flex mcp:justify-center">
              <Button
                variant="outline"
                disabled={activity.isFetchingNextPage}
                onClick={() => void activity.fetchNextPage()}
              >
                {activity.isFetchingNextPage ? 'Loading...' : 'Load older calls'}
              </Button>
            </div>
          ) : null}
        </>
      )}

      <ActivityDetail
        item={selected}
        related={selected?.action_id ? items.filter((i) => i.action_id === selected.action_id) : []}
        onSelect={setSelectedId}
        onClose={() => setSelectedId(null)}
      />
    </div>
  );
}

function ActivityRow({
  item,
  showUser,
  onOpen,
}: {
  item: ActivityItem;
  showUser: boolean;
  onOpen: () => void;
}) {
  return (
    <TableRow
      tabIndex={0}
      className="mcp:cursor-pointer"
      onClick={onOpen}
      onKeyDown={(e) => {
        if (e.key === 'Enter' || e.key === ' ') {
          e.preventDefault();
          onOpen();
        }
      }}
    >
      <TableCell className="mcp:whitespace-nowrap">{new Date(item.ts).toLocaleString()}</TableCell>
      <TableCell className="mcp:font-mono mcp:text-xs">{item.tool}</TableCell>
      {showUser ? <TableCell>{item.user}</TableCell> : null}
      <TableCell>{item.client_name || '-'}</TableCell>
      <TableCell>
        <OutcomeBadge outcome={item.outcome} />
      </TableCell>
      <TableCell className="mcp:text-right mcp:tabular-nums">{item.duration_ms} ms</TableCell>
    </TableRow>
  );
}
