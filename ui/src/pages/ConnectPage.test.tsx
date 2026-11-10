import { renderInHost } from '@oakestra/dashboard-sdk/testing';
import { fireEvent, screen, waitFor } from '@testing-library/react';
import { ADDON_ID } from 'virtual:oakestra-addon';
import { describe, expect, it } from 'vitest';

import ConnectPage from './ConnectPage';

const info = {
  name: 'oakestra-mcp',
  version: '0.1.0',
  mcp_path: '/mcp',
  public_url: '',
  writes_enabled: false,
  user: { username: 'alice', roles: ['Admin'], organization: '' },
  tools: [
    {
      name: 'confirm_action',
      title: 'Confirm action',
      summary: 'Run a prepared plan.',
      description: 'A long description the agent reads.',
      kind: 'write',
    },
  ],
};

function render(roles: string[]) {
  const puts: unknown[] = [];
  renderInHost(<ConnectPage />, {
    addon: { addonId: ADDON_ID },
    user: { username: 'alice', roles },
    addonFetch: async (request) => {
      if (request.method === 'PUT') {
        const body = (await request.json()) as { writes_enabled: boolean };
        puts.push(body);
        return Response.json(body);
      }
      return Response.json(info);
    },
  });
  return puts;
}

describe('ConnectPage', () => {
  it('shows the summary and keeps the full description collapsed', async () => {
    render(['Admin']);
    expect(await screen.findByText(/Run a prepared plan\./)).toBeTruthy();
    const details = screen.getByText('A long description the agent reads.').closest('details');
    expect(details?.open).toBe(false);
  });

  it('lets admins enable write mode after confirming', async () => {
    const puts = render(['Admin']);
    fireEvent.click(await screen.findByRole('switch'));
    fireEvent.click(await screen.findByRole('button', { name: 'Enable' }));
    await waitFor(() => expect(puts).toEqual([{ writes_enabled: true }]));
    await waitFor(() => expect(screen.getByRole('switch').getAttribute('aria-checked')).toBe('true'));
  });

  it('shows the switch read-only to other roles', async () => {
    render(['Application_Provider']);
    expect((await screen.findByRole('switch')).hasAttribute('disabled')).toBe(true);
    expect(screen.getByText('Only Admins can change this.')).toBeTruthy();
  });
});
