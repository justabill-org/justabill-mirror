import Link from "next/link";

// The body of the not-found, error and global-error pages (#73): a heading, a sentence and
// a few ways onward. It has no hooks, so server and client pages can both render it.

export interface StatusLink {
  href: string;
  label: string;
}

interface StatusPageProps {
  /** A short code shown above the title, e.g. "404". */
  code: string;
  title: string;
  description: string;
  links?: StatusLink[];
  /** Extra content under the description, e.g. a retry button. */
  children?: React.ReactNode;
}

export const HOME_LINKS: StatusLink[] = [
  { href: "/", label: "Go home" },
  { href: "/bills", label: "Browse bills" },
];

export function StatusPage({ code, title, description, links = HOME_LINKS, children }: StatusPageProps) {
  return (
    <div className="mx-auto flex max-w-xl flex-col items-center px-4 py-24 text-center sm:px-6">
      <p className="text-sm font-semibold text-muted-foreground">{code}</p>
      <h1 className="mt-2 text-3xl font-semibold tracking-tight text-foreground sm:text-4xl">{title}</h1>
      <p className="mt-4 text-pretty text-muted-foreground">{description}</p>
      {children}
      {links.length > 0 && (
        <nav aria-label="Where to go next" className="mt-8 flex flex-wrap justify-center gap-4 text-sm font-medium">
          {links.map((link) => (
            <Link key={link.href} href={link.href} className="text-link hover:underline">
              {link.label}
            </Link>
          ))}
        </nav>
      )}
    </div>
  );
}
