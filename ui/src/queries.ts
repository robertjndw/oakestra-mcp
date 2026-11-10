import { addonQueryKey, type AddonApi } from '@oakestra/dashboard-sdk';
import {
  infiniteQueryOptions,
  queryOptions,
  useMutation,
  useQueryClient,
} from '@tanstack/react-query';
import { ADDON_ID } from 'virtual:oakestra-addon';

import { type ActivityFilters, buildActivityPath } from './lib/activity-query';
import type { ActivityPage, ServerInfo, Settings } from './lib/types';

async function getJson<T>(api: AddonApi, path: string, signal?: AbortSignal, init?: RequestInit): Promise<T> {
  const response = await api.fetch(path, { ...init, signal });
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
    // Catalog and version only change when the server restarts. writes_enabled can change at
    // runtime, but only through useUpdateSettings, which patches this entry.
    staleTime: Infinity,
  });

export function useUpdateSettings(api: AddonApi) {
  const client = useQueryClient();
  const key = infoQuery(api).queryKey;
  return useMutation({
    mutationFn: (next: Settings) =>
      getJson<Settings>(api, '/settings', undefined, {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(next),
      }),
    onSuccess: (saved) => {
      client.setQueryData(key, (old) => (old ? { ...old, ...saved } : old));
    },
  });
}

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
