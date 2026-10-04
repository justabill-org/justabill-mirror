import type { HTMLAttributes } from "react";

export interface BadgeProps extends HTMLAttributes<HTMLSpanElement> {
  variant?: "default" | "secondary" | "outline" | "success" | "destructive" | "party-d" | "party-r" | "party-i";
}

export function Badge({
  className = "",
  variant = "default",
  children,
  ...props
}: BadgeProps) {
  const baseStyles =
    "inline-flex items-center rounded-full px-2.5 py-0.5 text-xs font-medium transition-colors";

  const variants = {
    default: "bg-primary text-primary-foreground",
    secondary: "bg-secondary text-secondary-foreground",
    outline: "border border-border text-foreground",
    success: "bg-success/10 text-success border border-success/20",
    destructive: "bg-destructive/10 text-destructive border border-destructive/20",
    "party-d": "bg-party-d/10 text-party-d border border-party-d/20",
    "party-r": "bg-party-r/10 text-party-r border border-party-r/20",
    "party-i": "bg-party-i/10 text-party-i border border-party-i/20",
  };

  return (
    <span className={`${baseStyles} ${variants[variant]} ${className}`} {...props}>
      {children}
    </span>
  );
}
