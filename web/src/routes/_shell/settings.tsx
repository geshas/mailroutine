import { useEffect, useMemo, useState } from "react";
import { useNavigate } from "@tanstack/react-router";
import { LogOut } from "lucide-react";
import { createFileRoute } from "@tanstack/react-router";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { AlertTriangle, ExternalLink, Mail, Monitor, Moon, Plus, Sun } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Label } from "@/components/ui/label";
import { Skeleton } from "@/components/ui/skeleton";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import {
  AlertDialog, AlertDialogAction, AlertDialogCancel, AlertDialogContent, AlertDialogDescription,
  AlertDialogFooter, AlertDialogHeader, AlertDialogTitle, AlertDialogTrigger,
} from "@/components/ui/alert-dialog";
import { PageHeader, Section } from "@/components/mr/common";
import { TraceLabels } from "@/components/mr/TraceLabels";
import { useTheme } from "@/lib/theme";
import { api } from "@/lib/api/client";
import type { ApiError } from "@/lib/api/types";
import { q } from "@/lib/queries";
import { usePageTitle } from "@/lib/title";
import type { ThemePreference } from "@/lib/api/types";
import { cn } from "@/lib/utils";

// The Go backend redirects back here after Google's consent screen.
type Search = { connected?: string | undefined; oauth_error?: string | undefined };

export const Route = createFileRoute("/_shell/settings")({
  // The router JSON-parses search values, so `?connected=1` arrives as the number 1.
  validateSearch: (s: Record<string, unknown>): Search => ({
    connected: typeof s["connected"] === "string" || typeof s["connected"] === "number" ? String(s["connected"]) : undefined,
    oauth_error: typeof s["oauth_error"] === "string" ? s["oauth_error"] : undefined,
  }),
  component: SettingsPage,
});

export const OAUTH_MESSAGES: Record<string, string> = {
  access_denied: "Google access was not granted, so nothing was connected.",
  state_mismatch: "The sign-in attempt could not be verified (it expired or was tampered with). Please try again.",
  no_refresh_token: "Google did not return a refresh token. Remove MailRoutine at myaccount.google.com/permissions and connect again.",
  exchange_failed: "Google rejected the authorization code. Check your client ID/secret and redirect URI, then try again.",
  insufficient_scope: "Google did not grant every Gmail permission MailRoutine needs, so the account was not connected. Connect again and keep all requested permissions ticked.",
  profile_failed: "Connected to Google but could not read the mailbox address. Is the Gmail API enabled for your project?",
};

/** All IANA zones the browser knows, always including the one currently configured. */
function timezoneOptions(current: string | undefined): string[] {
  let zones: string[] = [];
  try {
    zones = (Intl as unknown as { supportedValuesOf(k: string): string[] }).supportedValuesOf("timeZone");
  } catch {
    /* very old browser: only the current zone is offered */
  }
  const all = new Set(["UTC", ...zones]);
  if (current) all.add(current);
  return [...all].sort();
}

