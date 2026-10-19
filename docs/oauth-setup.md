# Google OAuth setup

MailRoutine talks to Gmail through an OAuth client **you** create in Google Cloud. This guide follows Google's current console (“Google Auth Platform”); menu names may shift slightly over time – the official references are:
[Gmail API quickstart](https://developers.google.com/gmail/api/quickstart/go),
[OAuth 2.0 for web server apps](https://developers.google.com/identity/protocols/oauth2/web-server),
[Gmail API scopes](https://developers.google.com/gmail/api/auth/scopes).

For this personal, self-hosted setup, leave only the basic, non-sensitive scopes in the console's **Data Access** list; **do not manually add `gmail.modify` there**. Set **Audience → Publishing status → In production** before connecting Gmail and keep your personal app **unverified**. Leaving it in **Testing** causes Gmail refresh tokens to expire after seven days and requires you to reconnect each account. MailRoutine requests the Gmail permission itself when you connect an account, as explained below.

## Scope used

MailRoutine requests exactly one scope: **`https://www.googleapis.com/auth/gmail.modify`**. There is no `gmail.notify` scope in Google's Gmail API scope list. MailRoutine uses `gmail.modify` to read message metadata, change labels (archive, read/unread, custom labels) and move messages to Trash. The permission itself also allows reading message bodies and composing/sending mail, although MailRoutine does not use those capabilities. It does **not** allow immediate permanent deletion, so the broader `https://mail.google.com/` scope is deliberately not used.

`gmail.modify` is a Google **restricted scope**. Publishing status, scope declarations and verification are separate settings:

- **Publishing status** controls the seven-day Testing limit. Choose **In production** to remove that limit.
- **Data Access in this setup** keeps only basic, non-sensitive identity scopes, such as `openid`, `userinfo.email` and `userinfo.profile`, if present. Do not manually add `gmail.modify` or start a restricted-scope verification submission for your personal installation. These identity scopes do not authorize Gmail routines: MailRoutine requests `gmail.modify` separately in the OAuth authorization URL when you connect Gmail, and you must approve that permission on Google's consent screen.
- **Verification** is a separate review. Declaring `gmail.modify` identifies restricted access, but does not by itself mean you must pay for verification. Personal use qualifies for Google's verification exception. Public use generally requires verification, and server access to restricted data can require an annual third-party security assessment (CASA), with costs agreed between the developer and assessor. See Google's [verification exceptions](https://developers.google.com/identity/protocols/oauth2/production-readiness/restricted-scope-verification#exceptions_to_verification_requirements) and [assessment FAQ](https://support.google.com/cloud/answer/13463817).

Google's general guidance is to declare all scopes an app uses. Keeping only basic scopes in the console does not make MailRoutine's runtime Gmail access non-sensitive or provide a verification exemption. This personal setup relies on the **personal-use exception**; a public deployment must declare its actual scopes and meet Google's applicable verification requirements.

## Steps

1. Open the [Google Cloud Console](https://console.cloud.google.com/) and **create a project** (or select one), e.g. “MailRoutine”.
2. **Enable the Gmail API**: *APIs & Services → Library → Gmail API → Enable*.
3. Open **Google Auth Platform** (*APIs & Services → OAuth consent screen*) and click **Get started**:
   - App name: `MailRoutine`; user support email: yours.
   - **Audience**: choose **External** (personal Gmail accounts); choose *Internal* only if you use Google Workspace and all mailboxes belong to your organisation.
   - Contact email: yours. Accept the policy and create.
4. **Data Access**: leave only the basic, non-sensitive identity scopes, if present. **Do not add `https://www.googleapis.com/auth/gmail.modify`** to this list or submit a restricted-scope verification request for this personal setup. MailRoutine requests the required Gmail permission automatically when you connect an account in step 8.
5. **Audience → Publish app**: confirm that the publishing status is **In production**. Do this **before connecting Gmail** to avoid the seven-day refresh-token expiry. Publishing the OAuth app does not publish your MailRoutine server on the internet or require you to complete verification. The **Test users** list is only needed if you deliberately stay in Testing for a short trial.
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
| In production, verified | No 7-day expiry | Google's review; server access to restricted data can require an annual third-party security assessment (CASA), which can cost money. Personal use qualifies for a verification exception. |

For personal long-running use: in **Google Auth Platform → Audience**, click **Publish app** to move to *In production*. Because you are the developer connecting your own accounts, you can keep it unverified and click through the warning. **Publishing removes the Testing limit; removing Gmail from the console scope list does not.** Google's exception for basic identity scopes in Testing applies only when those are the scopes actually requested, which is not the case for MailRoutine. See [Google's refresh-token expiration rules](https://developers.google.com/identity/protocols/oauth2#expiration).

**Already connected while in Testing?** After publishing, use **Settings → Reconnect** for each Gmail account to obtain a new refresh token issued in production. Do not rely on publishing to extend a token already issued with a seven-day lifetime.

Production tokens can still expire or be revoked (by you at <https://myaccount.google.com/permissions>, by a password change in some cases, or after six months without use). MailRoutine then marks the account *needs reconnect*. Production removes the fixed seven-day Testing expiry; it does not guarantee access forever.

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
- **“Token expired or revoked” / account shows *needs reconnect*** – if this repeats every seven days, first set **Audience → Publish app → In production**, then use **Settings → Reconnect** for each account to replace its Testing token. In production, check whether access was revoked or another token-expiration rule applies.
- **`invalid_client` / token exchange fails** – wrong or mistyped Client ID/secret, or the client was deleted. Re-copy from the console.
- **“Gmail API has not been used in project … or it is disabled”** – enable the Gmail API (step 2) and wait a minute. MailRoutine reports this as *profile_failed* after connecting.
- **“Gmail permission missing” after signing in** – Google lets you untick individual permissions on the consent screen. MailRoutine checks the granted scopes straight after sign-in and refuses to store an account that lacks `gmail.modify`; click **Reconnect Gmail** and leave every requested permission ticked. The same message appears for an existing account if Gmail later answers “insufficient authentication scopes”: the account is flagged *needs reconnect*.
- **No refresh token returned** – you previously granted access. Remove MailRoutine at <https://myaccount.google.com/permissions> and connect again (MailRoutine always requests `prompt=consent`, so this is rare).
