import { addonQueryKey, type AddonApi } from '@oakestra/dashboard-sdk';
import { infiniteQueryOptions, queryOptions } from '@tanstack/react-query';
import { ADDON_ID } from 'virtual:oakestra-addon';

import { type ActivityFilters, buildActivityPath } from './lib/activity-query';
import type { ActivityPage, ServerInfo } from './lib/types';

async function getJson<T>(api: AddonApi, path: string, signal: AbortSignal): Promise<T> {
  const response = await api.fetch(path, { signal });
  if (!response.ok) {
    let message = `The MCP server answered ${response.status}`;
    try {
      const body = (await response.json()) as { error?: string };
      if (body.error) message = body.error;
    } catch {
      // Not JSON (for example an nginx 502 page); the status line is all there is to show.
    }
    throw new Error(message);
  }
  return (await response.json()) as T;
}

export const infoQuery = (api: AddonApi) =>
  queryOptions({
    queryKey: addonQueryKey(ADDON_ID, 'info'),
    queryFn: ({ signal }) => getJson<ServerInfo>(api, '/info', signal),
    // Catalog, version and settings only change when the server restarts.
    staleTime: Infinity,
  });

export const ACTIVITY_REFETCH_MS = 10_000;

export const activityQuery = (api: AddonApi, filters: ActivityFilters) =>
  infiniteQueryOptions({
    queryKey: addonQueryKey(ADDON_ID, 'activity', filters),
    queryFn: ({ signal, pageParam }) =>
      getJson<ActivityPage>(api, buildActivityPath(filters, pageParam), signal),
    initialPageParam: null as number | null,
    getNextPageParam: (last) => last.next_before,
    refetchInterval: ACTIVITY_REFETCH_MS,
  });
