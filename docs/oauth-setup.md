# Google OAuth setup

MailRoutine talks to Gmail through an OAuth client **you** create in Google Cloud. This guide follows Google's current console (“Google Auth Platform”); menu names may shift slightly over time – the official references are:
[Gmail API quickstart](https://developers.google.com/gmail/api/quickstart/go),
[OAuth 2.0 for web server apps](https://developers.google.com/identity/protocols/oauth2/web-server),
[Gmail API scopes](https://developers.google.com/gmail/api/auth/scopes).

## Scope used

MailRoutine requests exactly one scope: **`https://www.googleapis.com/auth/gmail.modify`**. It allows reading message metadata, changing labels (archive, read/unread, custom labels) and moving messages to Trash. It does **not** allow permanent deletion or sending mail, so the broader `https://mail.google.com/` scope is deliberately not used.

`gmail.modify` is a Google **restricted scope**. This matters for how you publish the app (see below).

## Steps

1. Open the [Google Cloud Console](https://console.cloud.google.com/) and **create a project** (or select one), e.g. “MailRoutine”.
2. **Enable the Gmail API**: *APIs & Services → Library → Gmail API → Enable*.
3. Open **Google Auth Platform** (*APIs & Services → OAuth consent screen*) and click **Get started**:
   - App name: `MailRoutine`; user support email: yours.
   - **Audience**: choose **External** (personal Gmail accounts); choose *Internal* only if you use Google Workspace and all mailboxes belong to your organisation.
   - Contact email: yours. Accept the policy and create.
4. **Data Access → Add or remove scopes**: add `https://www.googleapis.com/auth/gmail.modify` and save. (If the scope is not listed, paste it under “Manually add scopes”.)
5. **Audience → Test users**: add every Gmail address you intend to connect (up to 100). Required while the app is in *Testing*.
6. **Clients → Create client**:
   - Application type: **Web application**.
   - **Authorized redirect URIs**: add `<APP_URL>/api/accounts/google/callback`, for example `http://localhost:8080/api/accounts/google/callback` or `https://mail.example.com/api/accounts/google/callback`. It must match exactly (scheme, host, port, path). MailRoutine shows the exact value under *Settings → About*.
   - Create, then copy the **Client ID** and **Client secret**.
7. Put them in `.env`:
   ```env
   GOOGLE_CLIENT_ID=1234-abc.apps.googleusercontent.com
   GOOGLE_CLIENT_SECRET=GOCSPX-...
   ```
8. Start MailRoutine, sign in, go to **Settings → Connect a Gmail account**.
9. On Google's page choose the account. You will see a warning *“Google hasn't verified this app”* (see below): click **Advanced → Go to MailRoutine (unsafe)**, then approve the requested Gmail permission.
10. You return to **Settings** with the account listed as *connected*.
11. Create a test routine (e.g. query `label:inbox older_than:1y`, action *Mark as read*), use **Preview matches** and **Dry run** to confirm everything works.

## Testing vs. production status (important)

| Status | Refresh token lifetime | Notes |
|---|---|---|
| **Testing** (External) | **Expires after 7 days** | Fine for trying MailRoutine out. After 7 days the account shows *needs reconnect* and you must reconnect it. |
| **In production**, unverified | No 7-day expiry | Warning screen stays; **100 lifetime users cap** (sufficient for personal use). |
| In production, verified | No 7-day expiry | Requires Google's verification; for restricted scopes this includes a third-party security assessment (CASA). Not needed for a personal, self-hosted installation. |

For personal long-running use: in **Google Auth Platform → Audience**, click **Publish app** to move to *In production*. Because you are the developer connecting your own accounts, you can keep it unverified and click through the warning. Google may change these policies; check the [official documentation](https://support.google.com/cloud/answer/10311615) and do not assume tokens are valid indefinitely. Tokens can also be revoked at any time (by you at <https://myaccount.google.com/permissions>, by a password change in some cases, or after ~6 months without use) – MailRoutine then marks the account *needs reconnect*.

Unverified-app limits: the consent screen warning, the 100-user cap, and no ability to remove it without verification. Public multi-user hosting would require verification; this guide only covers self-hosting for yourself.

## Redirect URIs and private LAN addresses

Google only accepts these redirect URI forms for web clients: `https://` on a real domain, or `http://localhost` / `http://127.0.0.1` (with any port). **A plain-HTTP private address such as `http://192.168.1.50:8080/...` or `http://nas.local/...` is rejected.** Options:

- Use `http://localhost:8080` when browsing on the same machine (or via an SSH tunnel: `ssh -L 8080:localhost:8080 server`).
- Put MailRoutine behind an HTTPS reverse proxy with a real domain (see the Caddy example in the README) and set `APP_URL=https://your.domain`.

`APP_URL` and the browser address must agree, otherwise the session cookie and the OAuth state cookie will not be sent back.

## Troubleshooting

- **`redirect_uri_mismatch`** – the URI in Google Cloud differs from `GOOGLE_REDIRECT_URL` / `APP_URL + /api/accounts/google/callback`. Compare character by character (trailing slash, http vs https, port). Changes can take a few minutes to apply.
- **`access_denied`** – you cancelled, or the Google account is not on the **Test users** list while the app is in Testing. Add it under Audience → Test users.
- **“Google hasn't verified this app”** – expected for unverified apps (see above).
- **“Token expired or revoked” / account shows *needs reconnect*** – Testing-mode tokens last 7 days, or access was revoked. Use *Settings → Reconnect*.
- **`invalid_client` / token exchange fails** – wrong or mistyped Client ID/secret, or the client was deleted. Re-copy from the console.
- **“Gmail API has not been used in project … or it is disabled”** – enable the Gmail API (step 2) and wait a minute. MailRoutine reports this as *profile_failed* after connecting.
- **“Gmail permission missing” after signing in** – Google lets you untick individual permissions on the consent screen. MailRoutine checks the granted scopes straight after sign-in and refuses to store an account that lacks `gmail.modify`; click **Reconnect Gmail** and leave every requested permission ticked. The same message appears for an existing account if Gmail later answers “insufficient authentication scopes”: the account is flagged *needs reconnect*.
- **No refresh token returned** – you previously granted access. Remove MailRoutine at <https://myaccount.google.com/permissions> and connect again (MailRoutine always requests `prompt=consent`, so this is rare).
