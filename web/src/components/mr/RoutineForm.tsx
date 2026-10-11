import { Link, useNavigate } from "@tanstack/react-router";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useMemo, useRef, useState } from "react";
import { toast } from "sonner";
import { AlertTriangle, ArrowLeft, ExternalLink, Eye, Info, Loader2, Mail } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Switch } from "@/components/ui/switch";
import { Checkbox } from "@/components/ui/checkbox";
import { Textarea } from "@/components/ui/textarea";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { ToggleGroup, ToggleGroupItem } from "@/components/ui/toggle-group";
import { RadioGroup, RadioGroupItem } from "@/components/ui/radio-group";
import {
  AlertDialog, AlertDialogAction, AlertDialogCancel, AlertDialogContent, AlertDialogDescription,
  AlertDialogFooter, AlertDialogHeader, AlertDialogTitle, AlertDialogTrigger,
} from "@/components/ui/alert-dialog";
import { Section } from "./common";
import { PreviewDialog } from "./PreviewDialog";
import { api, onUnauthorized } from "@/lib/api/client";
import { ApiError } from "@/lib/api/types";
import { q } from "@/lib/queries";
import type { ActionType, PreviewResult, Routine, RoutineInput, ScheduleType } from "@/lib/api/types";
import { ACTION_LABELS, DAYS, SCHEDULE_LABELS, TRACE_LABELS, buildGmailQuery, describeAction, describeSchedule, emptyQueryBuilder, queryTargetsInbox, relativeDay, windowIsEmpty, type QueryBuilder } from "@/lib/format";
import { cn } from "@/lib/utils";

const DRAFT_KEY = (id?: string) => `mailroutine.draft.${id ?? "new"}`;

function parseQuery(query: string): QueryBuilder | null {
  const b: QueryBuilder = { ...emptyQueryBuilder };
  if (!query.trim()) return b;
  for (const t of query.trim().split(/\s+/)) {
    let m: RegExpMatchArray | null;
    if ((m = t.match(/^label:(.+)$/))) b.label = m[1]!;
    else if ((m = t.match(/^from:(.+)$/))) b.from = m[1]!;
    else if ((m = t.match(/^older_than:(\d+)([dmy])$/))) { b.olderThan = m[1]!; b.olderUnit = m[2] as "d"; }
    else if ((m = t.match(/^(after|before):(\d{4})[/-](\d{2})[/-](\d{2})$/))) b[m[1] as "after" | "before"] = `${m[2]}-${m[3]}-${m[4]}`;
    else if (/^(in|label):inbox$/i.test(t)) b.inboxOnly = true;
    else if (t === "is:read") b.read = "read";
    else if (t === "is:unread") b.read = "unread";
    else if (t === "is:starred") b.starred = "starred";
    else if (t === "-is:starred") b.starred = "not_starred";
    else return null;
  }
  return b;
}

interface Draft {
  name: string; accountId: string; advanced: boolean; builder: QueryBuilder; rawQuery: string;
  schedule: Routine["schedule"]; action: Routine["action"]; maxPerRun: string | null; enabled: boolean;
  traceability?: boolean;
}

function readDraft(key: string): Draft | null {
  try {
    const raw = sessionStorage.getItem(key);
    return raw ? (JSON.parse(raw) as Draft) : null;
  } catch {
    return null; // corrupt or unavailable storage: start from the stored routine
  }
}

