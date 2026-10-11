import { createFileRoute, Link, useNavigate } from "@tanstack/react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useRef, useState } from "react";
import { toast } from "sonner";
import { Copy, Eye, FlaskConical, MoreHorizontal, Pencil, Play, Plus, Repeat, Trash2, Loader2 } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Switch } from "@/components/ui/switch";
import { Skeleton } from "@/components/ui/skeleton";
import {
  DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuSeparator, DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import {
  AlertDialog, AlertDialogAction, AlertDialogCancel, AlertDialogContent, AlertDialogDescription,
  AlertDialogFooter, AlertDialogHeader, AlertDialogTitle,
} from "@/components/ui/alert-dialog";
import { Code, EmptyState, ErrorState, PageHeader } from "@/components/mr/common";
import { PreviewDialog } from "@/components/mr/PreviewDialog";
import { api } from "@/lib/api/client";
import { q } from "@/lib/queries";
import type { PreviewResult, Routine, RunSummary } from "@/lib/api/types";
import { usePageTitle } from "@/lib/title";
import { describeAction, describeSchedule, num, relativeDay } from "@/lib/format";
import { cn } from "@/lib/utils";

export const Route = createFileRoute("/_shell/routines/")({
  component: RoutinesPage,
});

function RoutinesPage() {
  usePageTitle("Routines");
  const qc = useQueryClient();
  const navigate = useNavigate();
  const { data: routines, isLoading, error, refetch } = useQuery(q.routines());
  const { data: accounts } = useQuery(q.accounts());
  const [toDelete, setToDelete] = useState<Routine | null>(null);
  const [preview, setPreview] = useState<{ routine: Routine; result?: PreviewResult } | null>(null);
  const [running, setRunning] = useState<string | null>(null);
  // Moving mail to Trash always needs an explicit "yes" before it is enabled or run for real.
  const [confirm, setConfirm] = useState<{ kind: "enable" | "run"; routine: Routine } | null>(null);
  const abort = useRef(new AbortController());
  useEffect(() => {
    const c = abort.current;
    return () => c.abort();
  }, []);

  const invalidate = () => {
    qc.invalidateQueries({ queryKey: ["routines"] });
    qc.invalidateQueries({ queryKey: ["dashboard"] });
    qc.invalidateQueries({ queryKey: ["runs"] });
  };

  const toggle = useMutation({
    mutationFn: ({ id, enabled, confirmed }: { id: string; enabled: boolean; confirmed?: boolean }) =>
      api.setRoutineEnabled(id, enabled, { confirmDestructive: confirmed ?? false }),
    onMutate: async ({ id, enabled }) => {
      qc.setQueryData<Routine[]>(["routines"], (old) => old?.map((r) => (r.id === id ? { ...r, enabled } : r)));
    },
    onSuccess: (r) => toast.success(`${r.name} ${r.enabled ? "enabled" : "disabled"}`),
    onError: (e) => toast.error("Couldn't update routine", { description: (e as Error).message }),
    onSettled: invalidate,
  });

  function requestToggle(r: Routine, enabled: boolean) {
    if (enabled && r.action.type === "trash") setConfirm({ kind: "enable", routine: r });
    else toggle.mutate({ id: r.id, enabled });
  }

  function requestRun(r: Routine, dryRun: boolean) {
    if (!dryRun && r.action.type === "trash") setConfirm({ kind: "run", routine: r });
    else void runNow(r, dryRun, false);
  }

  function describeOutcome(r: Routine, run: RunSummary) {
    if (run.status === "failed")
      return toast.error(`${r.name} failed`, { description: "See Run history for the error." });
    if (run.status === "cancelled") return toast.warning(`${r.name} was stopped`);
    const verb = run.dryRun ? "would be handled (dry run, nothing changed)" : "handled";
    const description = `${num(run.handled || (run.dryRun ? run.matched : 0))} of ${num(run.matched)} emails ${verb}.`;
    if (run.status === "warning") return toast.warning(`${r.name} finished with errors`, { description: `${description} ${num(run.failed)} failed.` });
    return toast.success(`${r.name} finished`, { description });
  }

  async function runNow(r: Routine, dryRun: boolean, confirmed: boolean) {
    setRunning(r.id);
    try {
      const started = await api.runRoutine(r.id, { dryRun, confirmDestructive: confirmed });
      toast.info(dryRun ? `Dry run of ${r.name} started` : `${r.name} started`);
      invalidate();
      describeOutcome(r, await api.waitForRun(started.id, abort.current.signal));
    } catch (e) {
      if ((e as Error).name !== "AbortError") toast.error("Run failed", { description: (e as Error).message });
    } finally {
      setRunning(null);
      invalidate();
    }
  }

  async function openPreview(r: Routine) {
    setPreview({ routine: r });
    try {
      const result = await api.previewRoutine(r.id);
      setPreview({ routine: r, result });
    } catch (e) {
      setPreview(null);
      toast.error("Preview failed", { description: (e as Error).message });
    }
  }

  async function duplicate(r: Routine) {
    const { id: _id, createdAt: _c, updatedAt: _u, lastRunAt: _l, nextRunAt: _n, lastRunHandled: _h, running: _r, ...rest } = r;
    try {
      await api.createRoutine({ ...rest, name: `${r.name} (copy)`, enabled: false });
      toast.success("Routine duplicated", { description: "The copy is disabled until you enable it." });
      invalidate();
    } catch (e) {
      toast.error("Couldn't duplicate routine", { description: (e as Error).message });
    }
  }

  async function confirmDelete() {
    if (!toDelete) return;
    try {
      await api.deleteRoutine(toDelete.id);
      toast.success(`Deleted “${toDelete.name}”`);
      invalidate();
    } catch (e) {
      toast.error("Couldn't delete routine", { description: (e as Error).message });
    }
    setToDelete(null);
  }

  const accountEmail = (id: string) => accounts?.find((a) => a.id === id)?.email ?? "—";

  return (
    <>
      <PageHeader
        title="Routines"
        subtitle="Rules that automatically maintain your Gmail on a schedule."
        actions={<Button asChild><Link to="/routines/new"><Plus /> New Routine</Link></Button>}
      />

      {error ? (
        <ErrorState message={(error as Error).message} onRetry={() => refetch()} />
      ) : isLoading || !routines ? (
        <div className="space-y-3">{Array.from({ length: 3 }).map((_, i) => <Skeleton key={i} className="h-32 w-full" />)}</div>
      ) : routines.length === 0 ? (
        <EmptyState
          icon={<Repeat className="size-8" />}
          title="No routines yet."
          description="Routines run in the background on a schedule. You keep using Gmail as usual."
          action={<Button asChild><Link to="/routines/new"><Plus /> Create your first routine</Link></Button>}
        />
      ) : (
        <ul className="divide-y rounded-lg border bg-card shadow-subtle">
          {routines.map((r) => (
            <li key={r.id} className={cn("p-5 transition-opacity", !r.enabled && "opacity-70")}>
              <div className="flex flex-col gap-4 lg:flex-row lg:items-start">
                <div className="min-w-0 flex-1 space-y-2">
                  <div className="flex items-center gap-3">
                    <Link to="/routines/$id" params={{ id: r.id }} className="truncate font-medium hover:underline">
                      {r.name}
                    </Link>
                    <span className="hidden text-xs text-muted-foreground sm:inline">{accountEmail(r.accountId)}</span>
                  </div>
                  <div className="break-all"><Code>{r.gmailQuery}</Code></div>
                  <p className="text-sm text-muted-foreground">
                    {describeSchedule(r.schedule)} <span className="mx-1.5">·</span>
                    <span className="text-foreground">{describeAction(r.action)}</span>
                  </p>
                </div>

                <dl className="grid grid-cols-3 gap-4 text-sm lg:w-[380px] lg:shrink-0">
                  <div><dt className="text-xs text-muted-foreground">Last run</dt><dd className="mt-0.5 whitespace-nowrap">{relativeDay(r.lastRunAt)}</dd></div>
                  <div><dt className="text-xs text-muted-foreground">Handled</dt><dd className="mt-0.5 tabular-nums">{r.lastRunHandled != null ? `${num(r.lastRunHandled)} emails` : "—"}</dd></div>
                  <div><dt className="text-xs text-muted-foreground">Next run</dt><dd className="mt-0.5 whitespace-nowrap">{r.enabled ? relativeDay(r.nextRunAt) : "Paused"}</dd></div>
                </dl>

                <div className="flex items-center gap-2 lg:shrink-0">
                  <label className="mr-1 flex items-center gap-2 text-sm">
                    <Switch
                      checked={r.enabled}
                      onCheckedChange={(enabled) => requestToggle(r, enabled)}
                      aria-label={`${r.enabled ? "Disable" : "Enable"} ${r.name}`}
                    />
                    <span className="w-14 text-muted-foreground">{r.enabled ? "Enabled" : "Disabled"}</span>
                  </label>
                  <Button variant="outline" size="sm" onClick={() => openPreview(r)}><Eye /> <span className="sr-only sm:not-sr-only">Preview</span></Button>
                  <Button variant="outline" size="sm" onClick={() => requestRun(r, false)} disabled={running === r.id || r.running}>
                    {running === r.id || r.running ? <Loader2 className="animate-spin" /> : <Play />}
                    <span className="sr-only sm:not-sr-only">Run now</span>
                  </Button>
                  <DropdownMenu>
                    <DropdownMenuTrigger asChild>
                      <Button variant="ghost" size="icon" aria-label="More actions"><MoreHorizontal /></Button>
                    </DropdownMenuTrigger>
                    <DropdownMenuContent align="end">
                      <DropdownMenuItem onClick={() => navigate({ to: "/routines/$id", params: { id: r.id } })}><Pencil /> Edit</DropdownMenuItem>
                      <DropdownMenuItem onClick={() => duplicate(r)}><Copy /> Duplicate</DropdownMenuItem>
                      <DropdownMenuItem disabled={running === r.id || r.running} onClick={() => requestRun(r, false)}><Play /> Run now</DropdownMenuItem>
                      <DropdownMenuItem disabled={running === r.id || r.running} onClick={() => requestRun(r, true)}><FlaskConical /> Dry run (no changes)</DropdownMenuItem>
                      <DropdownMenuItem onClick={() => openPreview(r)}><Eye /> Preview</DropdownMenuItem>
                      <DropdownMenuSeparator />
                      <DropdownMenuItem className="text-destructive focus:text-destructive" onClick={() => setToDelete(r)}><Trash2 /> Delete</DropdownMenuItem>
                    </DropdownMenuContent>
                  </DropdownMenu>
                </div>
              </div>
            </li>
          ))}
        </ul>
      )}

      <PreviewDialog
        open={!!preview}
        onOpenChange={(o) => !o && setPreview(null)}
        loading={!!preview && !preview.result}
        result={preview?.result}
        query={preview?.routine.gmailQuery ?? ""}
      />

      <AlertDialog open={!!confirm} onOpenChange={(o) => !o && setConfirm(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              {confirm?.kind === "enable" ? "Enable" : "Run"} “{confirm?.routine.name}”?
            </AlertDialogTitle>
            <AlertDialogDescription>
              {confirm?.kind === "enable"
                ? "On its schedule this routine will move every matching email to Gmail Trash."
                : "This will move matching emails to Gmail Trash right now."}{" "}
              MailRoutine never deletes permanently; Gmail keeps trashed emails for 30 days. Tip: use Preview or Dry run first.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Cancel</AlertDialogCancel>
            <AlertDialogAction
              onClick={() => {
                if (!confirm) return;
                if (confirm.kind === "enable") toggle.mutate({ id: confirm.routine.id, enabled: true, confirmed: true });
                else void runNow(confirm.routine, false, true);
              }}
            >
              {confirm?.kind === "enable" ? "Enable routine" : "Move to Trash"}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>

      <AlertDialog open={!!toDelete} onOpenChange={(o) => !o && setToDelete(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Delete “{toDelete?.name}”?</AlertDialogTitle>
            <AlertDialogDescription>
              The routine, its schedule and its run history will be removed. No emails in Gmail are affected.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Cancel</AlertDialogCancel>
            <AlertDialogAction className="bg-destructive text-destructive-foreground hover:bg-destructive/90" onClick={confirmDelete}>
              Delete routine
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </>
  );
}
