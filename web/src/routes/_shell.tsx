import { createFileRoute, Link, Outlet, redirect, useNavigate, useRouterState } from "@tanstack/react-router";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useRef } from "react";
import { toast } from "sonner";
import { AlertTriangle } from "lucide-react";
import { SidebarProvider, SidebarTrigger } from "@/components/ui/sidebar";
import { Button } from "@/components/ui/button";
import { AppSidebar } from "@/components/mr/AppSidebar";
import { Logo } from "@/components/mr/common";
import { api, onUnauthorized } from "@/lib/api/client";
import { useSessionActivity } from "@/lib/session-activity";
import { q } from "@/lib/queries";

export const Route = createFileRoute("/_shell")({
  beforeLoad: async () => {
    let session = null;
    try {
      session = await api.session();
    } catch {
      // Server unreachable: send to login, which shows the connection error on sign-in.
    }
    if (!session) throw redirect({ to: "/login" });
    return { session };
  },
  component: ShellLayout,
});

function GmailAttentionBanner() {
  const { data } = useQuery(q.accounts());
  if (!data?.some((a) => a.status === "expired")) return null;
  return (
    <div role="alert" className="mb-6 flex flex-col gap-3 rounded-lg border border-destructive/40 bg-destructive/5 p-4 text-sm sm:flex-row sm:items-center sm:justify-between">
      <span className="flex items-center gap-2 font-medium">
        <AlertTriangle className="size-4 text-destructive" /> Gmail connection needs attention.
      </span>
      <Button size="sm" asChild><Link to="/settings">Reconnect Gmail</Link></Button>
    </div>
  );
}

function ShellLayout() {
  const { session } = Route.useRouteContext();
  const navigate = useNavigate();
  const qc = useQueryClient();
  useSessionActivity();

  // Single-page navigation: move focus to the new page so keyboard and screen-reader users notice it.
  const mainRef = useRef<HTMLElement>(null);
  const path = useRouterState({ select: (s) => s.location.pathname });
  const firstRender = useRef(true);
  useEffect(() => {
    if (firstRender.current) {
      firstRender.current = false;
      return;
    }
    mainRef.current?.focus({ preventScroll: true });
  }, [path]);

  useEffect(
    () =>
      onUnauthorized(() => {
        void qc.cancelQueries();
        qc.clear();
        toast.error("Your session has expired", { description: "Please sign in again." });
        navigate({ to: "/login", replace: true });
      }),
    [navigate, qc],
  );

  return (
    <SidebarProvider>
      <a
        href="#main"
        onClick={(e) => { e.preventDefault(); mainRef.current?.focus(); }}
        className="sr-only focus:not-sr-only focus:fixed focus:left-4 focus:top-4 focus:z-50 focus:rounded-md focus:bg-primary focus:px-4 focus:py-2 focus:text-primary-foreground"
      >
        Skip to content
      </a>
      <div className="flex min-h-screen w-full">
        <AppSidebar username={session.username} />
        <div className="flex min-w-0 flex-1 flex-col">
          <header className="sticky top-0 z-10 flex h-14 items-center gap-3 border-b bg-background/90 px-4 backdrop-blur md:hidden">
            <SidebarTrigger />
            <Logo />
          </header>
          <main id="main" ref={mainRef} tabIndex={-1} className="mx-auto w-full max-w-6xl flex-1 px-4 py-8 outline-none sm:px-8 md:py-12">
            <GmailAttentionBanner />
            <Outlet />
          </main>
        </div>
      </div>
    </SidebarProvider>
  );
}
