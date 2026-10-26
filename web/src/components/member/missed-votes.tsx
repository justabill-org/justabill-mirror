import Link from "next/link";
import type { MemberAttendance, MemberAttendanceEntry } from "@/lib/types";
import { ordinal } from "@/lib/graph";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";

/**
 * A member's missed roll-call votes per congress under `attendance-v1` (#1001): the latest congress
 * as "44 of 782 roll calls (5.6%)" with its chamber's median, each earlier one on a line. The count
 * always comes before the percentage, so a member sworn in late with a handful of roll calls isn't
 * read as having missed a term's worth; the median is a number only, never "better" or "worse".
 * Null attendance failed to load (as `RecentVotes`' null votes), which is not the same as none.
 */
export function MissedVotes({ attendance }: { attendance: MemberAttendance | null }) {
  const [latest, ...earlier] = attendance?.congresses ?? [];
  return (
    <section aria-label="Missed votes">
      <Card>
        <CardHeader>
          <CardTitle className="text-lg">Missed votes</CardTitle>
        </CardHeader>
        <CardContent>
          {attendance === null ? (
            <p className="text-sm text-muted-foreground">
              Missed votes couldn&apos;t be loaded right now. Try again in a minute.
            </p>
          ) : !latest ? (
            <p className="text-sm text-muted-foreground">No roll-call votes on record.</p>
          ) : (
            <>
              <dl className="text-sm">
                <div>
                  <dt className="text-muted-foreground">{congressLabel(latest)}</dt>
                  <dd className="mt-1 text-base font-medium text-foreground tabular-nums">{countLine(latest)}</dd>
                  <dd className="mt-0.5 text-muted-foreground tabular-nums">{medianLine(latest)}</dd>
                </div>
                {earlier.map((entry, i) => (
                  <div
                    key={`${entry.congress}-${entry.chamber}`}
                    className={`border-t border-border py-2 ${i === 0 ? "mt-3" : ""}`}
                  >
                    <dt className="text-muted-foreground">{congressLabel(entry)}</dt>
                    <dd className="text-foreground tabular-nums">
                      {countLine(entry)} · {medianLine(entry)}
                    </dd>
                  </div>
                ))}
              </dl>
              <p className="mt-4 text-sm text-muted-foreground">
                Roll-call votes the member was listed on, except quorum calls. &ldquo;Present&rdquo; counts as
                voting. Many things keep a member off the floor; this doesn&apos;t say why.{" "}
                <Link
                  href="/methodology#missed-votes"
                  className="text-link underline underline-offset-2 hover:text-foreground"
                >
                  How we count missed votes
                </Link>
              </p>
            </>
          )}
        </CardContent>
      </Card>
    </section>
  );
}

function congressLabel(entry: MemberAttendanceEntry): string {
  return `${ordinal(entry.congress)} Congress · ${entry.chamber}`;
}

/** "44 of 782 roll calls (5.6%)": the count first, then the API's rounded percentage. */
function countLine(entry: MemberAttendanceEntry): string {
  return `${entry.missed.toLocaleString("en-US")} of ${entry.eligible.toLocaleString("en-US")} roll calls (${pct(entry.missed_pct)})`;
}

function medianLine(entry: MemberAttendanceEntry): string {
  return `${entry.chamber} median ${pct(entry.chamber_median_pct)}`;
}

function pct(value: number): string {
  return `${value.toFixed(1)}%`;
}
