import { renderInHost } from '@oakestra/dashboard-sdk/testing';
import { screen } from '@testing-library/react';
import { ADDON_ID } from 'virtual:oakestra-addon';
import { describe, expect, it } from 'vitest';

import ActivityPage from './ActivityPage';

const info = { tools: [{ name: 'list_clusters', title: 'List clusters', description: '', kind: 'read' }] };
const row = {
  id: 1,
  ts: '2026-11-10T12:00:00Z',
  user: 'alice',
  organization: '',
  client_name: 'claude-code',
  tool: 'list_clusters',
  kind: 'read',
  args: {},
  outcome: 'ok',
  error: '',
  duration_ms: 12,
  action_id: '',
};

function render(roles: string[]) {
  const requested: string[] = [];
  renderInHost(<ActivityPage />, {
    addon: { addonId: ADDON_ID },
    user: { username: 'alice', roles },
    addonFetch: (request) => {
      requested.push(new URL(request.url).pathname);
      const body = request.url.includes('/info') ? info : { items: [row], next_before: null };
      return Promise.resolve(Response.json(body));
    },
  });
  return requested;
}

describe('ActivityPage', () => {
  it('lists calls and offers the user filter to admins', async () => {
    const requested = render(['Admin']);
    expect(await screen.findByText('claude-code')).toBeTruthy();
    expect(screen.getByLabelText('User')).toBeTruthy();
    expect(requested.some((p) => p.endsWith('/activity'))).toBe(true);
  });

  it('hides the user filter from other roles', async () => {
    render(['Application_Provider']);
    expect(await screen.findByText('claude-code')).toBeTruthy();
    expect(screen.queryByLabelText('User')).toBeNull();
  });
});
