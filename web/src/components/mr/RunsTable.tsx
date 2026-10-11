import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import type { RunSummary } from "@/lib/api/types";
import { duration, num, relativeDay } from "@/lib/format";
import { StatusBadge } from "./common";

export function RunsTable({
  runs,
  onSelect,
  extended,
}: {
  runs: RunSummary[];
  onSelect: (id: string) => void;
  extended?: boolean;
}) {
  return (
    <div className="overflow-x-auto rounded-lg border">
      <Table>
        <TableHeader>
          <TableRow className="hover:bg-transparent">
            <TableHead>Routine</TableHead>
            <TableHead>{extended ? "Started" : "Time"}</TableHead>
            {extended && <TableHead>Finished</TableHead>}
            {extended && <TableHead>Duration</TableHead>}
            <TableHead className="text-right">Matched</TableHead>
            <TableHead className="text-right">Handled</TableHead>
            {extended && <TableHead className="text-right">Failed</TableHead>}
            <TableHead>Status</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {runs.map((r) => (
            <TableRow key={r.id} className="cursor-pointer" onClick={() => onSelect(r.id)}>
              <TableCell className="font-medium">
                <button
                  type="button"
                  onClick={(e) => { e.stopPropagation(); onSelect(r.id); }}
                  className="rounded-sm text-left font-medium hover:underline focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 focus-visible:ring-offset-background"
                  aria-label={`${r.routineName}, view run details`}
                >
                  {r.routineName}
                </button>
                {r.dryRun && <span className="ml-2 rounded bg-muted px-1.5 py-0.5 text-xs font-medium text-muted-foreground">Dry run</span>}
              </TableCell>
              <TableCell className="whitespace-nowrap text-muted-foreground">{relativeDay(r.startedAt)}</TableCell>
              {extended && <TableCell className="whitespace-nowrap text-muted-foreground">{r.finishedAt ? relativeDay(r.finishedAt) : "—"}</TableCell>}
              {extended && <TableCell className="text-muted-foreground">{duration(r.startedAt, r.finishedAt)}</TableCell>}
              <TableCell className="text-right tabular-nums">{num(r.matched)}</TableCell>
              <TableCell className="text-right tabular-nums">{num(r.handled)}</TableCell>
              {extended && (
                <TableCell className={r.failed ? "text-right tabular-nums text-destructive" : "text-right tabular-nums text-muted-foreground"}>
                  {num(r.failed)}
                </TableCell>
              )}
              <TableCell><StatusBadge status={r.status} /></TableCell>
            </TableRow>
          ))}
        </TableBody>
      </Table>
    </div>
  );
}
