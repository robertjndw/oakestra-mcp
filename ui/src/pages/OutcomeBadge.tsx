import { Badge } from '@oakestra/ui';

import type { Outcome } from '../lib/types';

export function OutcomeBadge({ outcome }: { outcome: Outcome }) {
  const variant = outcome === 'ok' ? 'success' : outcome === 'error' ? 'destructive' : 'warning';
  return <Badge variant={variant}>{outcome}</Badge>;
}
