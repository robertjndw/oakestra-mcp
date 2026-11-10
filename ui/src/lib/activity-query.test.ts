import { describe, expect, it } from 'vitest';

import { buildActivityPath } from './activity-query';

describe('buildActivityPath', () => {
  it('sends only the page size without filters', () => {
    expect(buildActivityPath({})).toBe('/activity?limit=50');
  });

  it('skips empty and blank filters', () => {
    expect(buildActivityPath({ tool: '', outcome: '', user: '  ' })).toBe('/activity?limit=50');
  });

  it('adds the filters and the paging cursor', () => {
    const path = buildActivityPath(
      { tool: 'dry_run_placement', outcome: 'error', user: 'Admin' },
      41,
      20,
    );
    const params = new URL(path, 'http://x').searchParams;
    expect(Object.fromEntries(params)).toEqual({
      limit: '20',
      before: '41',
      tool: 'dry_run_placement',
      outcome: 'error',
      user: 'Admin',
    });
  });

  it('keeps a cursor of zero', () => {
    expect(buildActivityPath({}, 0)).toContain('before=0');
  });
});
