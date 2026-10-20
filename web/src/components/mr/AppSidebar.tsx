import { Link, useNavigate, useRouterState } from "@tanstack/react-router";
import { Coffee, History, LayoutDashboard, LogOut, Monitor, Moon, Repeat, Settings, Sun } from "lucide-react";
import { useQueryClient } from "@tanstack/react-query";
import {
  Sidebar,
  SidebarContent,
  SidebarFooter,
  SidebarGroup,
  SidebarGroupContent,
  SidebarHeader,
  SidebarMenu,
  SidebarMenuButton,
  SidebarMenuItem,
  useSidebar,
} from "@/components/ui/sidebar";
import { ToggleGroup, ToggleGroupItem } from "@/components/ui/toggle-group";
import { Button } from "@/components/ui/button";
import { Logo } from "./common";
import { useTheme } from "@/lib/theme";
import { api } from "@/lib/api/client";
import type { ThemePreference } from "@/lib/api/types";

const items = [
  { title: "Dashboard", url: "/dashboard", icon: LayoutDashboard },
  { title: "Routines", url: "/routines", icon: Repeat },
  { title: "Run history", url: "/runs", icon: History },
  { title: "Settings", url: "/settings", icon: Settings },
] as const;

export function AppSidebar({ username }: { username: string }) {
  const path = useRouterState({ select: (s) => s.location.pathname });
  const { theme, setTheme } = useTheme();
  const { setOpenMobile } = useSidebar();
  const navigate = useNavigate();
  const qc = useQueryClient();

  async function logout() {
    await qc.cancelQueries();
    qc.clear();
    await api.logout();
    navigate({ to: "/login", replace: true });
  }

  return (
    <Sidebar>
      <SidebarHeader className="px-4 py-5">
        <Logo />
      </SidebarHeader>
      <SidebarContent>
        <SidebarGroup>
          <SidebarGroupContent>
            <SidebarMenu>
              {items.map((item) => (
                <SidebarMenuItem key={item.url}>
                  <SidebarMenuButton asChild isActive={path.startsWith(item.url)}>
                    <Link to={item.url} onClick={() => setOpenMobile(false)}>
                      <item.icon />
                      <span>{item.title}</span>
                    </Link>
                  </SidebarMenuButton>
                </SidebarMenuItem>
              ))}
            </SidebarMenu>
          </SidebarGroupContent>
        </SidebarGroup>
      </SidebarContent>
      <SidebarFooter className="gap-3 p-3">
        <Button variant="outline" asChild>
          <a href="https://paypal.me/geshasoft" target="_blank" rel="noopener noreferrer">
            <Coffee aria-hidden="true" />
            Buy me a coffee
            <span className="sr-only"> (opens PayPal in a new tab)</span>
          </a>
        </Button>
        <ToggleGroup
          type="single"
          value={theme}
          onValueChange={(v) => v && setTheme(v as ThemePreference)}
          className="w-full rounded-md border bg-background p-0.5"
        >
          {[
            { v: "light", i: Sun, l: "Light" },
            { v: "dark", i: Moon, l: "Dark" },
            { v: "system", i: Monitor, l: "System" },
          ].map(({ v, i: Icon, l }) => (
            <ToggleGroupItem key={v} value={v} aria-label={`${l} theme`} className="h-7 flex-1">
              <Icon className="size-3.5" />
            </ToggleGroupItem>
          ))}
        </ToggleGroup>
        <div className="flex items-center gap-2.5 rounded-md px-1 py-1">
          <div className="flex size-8 items-center justify-center rounded-full bg-accent text-xs font-semibold uppercase text-accent-foreground">
            {username.slice(0, 2)}
          </div>
          <div className="min-w-0 flex-1">
            <p className="truncate text-sm font-medium">{username}</p>
            <p className="text-xs text-muted-foreground">Administrator</p>
          </div>
          <button
            onClick={logout}
            aria-label="Log out"
            title="Log out"
            className="flex size-9 items-center justify-center rounded-md text-muted-foreground transition-colors hover:bg-sidebar-accent hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-sidebar-ring"
          >
            <LogOut className="size-4" />
          </button>
        </div>
      </SidebarFooter>
    </Sidebar>
  );
}
