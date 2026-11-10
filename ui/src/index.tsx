import './addon.css';

import { defineAddon } from '@oakestra/dashboard-sdk';
import { BotIcon } from 'lucide-react';
import { lazy } from 'react';
import { ADDON_ID } from 'virtual:oakestra-addon';

// The shell loads this module on every page for the nav item, so the pages stay behind imports.
const ConnectPage = lazy(() => import('./pages/ConnectPage'));
const ActivityPage = lazy(() => import('./pages/ActivityPage'));

export default defineAddon({
  id: ADDON_ID,
  // First breadcrumb of the addon's pages.
  label: 'MCP',
  routes: [
    { index: true, Component: ConnectPage },
    { path: 'activity', crumb: 'Activity', Component: ActivityPage },
  ],
  contributions: {
    'nav.items': [
      {
        id: 'main',
        label: 'MCP',
        path: '',
        icon: BotIcon,
        children: [
          { id: 'connect', label: 'Connect', path: '' },
          { id: 'activity', label: 'Activity', path: 'activity' },
        ],
      },
    ],
  },
});
