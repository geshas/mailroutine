import { createFileRoute, useNavigate } from "@tanstack/react-router";
import { useState } from "react";
import { Loader2 } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Logo } from "@/components/mr/common";
import { api } from "@/lib/api/client";
import { usePageTitle } from "@/lib/title";

export const Route = createFileRoute("/login")({
  component: LoginPage,
});

function LoginPage() {
  usePageTitle("Sign in");
  const navigate = useNavigate();
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(false);

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    setError(null);
    setLoading(true);
    try {
      await api.login(username, password);
      navigate({ to: "/dashboard" });
    } catch (err) {
      setError(err instanceof Error ? err.message : "Sign in failed");
    } finally {
      setLoading(false);
    }
  }

  return (
    <div className="flex min-h-screen flex-col items-center justify-center bg-background px-4">
      <div className="w-full max-w-sm">
        <h1 className="sr-only">Sign in to MailRoutine</h1>
        <div className="mb-8 flex flex-col items-center text-center">
          <Logo className="mb-3 [&_span]:text-lg" />
          <p className="text-sm text-muted-foreground">Scheduled automation for Gmail</p>
        </div>
        <form
          onSubmit={submit}
          className="space-y-4 rounded-lg border bg-card p-6 shadow-subtle"
        >
          <div className="space-y-2">
            <Label htmlFor="username">Username</Label>
            <Input id="username" aria-describedby={error ? "login-error" : undefined} autoComplete="username" value={username} onChange={(e) => setUsername(e.target.value)} required autoFocus />
          </div>
          <div className="space-y-2">
            <Label htmlFor="password">Password</Label>
            <Input id="password" aria-describedby={error ? "login-error" : undefined} aria-invalid={!!error} type="password" autoComplete="current-password" value={password} onChange={(e) => setPassword(e.target.value)} required />
          </div>
          {error && <p id="login-error" role="alert" className="text-sm text-destructive">{error}</p>}
          <Button type="submit" className="w-full" disabled={loading}>
            {loading && <Loader2 className="animate-spin" />}
            Sign in
          </Button>
        </form>
        <p className="mt-6 text-center text-xs text-muted-foreground">Self-hosted Gmail automation</p>
      </div>
    </div>
  );
}
