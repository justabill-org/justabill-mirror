import { electionDistrictLine, type ElectionLookup } from "@/lib/election-district";

/**
 * The "you vote in" line under the House member in find-my-reps (#375): shown only when the
 * address votes in a different district at the next general election, and nothing otherwise.
 */
export function ElectionDistrictNote({ lookup, today = new Date() }: { lookup: ElectionLookup; today?: Date }) {
  const line = electionDistrictLine(lookup, today);
  if (!line) return null;
  return (
    <div className="space-y-1 rounded-lg border border-border p-3 text-sm">
      <p className="text-foreground">
        {line.lead}
        <strong>{line.districts}</strong>
        {line.rest}
      </p>
      <p className="text-xs text-muted-foreground">
        {line.source} Confirm with your state election office at{" "}
        <a
          href="https://vote.gov"
          target="_blank"
          rel="noopener noreferrer"
          className="underline underline-offset-2 hover:text-foreground"
        >
          vote.gov
        </a>
        .
      </p>
    </div>
  );
}