export function RoutineForm({ routine }: { routine?: Routine }) {
  const isEdit = !!routine;
  const navigate = useNavigate();
  const qc = useQueryClient();
  const accounts = useQuery(q.accounts());
  const settings = useQuery(q.settings());
  // A draft saved when the session expired mid-edit wins over the stored routine.
  const draftKey = DRAFT_KEY(routine?.id);
  const [draft] = useState<Draft | null>(() => readDraft(draftKey));
  const initialBuilder = parseQuery(routine?.gmailQuery ?? "");
  const [name, setName] = useState(draft?.name ?? routine?.name ?? "");
  const [accountId, setAccountId] = useState(draft?.accountId ?? routine?.accountId ?? "");
  const [advanced, setAdvanced] = useState(draft?.advanced ?? !initialBuilder);
  const [builder, setBuilder] = useState<QueryBuilder>(draft?.builder ? { ...emptyQueryBuilder, ...draft.builder } : (initialBuilder ?? emptyQueryBuilder));
  const [rawQuery, setRawQuery] = useState(draft?.rawQuery ?? routine?.gmailQuery ?? "");
  const [schedule, setSchedule] = useState<Routine["schedule"]>(draft?.schedule ?? routine?.schedule ?? { type: "daily", time: "03:00" });
  const [action, setAction] = useState<Routine["action"]>(draft?.action ?? routine?.action ?? { type: "archive" });
  // null = untouched: new routines show the server's configured per-run limit (never a number made up here).
  const [maxTyped, setMaxTyped] = useState<string | null>(draft?.maxPerRun ?? (routine ? String(routine.maxMessagesPerRun) : null));
  const [enabled, setEnabled] = useState(draft?.enabled ?? routine?.enabled ?? false);
  // Traceability is on by default so handled emails can be found later.
  const [traceability, setTraceability] = useState(draft?.traceability ?? routine?.traceability ?? true);
  const [saving, setSaving] = useState(false);
  // Moving mail to Trash (or any "Save & Enable") is confirmed explicitly before it is saved enabled.
  const [confirmEnable, setConfirmEnable] = useState<{ enable: boolean } | null>(null);
  const [preview, setPreview] = useState<{ open: boolean; result?: PreviewResult }>({ open: false });
  const [errors, setErrors] = useState<Partial<Record<"name" | "query" | "label" | "max" | "account" | "window", string>>>({});
  const maxPerRun = maxTyped ?? (settings.data ? String(settings.data.defaultMaxMessagesPerRun) : "");
  const setMaxPerRun = (v: string) => setMaxTyped(v);

  const builtQuery = useMemo(() => buildGmailQuery(builder), [builder]);
  const gmailQuery = advanced ? rawQuery : builtQuery;
  const effectiveAccount = accountId || accounts.data?.[0]?.id || "";
  const noAccounts = accounts.data && accounts.data.length === 0;
  const upd = (p: Partial<QueryBuilder>) => setBuilder((b) => ({ ...b, ...p }));

  // Gmail labels come from the backend for the selected account.
  const labels = useQuery({
    queryKey: ["labels", effectiveAccount],
    queryFn: ({ signal }) => api.listLabels(effectiveAccount, signal),
    enabled: !!effectiveAccount,
    staleTime: 5 * 60_000,
  });
  const labelNames = labels.data?.filter((l) => l.type !== "system").map((l) => l.name) ?? [];

  // Preserve unsaved edits if the session expires mid-edit; they are restored after signing back in.
  const snapshot = useRef<Draft | null>(null);
  snapshot.current = { name, accountId, advanced, builder, rawQuery, schedule, action, maxPerRun: maxTyped, enabled, traceability };
  useEffect(() => {
    if (draft) {
      sessionStorage.removeItem(draftKey);
      toast.info("Restored your unsaved changes", { description: "They were kept when your session expired." });
    }
    return onUnauthorized(() => sessionStorage.setItem(draftKey, JSON.stringify(snapshot.current)));
  }, [draft, draftKey]);

  function validate(): RoutineInput | null {
    const e: Partial<Record<"name" | "query" | "label" | "max" | "account" | "window", string>> = {};
    if (!name.trim()) e.name = "Give the routine a name.";
    if (!gmailQuery.trim()) e.query = "Add at least one condition so the routine doesn't match every email.";
    if (!advanced && windowIsEmpty(builder)) e.window = "“After” must be earlier than “Before”, otherwise nothing can match.";
    if ((action.type === "add_label" || action.type === "remove_label") && !action.label?.trim()) e.label = "Choose a label.";
    const max = Number(maxPerRun);
    if (!Number.isInteger(max) || max < 1 || max > 10000) e.max = "Enter a number between 1 and 10,000.";
    if (!effectiveAccount) e.account = "Connect a Gmail account first.";
    setErrors(e);
    if (Object.keys(e).length) {
      // Move keyboard/screen-reader users to the first problem instead of leaving them on the button.
      const target = ({ name: "name", query: "query", label: "action-label", max: "max", window: "after" } as const)[
        (["name", "query", "label", "max", "window"] as const).find((k) => e[k]) ?? "name"
      ];
      document.getElementById(target)?.focus();
      toast.error("Please fix the highlighted fields");
      return null;
    }
    return {
      accountId: effectiveAccount,
      name: name.trim(),
      ...(routine?.description ? { description: routine.description } : {}),
      gmailQuery: gmailQuery.trim(),
      action: action.type === "add_label" || action.type === "remove_label" ? action : { type: action.type },
      schedule,
      enabled,
      traceability,
      maxMessagesPerRun: max,
    };
  }

  /** Validates, then either asks for confirmation (Trash / Save & Enable) or saves right away. */
  function attempt(enable?: boolean) {
    const input = validate();
    if (!input) return;
    const willBeEnabled = enable || input.enabled;
    if (enable || (willBeEnabled && input.action.type === "trash")) setConfirmEnable({ enable: !!enable });
    else void save(false, false);
  }

  async function save(enable: boolean, confirmed: boolean) {
    const input = validate();
    if (!input) return;
    if (enable) input.enabled = true;
    setSaving(true);
    try {
      const opts = { confirmDestructive: confirmed && input.enabled && input.action.type === "trash" };
      if (isEdit) await api.updateRoutine(routine.id, input, opts);
      else await api.createRoutine(input, opts);
      qc.invalidateQueries({ queryKey: ["routines"] });
      qc.invalidateQueries({ queryKey: ["dashboard"] });
      toast.success(isEdit ? "Routine saved" : input.enabled ? "Routine created and enabled" : "Routine saved (disabled)");
      navigate({ to: "/routines" });
    } catch (e) {
      if (e instanceof ApiError && Object.keys(e.fields).length) {
        const f = e.fields;
        setErrors({
          ...(f["name"] ? { name: f["name"] } : {}),
          ...(f["gmail_query"] ? { query: f["gmail_query"] } : {}),
          ...(f["action_config.label"] ? { label: f["action_config.label"] } : {}),
          ...(f["max_messages_per_run"] ? { max: f["max_messages_per_run"] } : {}),
          ...(f["account_id"] ? { account: f["account_id"] } : {}),
        });
      }
      toast.error("Couldn't save routine", { description: (e as Error).message });
    } finally {
      setSaving(false);
    }
  }

  async function runPreview() {
    setPreview({ open: true });
    try {
      // Always preview what is on screen (the edited query), not the stored one.
      const result = await api.previewDraft({ accountId: effectiveAccount, gmailQuery });
      setPreview({ open: true, result });
    } catch (e) {
      setPreview({ open: false });
      toast.error("Preview failed", { description: (e as Error).message });
    }
  }

  async function remove() {
    if (!routine) return;
    await api.deleteRoutine(routine.id);
    qc.invalidateQueries({ queryKey: ["routines"] });
    toast.success(`Deleted “${routine.name}”`);
    navigate({ to: "/routines" });
  }

  const needsLabel = action.type === "add_label" || action.type === "remove_label";

  return (
    <div className="pb-28">
      <Link to="/routines" className="mb-4 inline-flex items-center gap-1.5 text-sm text-muted-foreground hover:text-foreground">
        <ArrowLeft className="size-4" /> Routines
      </Link>
      <div className="mb-10 flex flex-col gap-4 sm:flex-row sm:items-end sm:justify-between">
        <div>
          <h1 className="text-2xl font-semibold tracking-tight">{isEdit ? routine.name : "New Routine"}</h1>
          <p className="mt-1 text-sm text-muted-foreground">
            {isEdit ? (
              <>Last run {relativeDay(routine.lastRunAt)} · Next run {enabled ? relativeDay(routine.nextRunAt) : "paused"}</>
            ) : (
              "Runs in the background. You keep using Gmail as usual."
            )}
          </p>
        </div>
        {isEdit && (
          <label className="flex items-center gap-2.5 rounded-md border px-3 py-2 text-sm">
            <Switch checked={enabled} onCheckedChange={setEnabled} />
            {enabled ? "Enabled" : "Disabled"}
          </label>
        )}
      </div>

      <Section title="General">
        <div className="space-y-2">
          <Label htmlFor="name">Routine name</Label>
          <Input id="name" placeholder="Archive old newsletters" value={name} onChange={(e) => setName(e.target.value)} className="max-w-md" aria-invalid={!!errors.name} aria-describedby={errors.name ? "name-error" : undefined} />
          {errors.name && <p id="name-error" role="alert" className="text-sm text-destructive">{errors.name}</p>}
        </div>
        <div className="space-y-2">
          <Label>Gmail account</Label>
          {noAccounts ? (
            <div className="flex flex-col gap-3 rounded-lg border border-dashed p-4 text-sm sm:flex-row sm:items-center">
              <Mail className="size-5 text-muted-foreground" />
              <p className="flex-1 text-muted-foreground">Connect a Gmail account in Settings before creating a routine.</p>
              <Button size="sm" asChild><Link to="/settings">Connect Gmail</Link></Button>
            </div>
          ) : (
            <Select value={effectiveAccount} onValueChange={setAccountId}>
              <SelectTrigger className="max-w-md" aria-label="Gmail account"><SelectValue placeholder="Loading accounts…" /></SelectTrigger>
              <SelectContent>
                {accounts.data?.map((a) => <SelectItem key={a.id} value={a.id}>{a.email}</SelectItem>)}
              </SelectContent>
            </Select>
          )}
        </div>
      </Section>

      <Section title="Which emails should match?" description="Build the search, or write a Gmail query directly.">
        <ToggleGroup
          type="single"
          value={advanced ? "advanced" : "simple"}
          onValueChange={(v) => {
            if (!v) return;
            if (v === "advanced") { setRawQuery(builtQuery); setAdvanced(true); }
            else {
              const parsed = parseQuery(rawQuery);
              if (!parsed) { toast.error("This query uses syntax the simple builder can't show", { description: "Stay in Advanced mode to keep it." }); return; }
              setBuilder(parsed); setAdvanced(false);
            }
          }}
          className="inline-flex rounded-md border p-0.5"
        >
          <ToggleGroupItem value="simple" className="h-7 px-3 text-xs">Simple builder</ToggleGroupItem>
          <ToggleGroupItem value="advanced" className="h-7 px-3 text-xs">Advanced query</ToggleGroupItem>
        </ToggleGroup>

        {!advanced && (
          <div className="grid gap-5 sm:grid-cols-2">
            <label htmlFor="inbox-only" className="flex items-start gap-3 sm:col-span-2">
              <Switch id="inbox-only" checked={builder.inboxOnly} onCheckedChange={(v) => upd({ inboxOnly: v })} className="mt-0.5" />
              <span>
                <span className="block text-sm font-medium">Only emails in the Inbox</span>
                <span className="block text-xs font-normal text-muted-foreground">Skips mail that is already archived or elsewhere (adds <code>in:inbox</code>).</span>
              </span>
            </label>
            <div className="space-y-2">
              <Label htmlFor="label">Label</Label>
              <Input id="label" list="label-suggestions" placeholder="Any label" value={builder.label} onChange={(e) => upd({ label: e.target.value })} />
              <datalist id="label-suggestions">{labelNames.map((l) => <option key={l} value={l} />)}</datalist>
            </div>
            <div className="space-y-2">
              <Label htmlFor="older">Older than</Label>
              <div className="flex gap-2">
                {/* min=0 so the spinner can step back to "no age filter"; 0 and blank both mean any age. */}
                <Input
                  id="older"
                  type="number"
                  min={0}
                  step={1}
                  inputMode="numeric"
                  placeholder="Any age"
                  value={builder.olderThan}
                  aria-describedby="older-help"
                  onChange={(e) => upd({ olderThan: e.target.value.startsWith("-") ? "0" : e.target.value.replace(/\..*$/, "") })}
                  className="w-28"
                />
                <Select value={builder.olderUnit} onValueChange={(v) => upd({ olderUnit: v as "d" })}>
                  <SelectTrigger className="w-32" aria-label="Older than unit"><SelectValue /></SelectTrigger>
                  <SelectContent>
                    <SelectItem value="d">days</SelectItem>
                    <SelectItem value="m">months</SelectItem>
                    <SelectItem value="y">years</SelectItem>
                  </SelectContent>
                </Select>
                {builder.olderThan !== "" && (
                  <Button type="button" variant="ghost" size="sm" onClick={() => upd({ olderThan: "" })} aria-label="Clear older than">Clear</Button>
                )}
              </div>
              <p id="older-help" className="text-xs text-muted-foreground">0 or empty means any age.</p>
            </div>
            <fieldset className="space-y-2 sm:col-span-2">
              <legend className="mb-2 text-sm font-medium leading-none">Received between <span className="font-normal text-muted-foreground">(optional)</span></legend>
              <div className="flex flex-wrap items-center gap-3">
                <div className="flex items-center gap-2">
                  <Label htmlFor="after" className="font-normal text-muted-foreground">After</Label>
                  <Input id="after" type="date" value={builder.after} max={builder.before || undefined} onChange={(e) => upd({ after: e.target.value })} className="w-40" aria-invalid={!!errors.window} aria-describedby="window-help" />
                </div>
                <div className="flex items-center gap-2">
                  <Label htmlFor="before" className="font-normal text-muted-foreground">Before</Label>
                  <Input id="before" type="date" value={builder.before} min={builder.after || undefined} onChange={(e) => upd({ before: e.target.value })} className="w-40" aria-invalid={!!errors.window} aria-describedby="window-help" />
                </div>
                {(builder.after || builder.before) && (
                  <Button type="button" variant="ghost" size="sm" onClick={() => upd({ after: "", before: "" })}>Clear</Button>
                )}
              </div>
              <p id="window-help" role={errors.window ? "alert" : undefined} className="text-xs text-muted-foreground">
                {errors.window ? <span className="text-destructive">{errors.window}</span> : "“After” includes that day, “Before” stops just before it. Combine with “Older than” for a moving window."}
              </p>
            </fieldset>
            <div className="space-y-2">
              <Label htmlFor="from">From <span className="font-normal text-muted-foreground">(optional)</span></Label>
              <Input id="from" placeholder="amazon.com" value={builder.from} onChange={(e) => upd({ from: e.target.value })} />
            </div>
            <div className="grid grid-cols-2 gap-3">
              <div className="space-y-2">
                <Label>Read status</Label>
                <Select value={builder.read} onValueChange={(v) => upd({ read: v as QueryBuilder["read"] })}>
                  <SelectTrigger aria-label="Read status"><SelectValue /></SelectTrigger>
                  <SelectContent>
                    <SelectItem value="any">Any</SelectItem>
                    <SelectItem value="read">Read</SelectItem>
                    <SelectItem value="unread">Unread</SelectItem>
                  </SelectContent>
                </Select>
              </div>
              <div className="space-y-2">
                <Label>Starred</Label>
                <Select value={builder.starred} onValueChange={(v) => upd({ starred: v as QueryBuilder["starred"] })}>
                  <SelectTrigger aria-label="Starred"><SelectValue /></SelectTrigger>
                  <SelectContent>
                    <SelectItem value="any">Any</SelectItem>
                    <SelectItem value="starred">Starred</SelectItem>
                    <SelectItem value="not_starred">Not starred</SelectItem>
                  </SelectContent>
                </Select>
              </div>
            </div>
          </div>
        )}

        <div className="space-y-2">
          <div className="flex items-center justify-between">
            <Label htmlFor="query">{advanced ? "Gmail query" : "Generated Gmail query"}</Label>
            <a href="https://support.google.com/mail/answer/7190" target="_blank" rel="noreferrer" className="inline-flex items-center gap-1 text-xs text-muted-foreground hover:text-foreground">
              Learn Gmail search syntax <ExternalLink className="size-3" />
            </a>
          </div>
          <Textarea
            id="query"
            rows={2}
            value={gmailQuery}
            readOnly={!advanced}
            onChange={(e) => setRawQuery(e.target.value)}
            onFocus={() => { if (!advanced) { setRawQuery(builtQuery); setAdvanced(true); } }}
            className={cn("resize-none font-mono text-sm", !advanced && "bg-muted/50")}
            placeholder="Choose conditions above, or type any Gmail search"
            aria-invalid={!!errors.query}
            aria-describedby="query-help"
          />
          <p id="query-help" role={errors.query ? "alert" : undefined} className="text-xs text-muted-foreground">
            {errors.query ? <span className="text-destructive">{errors.query}</span> : "This is exactly what MailRoutine will search for in Gmail."}
          </p>
        </div>

        {action.type === "archive" && gmailQuery.trim() && !queryTargetsInbox(gmailQuery) && (
          <div className="flex flex-col gap-2 rounded-md border border-info/30 bg-info-soft p-3 text-sm sm:flex-row sm:items-center">
            <Info className="size-4 shrink-0 text-info" />
            <p className="flex-1">
              Archiving only changes mail that is in the Inbox, so already-archived matches are counted but not touched.
            </p>
            <Button
              type="button" size="sm" variant="outline"
              onClick={() => (advanced ? setRawQuery(`in:inbox ${rawQuery}`.trim()) : upd({ inboxOnly: true }))}
            >
              Only search the Inbox
            </Button>
          </div>
        )}

        <Button type="button" variant="secondary" onClick={runPreview} disabled={!gmailQuery.trim()}>
          <Eye /> Preview matches
        </Button>
      </Section>

      <Section title="When should it run?">
        <ToggleGroup
          type="single"
          value={schedule.type}
          onValueChange={(v) => v && setSchedule((s) => ({ ...s, type: v as ScheduleType, time: s.time ?? "03:00" }))}
          className="flex flex-wrap justify-start gap-1.5"
        >
          {(Object.keys(SCHEDULE_LABELS) as ScheduleType[]).map((t) => (
            <ToggleGroupItem key={t} value={t} className="h-9 rounded-md border px-3.5 text-sm data-[state=on]:border-primary data-[state=on]:bg-accent data-[state=on]:text-accent-foreground">
              {SCHEDULE_LABELS[t]}
            </ToggleGroupItem>
          ))}
        </ToggleGroup>
        {schedule.type !== "manual" && (
          <div className="flex flex-wrap items-end gap-3">
            {schedule.type === "hourly" && (
              <div className="space-y-2">
                <Label htmlFor="minute">At minute</Label>
                <Input id="minute" type="number" min={0} max={59} value={schedule.minute ?? 0} onChange={(e) => setSchedule((s) => ({ ...s, minute: Number(e.target.value) }))} className="w-24" />
              </div>
            )}
            {schedule.type === "weekly" && (
              <div className="space-y-2">
                <Label>On</Label>
                <Select value={String(schedule.dayOfWeek ?? 0)} onValueChange={(v) => setSchedule((s) => ({ ...s, dayOfWeek: Number(v) }))}>
                  <SelectTrigger className="w-40" aria-label="Day of week"><SelectValue /></SelectTrigger>
                  <SelectContent>{DAYS.map((d, i) => <SelectItem key={d} value={String(i)}>{d}</SelectItem>)}</SelectContent>
                </Select>
              </div>
            )}
            {schedule.type === "monthly" && (
              <div className="space-y-2">
                <Label>Day of month</Label>
                <Select value={String(schedule.dayOfMonth ?? 1)} onValueChange={(v) => setSchedule((s) => ({ ...s, dayOfMonth: Number(v) }))}>
                  <SelectTrigger className="w-28" aria-label="Day of month"><SelectValue /></SelectTrigger>
                  <SelectContent>{Array.from({ length: 31 }, (_, i) => <SelectItem key={i} value={String(i + 1)}>{i + 1}</SelectItem>)}</SelectContent>
                </Select>
                <p className="max-w-52 text-xs text-muted-foreground">In shorter months it runs on the last day.</p>
              </div>
            )}
            {schedule.type !== "hourly" && (
              <div className="space-y-2">
                <Label htmlFor="time">At</Label>
                <Input id="time" type="time" value={schedule.time ?? "03:00"} onChange={(e) => setSchedule((s) => ({ ...s, time: e.target.value }))} className="w-32" />
              </div>
            )}
          </div>
        )}
        <p className="text-sm text-muted-foreground">
          {schedule.type === "manual"
            ? "Runs only when you press Run now."
            : <>Timezone: <span className="text-foreground">{settings.data?.timezone ?? "…"}</span> · <Link to="/settings" className="underline-offset-2 hover:underline">change</Link></>}
        </p>
      </Section>

      <Section title="What should happen?">
        <RadioGroup
          value={action.type}
          onValueChange={(v) => setAction((a) => (a.label !== undefined ? { type: v as ActionType, label: a.label } : { type: v as ActionType }))}
          className="grid gap-2 sm:grid-cols-2"
        >
          {(Object.keys(ACTION_LABELS) as ActionType[]).map((t) => (
            <Label
              key={t}
              htmlFor={`a-${t}`}
              className={cn(
                "flex cursor-pointer items-center gap-3 rounded-md border px-3.5 py-3 font-normal transition-colors hover:bg-muted/50",
                action.type === t && "border-primary bg-accent",
              )}
            >
              <RadioGroupItem id={`a-${t}`} value={t} />
              {ACTION_LABELS[t]}
            </Label>
          ))}
        </RadioGroup>
        {needsLabel && (
          <div className="space-y-2">
            <Label htmlFor="action-label">Label</Label>
            <Input id="action-label" list="label-suggestions" placeholder="e.g. Archive/Receipts" value={action.label ?? ""} onChange={(e) => setAction((a) => ({ ...a, label: e.target.value }))} className="max-w-md" aria-invalid={!!errors.label} aria-describedby={errors.label ? "label-error" : undefined} />
            {errors.label ? <p id="label-error" role="alert" className="text-sm text-destructive">{errors.label}</p> : (
              <p className="text-sm text-muted-foreground">
                {labels.isLoading ? "Loading Gmail labels…" : labels.error ? "Couldn't load Gmail labels. You can still type a label name." : `${labelNames.length} labels from your Gmail account.`}
              </p>
            )}
          </div>
        )}
        {action.type === "trash" && (
          <div className="flex gap-2.5 rounded-md border border-warning/30 bg-warning-soft p-3 text-sm">
            <AlertTriangle className="mt-0.5 size-4 shrink-0 text-warning" />
            <p>Messages are moved to Gmail Trash. MailRoutine does not permanently delete messages.</p>
          </div>
        )}
      </Section>

      <Section title="Safety">
        <div className="space-y-2">
          <Label htmlFor="max">Maximum emails per run</Label>
          <Input id="max" type="number" min={1} max={10000} value={maxPerRun} onChange={(e) => setMaxPerRun(e.target.value)} className="w-32" aria-invalid={!!errors.max} aria-describedby="max-help" />
          <p id="max-help" role={errors.max ? "alert" : undefined} className="text-sm text-muted-foreground">
            {errors.max ? <span className="text-destructive">{errors.max}</span> : "MailRoutine will stop after this number of matching emails during a single run."}
          </p>
        </div>
        <div className="flex items-start gap-3">
          <Checkbox id="traceability" checked={traceability} onCheckedChange={(v) => setTraceability(v === true)} className="mt-0.5" />
          <div className="space-y-1">
            <Label htmlFor="traceability" className="cursor-pointer">Traceability</Label>
            <p id="traceability-help" className="text-sm text-muted-foreground">
              Add the Gmail label <code className="rounded bg-muted px-1.5 py-0.5 font-mono text-xs">{TRACE_LABELS[action.type]}</code> to every
              email this routine handles, so you can always find what MailRoutine touched. Create the labels once under Settings → MailRoutine labels.
            </p>
          </div>
        </div>
        <p className="flex items-center gap-2 text-xs text-muted-foreground">
          <Info className="size-3.5" /> You can disable this routine at any time from the Routines list.
        </p>
      </Section>

      {isEdit && (
        <Section title="Danger zone">
          <AlertDialog>
            <AlertDialogTrigger asChild>
              <Button variant="outline" className="border-destructive/40 text-destructive hover:bg-danger-soft hover:text-destructive">Delete routine</Button>
            </AlertDialogTrigger>
            <AlertDialogContent>
              <AlertDialogHeader>
                <AlertDialogTitle>Delete “{routine.name}”?</AlertDialogTitle>
                <AlertDialogDescription>The routine and its schedule will be removed. No emails in Gmail are affected.</AlertDialogDescription>
              </AlertDialogHeader>
              <AlertDialogFooter>
                <AlertDialogCancel>Cancel</AlertDialogCancel>
                <AlertDialogAction className="bg-destructive text-destructive-foreground hover:bg-destructive/90" onClick={remove}>Delete routine</AlertDialogAction>
              </AlertDialogFooter>
            </AlertDialogContent>
          </AlertDialog>
        </Section>
      )}

      <div className="fixed inset-x-0 bottom-0 z-20 border-t bg-background/95 backdrop-blur md:left-[var(--sidebar-width)]">
        <div className="mx-auto flex max-w-6xl flex-wrap items-center justify-end gap-2 px-4 py-3 sm:px-8">
          <Button variant="ghost" asChild><Link to="/routines">Cancel</Link></Button>
          <Button variant="outline" onClick={runPreview} disabled={!gmailQuery.trim()} className="hidden sm:inline-flex"><Eye /> Preview matches</Button>
          <Button variant="outline" onClick={() => attempt()} disabled={saving || !!noAccounts}>Save Routine</Button>
          {!(isEdit && enabled) && (
            <Button onClick={() => attempt(true)} disabled={saving || !!noAccounts}>
              {saving && <Loader2 className="animate-spin" />} Save & Enable
            </Button>
          )}
        </div>
      </div>

      <PreviewDialog
        open={preview.open}
        onOpenChange={(o) => setPreview((p) => ({ ...p, open: o }))}
        loading={preview.open && !preview.result}
        result={preview.result}
        query={gmailQuery}
        footer={
          <>
            <Button variant="ghost" onClick={() => setPreview({ open: false })}>Close</Button>
            <Button variant="outline" onClick={() => { setPreview({ open: false }); attempt(); }} disabled={!!noAccounts}>Save Routine</Button>
            <Button onClick={() => { setPreview({ open: false }); attempt(true); }} disabled={!!noAccounts}>Save & Enable</Button>
          </>
        }
      />
      <AlertDialog open={!!confirmEnable} onOpenChange={(o) => !o && setConfirmEnable(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{confirmEnable?.enable ? "Enable" : "Save"} “{name || "this routine"}”?</AlertDialogTitle>
            <AlertDialogDescription asChild>
              <div className="space-y-2 text-sm">
                <p>MailRoutine will modify your Gmail automatically:</p>
                <ul className="list-disc space-y-1 pl-5 text-foreground">
                  <li><strong>Action:</strong> {describeAction(action)}</li>
                  <li><strong>Schedule:</strong> {describeSchedule(schedule)}</li>
                  <li><strong>Limit:</strong> at most {maxPerRun} emails per run</li>
                  <li className="break-all"><strong>Query:</strong> <code>{gmailQuery}</code></li>
                </ul>
                {action.type === "trash" && <p>Trash is not permanent deletion — Gmail keeps trashed messages for 30 days.</p>}
                <p>Tip: preview matches first. You can disable the routine at any time.</p>
              </div>
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Cancel</AlertDialogCancel>
            <AlertDialogAction onClick={() => void save(!!confirmEnable?.enable, true)}>{confirmEnable?.enable ? "Save & Enable" : "Save routine"}</AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}
