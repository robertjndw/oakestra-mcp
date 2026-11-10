import {
  Button,
  JsonViewer,
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from '@oakestra/ui';

import type { ActivityItem } from '../lib/types';
import { OutcomeBadge } from './OutcomeBadge';

interface ActivityDetailProps {
  item: ActivityItem | null;
  /** Rows (including `item`) that share its action_id. */
  related: ActivityItem[];
  onSelect: (id: number) => void;
  onClose: () => void;
}

export function ActivityDetail({ item, related, onSelect, onClose }: ActivityDetailProps) {
  const others = related.filter((r) => r.id !== item?.id);

  return (
    <Sheet open={item !== null} onOpenChange={(open) => !open && onClose()}>
      <SheetContent className="mcp:w-full mcp:overflow-y-auto sm:mcp:max-w-xl">
        {item ? (
          <>
            <SheetHeader>
              <SheetTitle className="mcp:font-mono">{item.tool}</SheetTitle>
              <SheetDescription>{new Date(item.ts).toLocaleString()}</SheetDescription>
            </SheetHeader>
            <div className="mcp:space-y-4 mcp:px-4 mcp:pb-4">
              <dl className="mcp:grid mcp:grid-cols-[8rem_1fr] mcp:gap-y-1 mcp:text-sm">
                <dt className="mcp:text-muted-foreground">Outcome</dt>
                <dd>
                  <OutcomeBadge outcome={item.outcome} />
                </dd>
                <dt className="mcp:text-muted-foreground">User</dt>
                <dd>
                  {item.user}
                  {item.organization ? ` (${item.organization})` : ''}
                </dd>
                <dt className="mcp:text-muted-foreground">Client</dt>
                <dd>{item.client_name || '-'}</dd>
                <dt className="mcp:text-muted-foreground">Kind</dt>
                <dd>{item.kind}</dd>
                <dt className="mcp:text-muted-foreground">Duration</dt>
                <dd>{item.duration_ms} ms</dd>
                {item.action_id ? (
                  <>
                    <dt className="mcp:text-muted-foreground">Action ID</dt>
                    <dd className="mcp:font-mono mcp:text-xs mcp:break-all">{item.action_id}</dd>
                  </>
                ) : null}
              </dl>

              {item.error ? (
                <p
                  role="alert"
                  className="mcp:rounded-md mcp:bg-destructive/10 mcp:p-3 mcp:text-sm mcp:text-destructive"
                >
                  {item.error}
                </p>
              ) : null}

              <JsonViewer title="Arguments (redacted)" data={item.args ?? {}} />

              {others.length > 0 ? (
                <div className="mcp:space-y-2">
                  <h3 className="mcp:text-sm mcp:font-medium">Same action</h3>
                  <ul className="mcp:space-y-1">
                    {others.map((r) => (
                      <li key={r.id}>
                        <Button variant="link" className="mcp:h-auto mcp:p-0" onClick={() => onSelect(r.id)}>
                          {r.tool} at {new Date(r.ts).toLocaleTimeString()} ({r.outcome})
                        </Button>
                      </li>
                    ))}
                  </ul>
                </div>
              ) : null}
            </div>
          </>
        ) : null}
      </SheetContent>
    </Sheet>
  );
}
