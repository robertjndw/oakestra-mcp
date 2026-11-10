import { CopyButton } from '@oakestra/ui';

export function CodeBlock({ value, label }: { value: string; label: string }) {
  return (
    <div className="mcp:relative mcp:rounded-md mcp:border mcp:bg-muted/40">
      <pre className="mcp:overflow-x-auto mcp:p-3 mcp:pr-12 mcp:text-xs mcp:leading-relaxed">
        <code>{value}</code>
      </pre>
      <CopyButton
        value={value}
        label={`Copy ${label}`}
        className="mcp:absolute mcp:top-1.5 mcp:right-1.5"
      />
    </div>
  );
}
