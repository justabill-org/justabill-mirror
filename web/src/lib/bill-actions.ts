import type { BillAction } from "./types";

/** The source Congress.gov's "Actions Overview" (its major actions) comes from. */
export const OVERVIEW_SOURCE = "Library of Congress";

/** One action as the timeline shows it: the same action from several sources merged into one. */
export interface MergedAction {
  key: string;
  date: string;
  text: string;
  /** Every source that recorded it, in the order first seen. */
  sources: string[];
  recordedVote?: BillAction["recorded_vote"];
  /** On Congress.gov's overview of major actions, or a recorded roll-call vote. */
  isKey: boolean;
}

/** The action's text with whitespace collapsed, so the same words from two sources match. */
function normalize(text: string): string {
  return text.replace(/\s+/g, " ").trim();
}

/**
 * The bill's actions, newest first, with actions of the same day and text merged and their
 * sources listed together. An action is key when Congress.gov's overview lists it (the Library of
 * Congress source) or it has a roll-call vote.
 */
export function mergeActions(actions: BillAction[]): MergedAction[] {
  const sorted = [...actions].sort(
    (a, b) => b.action_date.localeCompare(a.action_date) || a.sort_order - b.sort_order,
  );
  const merged = new Map<string, MergedAction>();
  for (const action of sorted) {
    const text = normalize(action.action_text);
    const key = `${action.action_date.slice(0, 10)}|${text}`;
    let entry = merged.get(key);
    if (!entry) {
      entry = { key, date: action.action_date, text, sources: [], isKey: false };
      merged.set(key, entry);
    }
    const source = action.source_system;
    if (source && !entry.sources.includes(source)) entry.sources.push(source);
    if (action.recorded_vote && !entry.recordedVote) entry.recordedVote = action.recorded_vote;
    if (source === OVERVIEW_SOURCE || action.recorded_vote) entry.isKey = true;
  }
  return [...merged.values()];
}
