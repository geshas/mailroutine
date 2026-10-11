import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Check, Loader2, Tag } from "lucide-react";
import { toast } from "sonner";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { api } from "@/lib/api/client";
import type { GmailAccount } from "@/lib/api/types";
import { q } from "@/lib/queries";

/** One account's MailRoutine/* labels: which exist in Gmail, and a button to create the missing ones. */
function AccountTraceLabels({ account }: { account: GmailAccount }) {
  const qc = useQueryClient();
  const labels = useQuery({ ...q.traceLabels(account.id), enabled: account.status === "connected" });

  const create = useMutation({
    mutationFn: () => api.createTraceLabels(account.id),
    onSuccess: (report) => {
      qc.setQueryData(q.traceLabels(account.id).queryKey, report);
      qc.invalidateQueries({ queryKey: ["labels", account.id] });
      toast.success(
        report.created.length
          ? `Created ${report.created.length} ${report.created.length === 1 ? "label" : "labels"} in ${account.email}`
          : `All MailRoutine labels already exist in ${account.email}`,
        report.created.length ? { description: report.created.join(", ") } : {},
      );
    },
    onError: (e) => toast.error("Couldn't create labels", { description: (e as Error).message }),
  });

  const items = labels.data?.items ?? [];
  const missing = items.filter((i) => !i.exists).length;

  return (
    <li className="space-y-3 p-4">
      <div className="flex flex-col gap-3 sm:flex-row sm:items-center">
        <div className="flex min-w-0 flex-1 items-center gap-3">
          <div className="flex size-9 items-center justify-center rounded-md bg-muted"><Tag className="size-4" aria-hidden="true" /></div>
          <div className="min-w-0">
            <p className="truncate text-sm font-medium">{account.email}</p>
            <p className="text-xs text-muted-foreground" aria-live="polite">
              {account.status !== "connected"
                ? "Reconnect this account to manage its labels."
                : labels.isLoading
                  ? "Checking Gmail…"
                  : labels.error
                    ? "Couldn't check Gmail labels."
                    : missing === 0
                      ? "All MailRoutine labels exist."
                      : `${missing} of ${items.length} labels missing.`}
            </p>
          </div>
        </div>
        {account.status === "connected" && (
          <Button
            variant="outline"
            size="sm"
            onClick={() => create.mutate()}
            disabled={create.isPending || labels.isLoading || (labels.isSuccess && missing === 0)}
          >
            {create.isPending ? <Loader2 className="animate-spin" /> : missing === 0 && labels.isSuccess ? <Check /> : <Tag />}
            {labels.isSuccess && missing === 0 ? "Labels ready" : "Create labels"}
          </Button>
        )}
      </div>
      {labels.isLoading && account.status === "connected" && <Skeleton className="h-6 w-full" />}
      {items.length > 0 && (
        <ul className="flex flex-wrap gap-1.5" aria-label={`MailRoutine labels in ${account.email}`}>
          {items.map((l) => (
            <li key={l.name}>
              <Badge variant={l.exists ? "success" : "muted"} className="gap-1 font-mono text-xs font-normal">
                {l.exists ? <Check className="size-3" aria-hidden="true" /> : null}
                {l.name}
                <span className="sr-only">{l.exists ? " (exists)" : " (missing)"}</span>
              </Badge>
            </li>
          ))}
        </ul>
      )}
    </li>
  );
}

export function TraceLabels({ accounts }: { accounts: GmailAccount[] }) {
  if (accounts.length === 0) {
    return <p className="text-sm text-muted-foreground">Connect a Gmail account to create its MailRoutine labels.</p>;
  }
  return (
    <ul className="divide-y rounded-lg border">
      {accounts.map((a) => (
        <AccountTraceLabels key={a.id} account={a} />
      ))}
    </ul>
  );
}
