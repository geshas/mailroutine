import { createFileRoute, Link } from "@tanstack/react-router";
import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { AlertTriangle, ArrowRight, CircleCheck, Mail, Plus, Repeat } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { Badge } from "@/components/ui/badge";
import { EmptyState, ErrorState, PageHeader } from "@/components/mr/common";
import { RunsTable } from "@/components/mr/RunsTable";
import { RunDetailsSheet } from "@/components/mr/RunDetailsSheet";
import { q } from "@/lib/queries";
import { num, relativeDay } from "@/lib/format";
import { usePageTitle } from "@/lib/title";

export const Route = createFileRoute("/_shell/dashboard")({
  component: DashboardPage,
});

function Metric({ label, value, caption }: { label: string; value: string; caption?: string | undefined }) {
  return (
    <div className="bg-card p-5">
      <p className="text-xs font-medium uppercase tracking-wide text-muted-foreground">{label}</p>
      <p className="mt-2 text-2xl font-semibold tracking-tight tabular-nums">{value}</p>
      {caption && <p className="mt-1 truncate text-sm text-muted-foreground">{caption}</p>}
    </div>
  );
}

/** Connected Gmail accounts, shown whether or not any routine exists yet. */
function ConnectedAccounts() {
  const { data: accounts, isLoading } = useQuery(q.accounts());
  if (isLoading) return <Skeleton className="h-16 w-full" />;
  if (!accounts?.length) {
    return (
      <div className="flex flex-col gap-3 rounded-lg border border-dashed p-4 text-sm sm:flex-row sm:items-center">
        <Mail className="size-5 text-muted-foreground" />
        <p className="flex-1 text-muted-foreground">No Gmail account is connected yet.</p>
        <Button size="sm" asChild><Link to="/settings">Connect Gmail</Link></Button>
      </div>
    );
  }
  return (
    <section aria-label="Connected Gmail accounts">
      <h2 className="mb-3 text-base font-semibold">Connected Gmail {accounts.length === 1 ? "account" : "accounts"}</h2>
      <ul className="divide-y rounded-lg border bg-card shadow-subtle">
        {accounts.map((a) => (
          <li key={a.id} className="flex items-center gap-3 p-4">
            <div className="flex size-9 items-center justify-center rounded-md bg-muted"><Mail className="size-4" /></div>
            <p className="min-w-0 flex-1 truncate text-sm font-medium">{a.email}</p>
            {a.status === "connected" ? (
              <Badge variant="success">Connected</Badge>
            ) : (
              <Button size="sm" variant="outline" asChild>
                <Link to="/settings"><AlertTriangle className="text-warning" /> Needs attention</Link>
              </Button>
            )}
          </li>
        ))}
      </ul>
    </section>
  );
}

function DashboardPage() {
  usePageTitle("Dashboard");
  const { data, isLoading, error, refetch } = useQuery(q.dashboard());
  const [runId, setRunId] = useState<string | null>(null);

  return (
    <>
      <PageHeader title="Dashboard" subtitle="Your Gmail automation at a glance." />

      {error ? (
        <ErrorState message={(error as Error).message} onRetry={() => refetch()} />
      ) : isLoading || !data ? (
        <div className="space-y-8" role="status" aria-busy="true" aria-label="Loading dashboard">
          <div className="grid gap-px overflow-hidden rounded-lg border bg-border sm:grid-cols-2 lg:grid-cols-4">
            {Array.from({ length: 4 }).map((_, i) => (
              <div key={i} className="space-y-3 bg-card p-5">
                <Skeleton className="h-3 w-20" /><Skeleton className="h-7 w-28" /><Skeleton className="h-4 w-36" />
              </div>
            ))}
          </div>
          <Skeleton className="h-56 w-full" />
        </div>
      ) : data.enabledRoutines === 0 && data.recentRuns.length === 0 ? (
        <div className="space-y-8">
          <ConnectedAccounts />
          <EmptyState
            icon={<Repeat className="size-8" />}
            title="No routines yet."
            description="Create your first routine and MailRoutine will automatically maintain your Gmail in the background."
            action={<Button asChild><Link to="/routines/new"><Plus /> Create your first routine</Link></Button>}
          />
        </div>
      ) : (
        <div className="space-y-10">
          {data.enabledRoutines > 0 ? (
            <div className="flex items-center gap-2 text-sm text-muted-foreground">
              <span className="relative flex size-2" aria-hidden="true">
                <span className="absolute inline-flex size-full animate-ping rounded-full bg-success opacity-60 motion-reduce:animate-none" />
                <span className="relative inline-flex size-2 rounded-full bg-success" />
              </span>
              Active — {data.enabledRoutines} {data.enabledRoutines === 1 ? "routine" : "routines"} maintaining {data.connectedAccounts} Gmail{" "}
              {data.connectedAccounts === 1 ? "account" : "accounts"} in the background.
            </div>
          ) : (
            <div className="flex items-center gap-2 text-sm text-muted-foreground">
              <span className="inline-flex size-2 rounded-full bg-muted-foreground/50" aria-hidden="true" />
              Paused — no routines are enabled.
            </div>
          )}

          <div className="grid gap-px overflow-hidden rounded-lg border bg-border shadow-subtle sm:grid-cols-2">
            <Metric label="Last run" value={relativeDay(data.lastRun?.startedAt)} caption={data.lastRun?.routineName} />
            <Metric label="Next run" value={relativeDay(data.nextRun?.at)} caption={data.nextRun?.routineName ?? "Nothing scheduled"} />
          </div>

          <div className="flex flex-wrap gap-x-8 gap-y-2 text-sm">
            <span className="flex items-center gap-2"><CircleCheck className="size-4 text-primary" /> Total runs <strong className="font-semibold tabular-nums">{num(data.totalRuns)}</strong></span>
            <span className="flex items-center gap-2"><CircleCheck className="size-4 text-primary" /> Emails handled <strong className="font-semibold tabular-nums">{num(data.emailsHandled)}</strong></span>
          </div>

          <ConnectedAccounts />

          <section>
            <div className="mb-4 flex flex-wrap items-center justify-between gap-2">
              <h2 className="text-base font-semibold">Recent runs</h2>
              <div className="flex items-center gap-2">
                <Button size="sm" asChild>
                  <Link to="/routines/new"><Plus /> New Routine</Link>
                </Button>
                <Button variant="ghost" size="sm" asChild>
                  <Link to="/runs">View all runs <ArrowRight /></Link>
                </Button>
              </div>
            </div>
            {data.recentRuns.length ? (
              <RunsTable runs={data.recentRuns} onSelect={setRunId} />
            ) : (
              <EmptyState title="No runs yet. Your routine execution history will appear here." />
            )}
          </section>
        </div>
      )}
      <RunDetailsSheet runId={runId} onClose={() => setRunId(null)} />
    </>
  );
}
