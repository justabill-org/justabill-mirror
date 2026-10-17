import { Children, type ReactNode } from "react";
import { cn } from "@/lib/utils";

/** How serious a message is: an error the visitor must act on, or a quieter info or success note. */
export type AlertVariant = "info" | "success" | "error";

export interface AlertProps {
  variant?: AlertVariant;
  /** A short bold first line, e.g. "Your vote wasn't saved". */
  title?: string;
  /** The message. Leave it empty until there is one: the region must be in the page before it fills. */
  children?: ReactNode;
  /** Classes for the filled box (margins, width); an empty alert ignores them. */
  className?: string;
}

// After USWDS's alert guidance (#951, option U3), in our own tokens: the role follows the
// severity, and an icon with a name says it too, so color is never the only signal. The text is
// --foreground on the variant's 10% tint, and the icon is the variant's color on that tint; both
// pairs are in contrast.test.ts.
const VARIANTS: Record<AlertVariant, { role: "alert" | "status"; label: string; box: string; icon: string }> = {
  error: {
    role: "alert",
    label: "Error",
    box: "border-destructive/30 bg-destructive/10",
    icon: "text-destructive",
  },
  success: {
    role: "status",
    label: "Success",
    box: "border-success/30 bg-success/10",
    icon: "text-success",
  },
  info: {
    role: "status",
    label: "Note",
    box: "border-border bg-muted",
    icon: "text-muted-foreground",
  },
};

/**
 * A message announced to screen readers: `role="alert"` for errors, `role="status"` otherwise.
 *
 * Render it before there's anything to say and fill it later (`<Alert variant="error">{error}</Alert>`
 * with `error` null at first), because a live region that mounts with its text may go unannounced.
 * Empty, it stays in the page as a visually hidden region that takes no space.
 */
export function Alert({ variant = "info", title, children, className }: AlertProps) {
  const v = VARIANTS[variant];
  const filled = Boolean(title) || Children.toArray(children).some((child) => child !== "");
  if (!filled) return <div role={v.role} className="sr-only" />;
  return (
    <div
      role={v.role}
      className={cn("flex items-start gap-2 rounded-lg border px-3 py-2 text-left text-sm text-foreground", v.box, className)}
    >
      <AlertIcon variant={variant} label={v.label} className={cn("mt-0.5 h-4 w-4 shrink-0", v.icon)} />
      <div className="min-w-0 flex-1">
        {title && <p className="font-semibold">{title}</p>}
        {children}
      </div>
    </div>
  );
}

function AlertIcon({ variant, label, className }: { variant: AlertVariant; label: string; className: string }) {
  const paths: Record<AlertVariant, string> = {
    // A circle with an exclamation mark, a check, or an "i".
    error: "M12 8v4m0 4h.01M21 12a9 9 0 11-18 0 9 9 0 0118 0z",
    success: "M9 12l2 2 4-4m6 2a9 9 0 11-18 0 9 9 0 0118 0z",
    info: "M12 16v-4m0-4h.01M21 12a9 9 0 11-18 0 9 9 0 0118 0z",
  };
  return (
    <svg
      role="img"
      aria-label={label}
      className={className}
      fill="none"
      viewBox="0 0 24 24"
      stroke="currentColor"
      strokeWidth={2}
    >
      <path strokeLinecap="round" strokeLinejoin="round" d={paths[variant]} />
    </svg>
  );
}
