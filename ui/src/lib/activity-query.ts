import type { Outcome } from './types';

export interface ActivityFilters {
  tool?: string;
  outcome?: Outcome | '';
  user?: string;
}

export const ACTIVITY_PAGE_SIZE = 50;

/**
 * Path (below the addon API base) for one page of the activity log. Empty filters are left out
 * so the backend applies its defaults instead of matching on an empty string.
 */
export function buildActivityPath(
  filters: ActivityFilters,
  before?: number | null,
  limit = ACTIVITY_PAGE_SIZE,
): string {
  const params = new URLSearchParams({ limit: String(limit) });
  if (before != null) params.set('before', String(before));
  const tool = filters.tool?.trim();
  if (tool) params.set('tool', tool);
  if (filters.outcome) params.set('outcome', filters.outcome);
  // The backend ignores `user` for non-admins, but sending it would suggest the filter works.
  const user = filters.user?.trim();
  if (user) params.set('user', user);
  return `/activity?${params.toString()}`;
}
