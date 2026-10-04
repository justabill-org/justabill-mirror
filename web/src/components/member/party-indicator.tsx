interface PartyIndicatorProps {
  party: string;
  size?: "sm" | "md" | "lg";
  showLabel?: boolean;
}

const partyColors: Record<string, string> = {
  D: "bg-party-d",
  R: "bg-party-r",
  I: "bg-party-i",
  ID: "bg-party-i",
};

const partyLabels: Record<string, string> = {
  D: "Democrat",
  R: "Republican",
  I: "Independent",
  ID: "Independent",
};

export function PartyIndicator({ party, size = "md", showLabel = false }: PartyIndicatorProps) {
  const color = partyColors[party] || "bg-muted-foreground";
  const label = partyLabels[party] || party;

  const sizes = {
    sm: "h-2 w-2",
    md: "h-2.5 w-2.5",
    lg: "h-3 w-3",
  };

  return (
    <span className="inline-flex items-center gap-1.5">
      <span className={`${sizes[size]} shrink-0 rounded-full ${color}`} aria-hidden="true" />
      {/* Never color alone: the name is visible, or read to screen readers (once, not twice). */}
      <span className={showLabel ? "text-sm" : "sr-only"}>{label}</span>
    </span>
  );
}
