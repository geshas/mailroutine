import { createFileRoute } from "@tanstack/react-router";
import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { History } from "lucide-react";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Skeleton } from "@/components/ui/skeleton";
import { Button } from "@/components/ui/button";
import { EmptyState, ErrorState, PageHeader } from "@/components/mr/common";
import { RunsTable } from "@/components/mr/RunsTable";
import { RunDetailsSheet } from "@/components/mr/RunDetailsSheet";
import { q } from "@/lib/queries";
import { usePageTitle } from "@/lib/title";
import type { RunStatus } from "@/lib/api/types";

type Search = { routine?: string | undefined };

export const Route = createFileRoute("/_shell/runs")({
  validateSearch: (s: Record<string, unknown>): Search => ({
    routine: typeof s["routine"] === "string" ? s["routine"] : undefined,
  }),
  component: RunsPage,
});

const PAGE_SIZE = 25;
const RANGES = { all: "All time", "1": "Last 24 hours", "7": "Last 7 days", "30": "Last 30 days" } as const;

function RunsPage() {
  usePageTitle("Run history");
  const search = Route.useSearch();
  const [routineId, setRoutineId] = useState(search.routine ?? "all");
  const [status, setStatus] = useState<"all" | RunStatus>("all");
  const [range, setRange] = useState<keyof typeof RANGES>("all");
  // Captured when the range is chosen: computing "now" during render would change the query key every render.
  const [since, setSince] = useState<string | undefined>(undefined);
  const [runId, setRunId] = useState<string | null>(null);

  const routines = useQuery(q.routines());
  const filters = {
    routineId: routineId === "all" ? undefined : routineId,
    status: status === "all" ? undefined : status,
    from: since,
  };
  const [page, setPage] = useState(0);
  // Fetch one extra row to know whether a next page exists.
  const { data, isLoading, error, refetch } = useQuery(q.runs({ ...filters, limit: PAGE_SIZE + 1, offset: page * PAGE_SIZE }));
  const filtered = routineId !== "all" || status !== "all" || range !== "all";

  return (
    <>
      <PageHeader title="Run history" subtitle="Every execution of your routines." />
      <div className="mb-5 grid gap-2 sm:flex">
        <Select value={routineId} onValueChange={(v) => { setRoutineId(v); setPage(0); }}>
          <SelectTrigger className="sm:w-60" aria-label="Routine"><SelectValue placeholder="Routine" /></SelectTrigger>
          <SelectContent>
            <SelectItem value="all">All routines</SelectItem>
            {routines.data?.map((r) => <SelectItem key={r.id} value={r.id}>{r.name}</SelectItem>)}
          </SelectContent>
        </Select>
        <Select value={status} onValueChange={(v) => { setStatus(v as typeof status); setPage(0); }}>
          <SelectTrigger className="sm:w-40" aria-label="Status"><SelectValue /></SelectTrigger>
          <SelectContent>
            <SelectItem value="all">Any status</SelectItem>
            <SelectItem value="success">Success</SelectItem>
            <SelectItem value="warning">Partial success</SelectItem>
            <SelectItem value="failed">Failed</SelectItem>
            <SelectItem value="running">Running</SelectItem>
            <SelectItem value="cancelled">Cancelled</SelectItem>
          </SelectContent>
        </Select>
        <Select value={range} onValueChange={(v) => {
          setRange(v as typeof range);
          setSince(v === "all" ? undefined : new Date(Date.now() - Number(v) * 86_400_000).toISOString());
          setPage(0);
        }}>
          <SelectTrigger className="sm:w-44" aria-label="Date range"><SelectValue /></SelectTrigger>
          <SelectContent>
            {Object.entries(RANGES).map(([k, l]) => <SelectItem key={k} value={k}>{l}</SelectItem>)}
          </SelectContent>
        </Select>
      </div>
      {error ? (
        <ErrorState message={(error as Error).message} onRetry={() => refetch()} />
      ) : isLoading || !data ? (
        <div className="space-y-2">{Array.from({ length: 6 }).map((_, i) => <Skeleton key={i} className="h-11 w-full" />)}</div>
      ) : data.length === 0 ? (
        <EmptyState
          icon={<History className="size-8" />}
          title={filtered ? "No runs match these filters" : "No runs yet. Your routine execution history will appear here."}
          {...(filtered ? { description: "Try a different routine, status or date range." } : {})}
        />
      ) : (
        <>
          <RunsTable runs={data.slice(0, PAGE_SIZE)} onSelect={setRunId} extended />
          <div className="mt-4 flex items-center justify-between text-sm text-muted-foreground">
            <span>Page {page + 1}</span>
            <div className="flex gap-2">
              <Button variant="outline" size="sm" disabled={page === 0} onClick={() => setPage((p) => p - 1)}>Previous</Button>
              <Button variant="outline" size="sm" disabled={data.length <= PAGE_SIZE} onClick={() => setPage((p) => p + 1)}>Next</Button>
            </div>
          </div>
        </>
      )}
      <RunDetailsSheet runId={runId} onClose={() => setRunId(null)} />
    </>
  );
}
