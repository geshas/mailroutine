// Backend wire-format fixtures (what the Go server really sends).
export const sessionBody = {
  authenticated: true,
  username: 'admin',
  expires_at: '2099-01-01T00:00:00Z',
  idle_timeout_seconds: 1800,
  renewal_threshold_seconds: 300,
  csrf_token: 'csrf-token-1',
}

export const settingsBody = {
  timezone: 'UTC',
  version: '1.2.3',
  license: 'Apache-2.0',
  repository_url: 'https://github.com/geshas/mailroutine',
  documentation_url: 'https://github.com/geshas/mailroutine#readme',
  google_redirect_url: 'http://localhost:8080/api/accounts/google/callback',
  default_max_messages_per_run: 500,
}

export const accountBody = { id: 'acc1', provider: 'gmail', email: 'me@gmail.com', connection_status: 'connected', created_at: '2026-01-01T00:00:00Z' }
export const expiredAccountBody = { ...accountBody, id: 'acc2', email: 'old@gmail.com', connection_status: 'needs_reauth' }

export const routineBody = {
  id: 'r1',
  account_id: 'acc1',
  account_email: 'me@gmail.com',
  name: 'Archive news',
  description: '',
  gmail_query: 'label:news older_than:7d',
  action_type: 'archive',
  action_config: {},
  schedule_type: 'daily',
  schedule_config: { timezone: 'UTC', time: '03:00' },
  enabled: false,
  traceability: true,
  running: false,
  max_messages_per_run: 100,
  last_run_at: null,
  last_run_handled: null,
  next_run_at: null,
  created_at: '2026-01-01T00:00:00Z',
  updated_at: '2026-01-01T00:00:00Z',
}

export const runBody = {
  id: 'run1',
  routine_id: 'r1',
  routine_name: 'Archive news',
  started_at: '2026-01-02T03:00:00Z',
  finished_at: '2026-01-02T03:00:05Z',
  status: 'success',
  matched_count: 5,
  processed_count: 5,
  failed_count: 0,
  error_message: '',
  dry_run: false,
  trigger: 'schedule',
}

export const dashboardBody = {
  total_runs: 7,
  last_run: runBody,
  next_run: { routine_id: 'r1', routine_name: 'Archive news', at: '2099-01-02T03:00:00Z' },
  emails_handled: 42,
  enabled_routines: 2,
  connected_accounts: 1,
  recent_runs: [runBody],
}

export const previewBody = {
  query: 'label:news older_than:7d',
  count: 2,
  count_is_estimate: false,
  has_more: false,
  messages: [
    { id: 'm1', from: 'News <n@example.com>', subject: 'Hello', date: '2026-01-01T00:00:00Z' },
    { id: 'm2', from: 'a@b.c', subject: '', date: '2026-01-02T00:00:00Z' },
  ],
}

export const err = (status: number, code: string, message: string, fields?: Record<string, string>) => ({
  status,
  body: { error: { code, message, ...(fields ? { fields } : {}) } },
})