function SettingsPage() {
  usePageTitle("Settings");
  const search = Route.useSearch();
  const qc = useQueryClient();
  const { theme, setTheme } = useTheme();
  const settings = useQuery(q.settings());
  const accounts = useQuery(q.accounts());

  async function setTimezone(tz: string) {
    try {
      await api.updateSettings({ timezone: tz });
      qc.invalidateQueries({ queryKey: ["settings"] });
      qc.invalidateQueries({ queryKey: ["routines"] });
      toast.success(`Timezone set to ${tz}`, { description: "Existing routine schedules now follow it." });
    } catch (e) {
      toast.error("Couldn't change timezone", { description: (e as Error).message });
    }
  }

  const [oauthMissing, setOauthMissing] = useState(false);
  // Stays visible (the toast is transient) until the person starts another connection.
  // Captured on first render: arriving from Google is a full page load, and the query string is cleaned right after.
  const [scopeMissing, setScopeMissing] = useState(() => search.oauth_error === "insufficient_scope");
  const navigate = useNavigate();
  const timezones = useMemo(() => timezoneOptions(settings.data?.timezone), [settings.data?.timezone]);

  // Report the outcome of the Google redirect once, then clean the URL.
  useEffect(() => {
    if (!search.connected && !search.oauth_error) return;
    if (search.connected) {
      toast.success("Gmail account connected");
      qc.invalidateQueries({ queryKey: ["accounts"] });
    } else if (search.oauth_error) {
      toast.error("Couldn't connect Gmail", { description: OAUTH_MESSAGES[search.oauth_error] ?? `Google sign-in failed (${search.oauth_error}).` });
    }
    void navigate({ to: "/settings", search: {}, replace: true });
  }, [search.connected, search.oauth_error, navigate, qc]);

  async function logout() {
    await qc.cancelQueries();
    qc.clear();
    try { await api.logout(); } finally { navigate({ to: "/login", replace: true }); }
  }

  async function connect() {
    setScopeMissing(false);
    let url: string;
    try {
      ({ url } = await api.connectGoogleUrl());
    } catch (e) {
      const err = e as ApiError;
      if (err.code === "oauth_not_configured" || err.status === 501 || err.status === 503) {
        setOauthMissing(true);
        return;
      }
      toast.error("Couldn't start Gmail connection", { description: err.message });
      return;
    }
    window.location.assign(url);
  }

  async function disconnect(id: string) {
    try {
      await api.deleteAccount(id);
      qc.invalidateQueries({ queryKey: ["accounts"] });
      qc.invalidateQueries({ queryKey: ["routines"] });
      toast.success("Gmail account disconnected");
    } catch (e) {
      toast.error("Couldn't disconnect", { description: (e as Error).message });
    }
  }

  return (
    <>
      <PageHeader title="Settings" />

      <Section title="Appearance" description="Applies immediately on this device.">
        <div role="group" aria-label="Theme" className="grid max-w-md grid-cols-3 gap-2">
          {([
            { v: "system", l: "System", I: Monitor },
            { v: "light", l: "Light", I: Sun },
            { v: "dark", l: "Dark", I: Moon },
          ] as { v: ThemePreference; l: string; I: typeof Sun }[]).map(({ v, l, I }) => (
            <button
              key={v}
              type="button"
              onClick={() => setTheme(v)}
              aria-pressed={theme === v}
              className={cn(
                "flex flex-col items-center gap-2 rounded-md border px-3 py-4 text-sm transition-colors hover:bg-muted/50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 focus-visible:ring-offset-background",
                theme === v && "border-primary bg-accent text-accent-foreground",
              )}
            >
              <I className="size-4" aria-hidden="true" /> {l}
            </button>
          ))}
        </div>
      </Section>

      <Section title="General">
        <div className="space-y-2">
          <Label>Timezone</Label>
          {settings.data ? (
            <Select value={settings.data.timezone} onValueChange={setTimezone}>
              <SelectTrigger className="max-w-xs" aria-label="Timezone"><SelectValue /></SelectTrigger>
              <SelectContent>{timezones.map((t) => <SelectItem key={t} value={t}>{t}</SelectItem>)}</SelectContent>
            </Select>
          ) : <Skeleton className="h-9 w-64" />}
          <p className="text-sm text-muted-foreground">Routine schedules use this timezone.</p>
        </div>
      </Section>

      <Section title="Gmail accounts" description="MailRoutine works alongside Gmail. You keep reading and sending mail there.">
        {accounts.isLoading ? (
          <Skeleton className="h-16 w-full" />
        ) : accounts.data?.length ? (
          <ul className="divide-y rounded-lg border">
            {accounts.data.map((a) => (
              <li key={a.id} className="flex flex-col gap-3 p-4 sm:flex-row sm:items-center">
                <div className="flex flex-1 items-center gap-3">
                  <div className="flex size-9 items-center justify-center rounded-md bg-muted"><Mail className="size-4" /></div>
                  <div className="min-w-0">
                    <p className="text-xs text-muted-foreground">Gmail</p>
                    <p className="truncate text-sm font-medium">{a.email}</p>
                  </div>
                  {a.status === "connected"
                    ? <Badge variant="success">Connected</Badge>
                    : <Badge variant="warning">Needs attention</Badge>}
                </div>
                {a.status === "expired" && (
                  <div className="flex items-center gap-2 text-sm text-warning">
                    <AlertTriangle className="size-4" /> Gmail connection needs attention.
                    <Button size="sm" onClick={connect}>Reconnect Gmail</Button>
                  </div>
                )}
                <AlertDialog>
                  <AlertDialogTrigger asChild><Button variant="outline" size="sm">Disconnect</Button></AlertDialogTrigger>
                  <AlertDialogContent>
                    <AlertDialogHeader>
                      <AlertDialogTitle>Disconnect {a.email}?</AlertDialogTitle>
                      <AlertDialogDescription>This account's routines and their run history will be deleted, and MailRoutine's access will be revoked at Google. Your Gmail itself is not changed.</AlertDialogDescription>
                    </AlertDialogHeader>
                    <AlertDialogFooter>
                      <AlertDialogCancel>Cancel</AlertDialogCancel>
                      <AlertDialogAction onClick={() => disconnect(a.id)}>Disconnect</AlertDialogAction>
                    </AlertDialogFooter>
                  </AlertDialogContent>
                </AlertDialog>
              </li>
            ))}
          </ul>
        ) : (
          <div className="rounded-lg border border-dashed p-6 text-sm">
            <p className="font-medium">No Gmail account connected.</p>
            <p className="mt-1 text-muted-foreground">Connect Gmail to start creating routines.</p>
          </div>
        )}
        {scopeMissing && (
          <div role="alert" className="flex flex-col gap-3 rounded-md border border-destructive/30 bg-danger-soft p-3 text-sm sm:flex-row sm:items-center">
            <AlertTriangle className="size-4 shrink-0 text-destructive" />
            <p className="flex-1">
              <strong>Gmail permission missing.</strong> {OAUTH_MESSAGES["insufficient_scope"]}
            </p>
            <Button size="sm" onClick={connect}>Reconnect Gmail</Button>
          </div>
        )}
        {oauthMissing && (
          <div role="alert" className="flex gap-2.5 rounded-md border border-warning/30 bg-warning-soft p-3 text-sm">
            <AlertTriangle className="mt-0.5 size-4 shrink-0 text-warning" />
            <p>Google OAuth is not configured on this server. Set the Google client ID and secret environment variables as described in the README, then restart MailRoutine.</p>
          </div>
        )}
        <Button variant="outline" onClick={connect}><Plus /> Connect Gmail</Button>
      </Section>

      <Section
        title="MailRoutine labels"
        description="Gmail labels that let you see what MailRoutine handled. Routines with Traceability on add them to every email they touch."
      >
        {accounts.isLoading ? <Skeleton className="h-16 w-full" /> : <TraceLabels accounts={accounts.data ?? []} />}
        <p className="text-sm text-muted-foreground">
          Creates <code className="rounded bg-muted px-1.5 py-0.5 font-mono text-xs">MailRoutine</code> with Archive, Trash, Read, Unread, AddLabel and RemoveLabel
          inside it. Labels that already exist are left untouched, and nothing is ever duplicated.
        </p>
      </Section>

      <Section title="Application">
        <div className="space-y-1 text-sm">
          <p className="font-medium">MailRoutine</p>
          <p className="text-muted-foreground">Version {settings.data?.version ?? "…"} · Self-hosted</p>
        </div>
        <div className="flex flex-wrap gap-x-6 gap-y-2 text-sm">
          {settings.data && (
            <>
              <a href={settings.data.repositoryUrl} target="_blank" rel="noreferrer noopener" className="inline-flex items-center gap-1 text-muted-foreground hover:text-foreground">GitHub <ExternalLink className="size-3" /></a>
              <a href={settings.data.documentationUrl} target="_blank" rel="noreferrer noopener" className="inline-flex items-center gap-1 text-muted-foreground hover:text-foreground">Documentation <ExternalLink className="size-3" /></a>
              <span className="text-muted-foreground">License: {settings.data.license}</span>
            </>
          )}
        </div>
        {settings.data && (
          <p className="text-xs text-muted-foreground">
            Google OAuth redirect URI: <code className="rounded bg-muted px-1.5 py-0.5 font-mono">{settings.data.googleRedirectUrl}</code>
          </p>
        )}
      </Section>
      <Section title="Session">
        <Button variant="outline" onClick={logout}><LogOut /> Log out</Button>
      </Section>
    </>
  );
}
