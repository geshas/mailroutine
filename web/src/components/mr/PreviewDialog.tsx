import type { ReactNode } from "react";
import { Info, Loader2 } from "lucide-react";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Button } from "@/components/ui/button";
import type { PreviewResult } from "@/lib/api/types";
import { num } from "@/lib/format";
import { Code } from "./common";

export function PreviewDialog({
  open,
  onOpenChange,
  loading,
  result,
  query,
  footer,
}: {
  open: boolean;
  onOpenChange: (o: boolean) => void;
  loading: boolean;
  result?: PreviewResult | undefined;
  query: string;
  footer?: ReactNode;
}) {
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-2xl">
        <DialogHeader>
          <DialogTitle>Preview matches <span className="ml-1 rounded bg-muted px-1.5 py-0.5 align-middle text-xs font-medium text-muted-foreground">Read-only</span></DialogTitle>
          <DialogDescription className="break-all">
            Query: <Code>{query || "(empty)"}</Code>
          </DialogDescription>
        </DialogHeader>
        {loading ? (
          <div className="flex items-center justify-center gap-2 py-12 text-sm text-muted-foreground">
            <Loader2 className="size-4 animate-spin" /> Searching Gmail…
          </div>
        ) : result ? (
          <div className="space-y-4">
            <p className="text-sm">
              <span className="text-2xl font-semibold tabular-nums">
                {result.isEstimate ? "≈ " : ""}
                {num(result.total)}
              </span>{" "}
              <span className="text-muted-foreground">
                {result.isEstimate
                  ? "emails match (Gmail's estimate, not an exact count)."
                  : result.total === 1
                    ? "email currently matches this routine."
                    : "emails currently match this routine."}
                {result.hasMore && ` Showing the first ${num(result.samples.length)}.`}
              </span>
            </p>
            {result.samples.length > 0 && (
              <div className="overflow-x-auto rounded-lg border">
                <Table>
                  <TableHeader>
                    <TableRow className="hover:bg-transparent">
                      <TableHead>Sender</TableHead>
                      <TableHead>Subject</TableHead>
                      <TableHead className="text-right">Date</TableHead>
                    </TableRow>
                  </TableHeader>
                  <TableBody>
                    {result.samples.map((m) => (
                      <TableRow key={m.id} className="hover:bg-transparent">
                        <TableCell className="max-w-48 truncate font-medium" title={m.sender}>{m.sender}</TableCell>
                        <TableCell className="max-w-64 truncate text-muted-foreground" title={m.subject}>{m.subject || "(no subject)"}</TableCell>
                        <TableCell className="whitespace-nowrap text-right text-muted-foreground">
                          {Number.isNaN(Date.parse(m.date)) ? "—" : new Date(m.date).toLocaleDateString([], { month: "short", day: "numeric", year: "numeric" })}
                        </TableCell>
                      </TableRow>
                    ))}
                  </TableBody>
                </Table>
              </div>
            )}
            <p className="flex items-center gap-1.5 text-xs text-muted-foreground">
              <Info className="size-3.5" /> Preview is read-only — nothing in Gmail is changed. Only sender, subject and date are shown.
            </p>
          </div>
        ) : null}
        <DialogFooter className="gap-2 sm:gap-0">
          {footer ?? <Button variant="outline" onClick={() => onOpenChange(false)}>Close</Button>}
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
