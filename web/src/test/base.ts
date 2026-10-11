import { accountBody, dashboardBody, previewBody, routineBody, runBody, sessionBody, settingsBody } from './fixtures'

/** Routes every signed-in screen needs. Individual tests override what they exercise. */
export const signedIn = {
  'GET /api/auth/session': { body: sessionBody },
  'GET /api/settings': { body: settingsBody },
  'GET /api/accounts': { body: { items: [accountBody] } },
  'GET /api/accounts/acc1/labels': { body: { items: [{ id: 'L1', name: 'Newsletters', type: 'user' }, { id: 'INBOX', name: 'INBOX', type: 'system' }] } },
  'GET /api/accounts/acc1/labels/mailroutine': { body: { items: [{ name: 'MailRoutine', exists: true }, { name: 'MailRoutine/Archive', action: 'archive', exists: true }], created: [] } },
  'GET /api/dashboard': { body: dashboardBody },
  'GET /api/routines': { body: { items: [routineBody] } },
  'GET /api/routines/r1': { body: routineBody },
  'GET /api/runs': { body: { items: [runBody], total: 1, limit: 26, offset: 0 } },
  'GET /api/runs/run1': { body: { run: runBody, actions: [] } },
  'POST /api/routines/preview': { body: previewBody },
  'POST /api/routines/r1/preview': { body: previewBody },
}
