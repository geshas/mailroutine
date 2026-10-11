import { useQuery } from "@tanstack/react-query";
import { AlertTriangle } from "lucide-react";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Skeleton } from "@/components/ui/skeleton";
import { q } from "@/lib/queries";
import { duration, fullDate, num } from "@/lib/format";
import { StatusBadge } from "./common";

export function RunDetailsSheet({ runId, onClose }: { runId: string | null; onClose: () => void }) {
  const { data: run, isLoading } = useQuery({ ...q.run(runId ?? ""), enabled: !!runId });

  return (
    <Sheet open={!!runId} onOpenChange={(o) => !o && onClose()}>
      <SheetContent className="w-full sm:max-w-md">
        <SheetHeader>
          <SheetTitle>{run?.routineName ?? "Run details"}</SheetTitle>
          <SheetDescription>Execution record</SheetDescription>
        </SheetHeader>
        {isLoading || !run ? (
          <div className="mt-6 space-y-3">
            {Array.from({ length: 6 }).map((_, i) => <Skeleton key={i} className="h-5 w-full" />)}
          </div>
        ) : (
          <div className="mt-6 space-y-6">
            <dl className="divide-y rounded-lg border text-sm">
              {[
                ["Status", <StatusBadge key="s" status={run.status} />],
                ["Type", run.dryRun ? "Dry run (nothing changed)" : run.trigger === "manual" ? "Manual run" : "Scheduled run"],
                ["Started", fullDate(run.startedAt)],
                ["Finished", fullDate(run.finishedAt)],
                ["Duration", duration(run.startedAt, run.finishedAt)],
                ["Matched", num(run.matched)],
                ["Successfully handled", num(run.handled)],
                ["Failed", num(run.failed)],
              ].map(([k, v]) => (
                <div key={k as string} className="flex items-center justify-between px-4 py-2.5">
                  <dt className="text-muted-foreground">{k}</dt>
                  <dd className="font-medium tabular-nums">{v}</dd>
                </div>
              ))}
            </dl>
            {run.failures.length > 0 && (
              <div className="rounded-lg border p-4 text-sm">
                <p className="mb-2 font-medium">Messages that could not be modified</p>
                <ul className="max-h-48 space-y-1.5 overflow-y-auto">
                  {run.failures.slice(0, 50).map((f) => (
                    <li key={f.messageId} className="text-xs">
                      <code className="font-mono">{f.messageId}</code>
                      {f.error && <span className="text-muted-foreground"> — {f.error}</span>}
                    </li>
                  ))}
                </ul>
                {run.failures.length > 50 && <p className="mt-2 text-xs text-muted-foreground">Showing 50 of {num(run.failures.length)}.</p>}
              </div>
            )}
            {run.error && (
              <div className="rounded-lg border border-destructive/30 bg-danger-soft p-4 text-sm">
                <div className="mb-1.5 flex items-center gap-2 font-medium text-destructive">
                  <AlertTriangle className="size-4" /> Error
                </div>
                <p className="font-mono text-xs leading-relaxed text-foreground">{run.error}</p>
              </div>
            )}
          </div>
        )}
      </SheetContent>
    </Sheet>
  );
}
