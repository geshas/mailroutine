import type { ActionType, Routine, ScheduleType } from "./api/types";

const DAYS = ["Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday"];

export function relativeDay(isoStr?: string): string {
  if (!isoStr) return "—";
  const d = new Date(isoStr);
  const today = new Date();
  const startOf = (x: Date) => new Date(x.getFullYear(), x.getMonth(), x.getDate()).getTime();
  const diff = Math.round((startOf(d) - startOf(today)) / 86_400_000);
  const time = d.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", hour12: false });
  if (diff === 0) return `Today, ${time}`;
  if (diff === -1) return `Yesterday, ${time}`;
  if (diff === 1) return `Tomorrow, ${time}`;
  return `${d.toLocaleDateString([], { day: "numeric", month: "short" })}, ${time}`;
}

export function fullDate(isoStr?: string): string {
  if (!isoStr) return "—";
  return new Date(isoStr).toLocaleString([], {
    day: "numeric", month: "short", year: "numeric", hour: "2-digit", minute: "2-digit", hour12: false,
  });
}

export function duration(start: string, end?: string): string {
  if (!end) return "—";
  const s = Math.max(0, Math.round((+new Date(end) - +new Date(start)) / 1000));
  return s < 60 ? `${s}s` : `${Math.floor(s / 60)}m ${s % 60}s`;
}

export const num = (n: number) => n.toLocaleString("en-US");

export const ACTION_LABELS: Record<ActionType, string> = {
  archive: "Archive",
  trash: "Move to Trash",
  mark_read: "Mark as read",
  mark_unread: "Mark as unread",
  add_label: "Add label",
  remove_label: "Remove label",
};

export const SCHEDULE_LABELS: Record<ScheduleType, string> = {
  manual: "Manual only",
  hourly: "Hourly",
  daily: "Daily",
  weekly: "Weekly",
  monthly: "Monthly",
};

/** Label added to handled emails when a routine has traceability on (mirrors the server; checked by the contract test). */
export const TRACE_LABELS: Record<ActionType, string> = {
  archive: "MailRoutine/Archive",
  trash: "MailRoutine/Trash",
  mark_read: "MailRoutine/Read",
  mark_unread: "MailRoutine/Unread",
  add_label: "MailRoutine/AddLabel",
  remove_label: "MailRoutine/RemoveLabel",
};

export function describeAction(a: Routine["action"]) {
  return a.label ? `${ACTION_LABELS[a.type]} “${a.label}”` : ACTION_LABELS[a.type];
}

export function describeSchedule(s: Routine["schedule"]) {
  switch (s.type) {
    case "manual": return "Manual only";
    case "hourly": return `Hourly at :${String(s.minute ?? 0).padStart(2, "0")}`;
    case "daily": return `Daily at ${s.time ?? "00:00"}`;
    case "weekly": return `Weekly on ${DAYS[s.dayOfWeek ?? 0]} at ${s.time ?? "00:00"}`;
    case "monthly": return `Monthly on day ${s.dayOfMonth ?? 1} at ${s.time ?? "00:00"}`;
  }
}

export { DAYS };

export interface QueryBuilder {
  /** Only mail currently in the Inbox (`in:inbox`), so already-archived mail is ignored. */
  inboxOnly: boolean;
  label: string;
  olderThan: string;
  olderUnit: "d" | "m" | "y";
  from: string;
  /** Received on/after this date (YYYY-MM-DD), empty = no lower bound. */
  after: string;
  /** Received before this date (YYYY-MM-DD), empty = no upper bound. */
  before: string;
  read: "any" | "read" | "unread";
  starred: "any" | "starred" | "not_starred";
}

export function buildGmailQuery(b: QueryBuilder): string {
  const parts: string[] = [];
  if (b.inboxOnly) parts.push("in:inbox");
  if (b.label.trim()) parts.push(`label:${b.label.trim().replace(/\s+/g, "-")}`);
  if (b.from.trim()) parts.push(`from:${b.from.trim()}`);
  if (b.olderThan && Number(b.olderThan) > 0) parts.push(`older_than:${Number(b.olderThan)}${b.olderUnit}`);
  // Gmail date operators use YYYY/MM/DD: `after:` is inclusive, `before:` exclusive.
  if (b.after) parts.push(`after:${b.after.replaceAll("-", "/")}`);
  if (b.before) parts.push(`before:${b.before.replaceAll("-", "/")}`);
  if (b.read === "read") parts.push("is:read");
  if (b.read === "unread") parts.push("is:unread");
  if (b.starred === "starred") parts.push("is:starred");
  if (b.starred === "not_starred") parts.push("-is:starred");
  return parts.join(" ");
}

export const emptyQueryBuilder: QueryBuilder = {
  inboxOnly: false, label: "", olderThan: "", olderUnit: "d", from: "", after: "", before: "", read: "any", starred: "any",
};

/** True when both bounds are set and the window would match nothing. */
export function windowIsEmpty(b: Pick<QueryBuilder, "after" | "before">): boolean {
  return !!b.after && !!b.before && b.after >= b.before;
}

/** True when the query already restricts itself to the Inbox. */
export function queryTargetsInbox(query: string): boolean {
  return /(^|\s)(in|label):inbox(\s|$)/i.test(query);
}
